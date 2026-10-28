//go:build unix

package launch_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestPaneProcessPassesLauncherContract(t *testing.T) {
	interptest.RunLauncherContract(t, func(t *testing.T) interptest.Harness {
		p := newProcess(t)
		panes := adaptertest.NewShellPanes(t, "lab", "director")
		p.Presentation = &launch.Presentation{Host: panes, Report: func(context.Context, string, herdr.AgentReport) error { return nil }}
		return interptest.Harness{Launcher: p, Adapter: interp.FakeAdapter, Prepare: func(t *testing.T, o interptest.Outcome, render func(string) interp.Spec) interp.Spec {
			return prepared(t, p, render(instructions[o]))
		}}
	})
}

func TestWorkflowPaneOwnsLauncherAndClosingDoesNotStopAttempt(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=10m"))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	var pane string
	p.Presentation = &launch.Presentation{Host: panes, Report: func(_ context.Context, id string, r herdr.AgentReport) error {
		pane = id
		at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}}
		if r.Source != "guildhall" || r.Agent != "artisan" || !reflect.DeepEqual(r.Resume, launch.ResumeArgv(at)) {
			t.Errorf("report = %+v", r)
		}
		return nil
	}, CloseAfter: 5 * time.Minute}
	h, err := p.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Stop(h); err != nil {
			t.Error(err)
		}
	}()
	if pane == "" || len(panes.Sent()) != 1 || !strings.Contains(panes.Sent()[0], "--pane-launch") {
		t.Fatalf("no pane-backed owned launch: pane %q lines %q", pane, panes.Sent())
	}
	// The bridge is told the configured close delay so its finish line can state it.
	if !strings.Contains(panes.Sent()[0], "'--pane-launch' '--workspace-close-after' '5m0s'") {
		t.Errorf("bridge line %q does not pass the close delay", panes.Sent()[0])
	}
	// A second launch, even from another Process after a caller restart, must
	// not type anything while ownership is live or the API outcome is unknown.
	again := &launch.Process{Executable: p.Executable, Env: p.Env, Store: p.Store, StateDir: p.StateDir, Presentation: p.Presentation}
	if _, err := again.Launch(context.Background(), spec); err == nil {
		t.Fatal("duplicate launch accepted")
	}
	if len(panes.Sent()) != 1 {
		t.Fatal("duplicate command typed")
	}
	// Closing the terminal's process is presentation only. ShellPanes' interrupt
	// kills the pane bridge, not its independently owned launcher and worker.
	workspaces, err := panes.Workspaces(context.Background())
	if err != nil || len(workspaces) != 2 || workspaces[1].Label != "└ t7 · build #1" {
		t.Fatalf("expected separate labelled Artisan workspace: %+v, %v", workspaces, err)
	}
	ref := adapter.TabRef{Session: "lab", WorkspaceID: workspaces[1].ID, TabID: workspaces[1].ID + ":t1", PaneID: pane}
	before := runningAttempt(t, p)
	if err := panes.InterruptTab(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, "pane bridge exit", func() bool {
		info, err := panes.PaneProcessInfo(context.Background(), ref)
		return err == nil && info.ShellIdle()
	})
	after := runningAttempt(t, p)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("pane close changed attempt: before %+v after %+v", before, after)
	}
}

func TestAmbiguousPaneLaunchNeverTypesTwice(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, ""))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	refused := errors.New("report refused")
	p.Presentation = &launch.Presentation{Host: panes, Report: func(context.Context, string, herdr.AgentReport) error { return refused }}
	if _, err := p.Launch(context.Background(), spec); !errors.Is(err, refused) {
		t.Fatalf("Launch = %v", err)
	}
	if len(panes.Sent()) != 0 {
		t.Fatal("typed before resume registered")
	}
	if _, err := p.Launch(context.Background(), spec); err == nil {
		t.Fatal("uncertain launch retried")
	}
	if len(panes.Sent()) != 0 {
		t.Fatal("typed on retry")
	}
}

// uncertainTabs models an API response lost after the pane accepted its line.
// Retrying that API operation could start the same attempt twice.
type uncertainTabs struct{ adapter.TabHost }

func (h uncertainTabs) RunInTab(ctx context.Context, ref adapter.TabRef, line string) error {
	if err := h.TabHost.RunInTab(ctx, ref, line); err != nil {
		return err
	}
	return errors.New("lost pane response after execution")
}

func TestPaneLostResponsePreservesWorkerAndNeverRetypes(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=200ms"))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	p.Presentation = &launch.Presentation{Host: uncertainTabs{panes}, Report: func(context.Context, string, herdr.AgentReport) error { return nil }}
	if _, err := p.Launch(context.Background(), spec); err == nil {
		t.Fatal("lost API response not returned")
	}
	if _, err := p.Launch(context.Background(), spec); err == nil {
		t.Fatal("uncertain launch retried")
	}
	if len(panes.Sent()) != 1 {
		t.Fatalf("commands = %q", panes.Sent())
	}
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if a := runningAttempt(t, p); a.Outcome == store.AttemptSucceeded {
			break
		}
		if time.Now().After(end) {
			t.Fatal("uncertain launch did not preserve its running worker")
		}
	}
}

func TestRelativeStorePaneLaunchAndResume(t *testing.T) {
	for _, files := range []bool{false, true} {
		name := "workflow"
		if files {
			name = "judge"
		}
		t.Run(name, func(t *testing.T) {
			p := newProcess(t)
			spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=10m"))
			storePath := p.Store
			t.Chdir(filepath.Dir(storePath))
			p.Store = filepath.Base(storePath)
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			var resume []string
			p.Presentation = &launch.Presentation{Host: panes, Report: func(_ context.Context, _ string, r herdr.AgentReport) error {
				resume = r.Resume
				return nil
			}}
			var h interp.Handle
			var err error
			if files {
				k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
				h, err = p.LaunchFiles(context.Background(), k, spec.Adapter, spec.PromptFile, spec.WorkDir)
			} else {
				h, err = p.Launch(context.Background(), spec)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Stop(h) })
			if err := p.Stop(h); err != nil {
				t.Fatal(err)
			}
			at := launch.Attempt{Store: storePath, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}}
			if !reflect.DeepEqual(resume, launch.ResumeArgv(at)) {
				t.Fatalf("resume = %q", resume)
			}
			cmd := exec.Command(p.Executable, resume[1:]...)
			cmd.Dir = spec.WorkDir
			cmd.Env = append(os.Environ(), p.Env...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("resume from pane directory: %v: %s", err, out)
			}
		})
	}
}

type timeoutTabs struct {
	adapter.TabHost
	phase               string
	creates, interrupts int
	workspaces          int
}

func (h *timeoutTabs) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	h.workspaces++
	w, err := h.TabHost.CreateWorkspace(ctx, label, cwd)
	if err == nil && h.phase == "workspace" {
		<-ctx.Done()
		return w, ctx.Err()
	}
	return w, err
}

func (h *timeoutTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (adapter.TabRef, error) {
	h.creates++
	ref, err := h.TabHost.CreateTab(ctx, spec)
	if err == nil && h.phase == "create" {
		<-ctx.Done()
		return ref, ctx.Err()
	}
	return ref, err
}

func (h *timeoutTabs) PaneProcessInfo(ctx context.Context, ref adapter.TabRef) (herdr.ProcessInfo, error) {
	if h.phase == "shell" {
		<-ctx.Done()
		return herdr.ProcessInfo{}, ctx.Err()
	}
	return h.TabHost.PaneProcessInfo(ctx, ref)
}

func (h *timeoutTabs) RunInTab(ctx context.Context, ref adapter.TabRef, line string) error {
	if err := h.TabHost.RunInTab(ctx, ref, line); err != nil {
		return err
	}
	if h.phase == "type" {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (h *timeoutTabs) InterruptTab(ctx context.Context, ref adapter.TabRef) error {
	h.interrupts++
	return h.TabHost.InterruptTab(ctx, ref)
}

func TestPaneOperationsTimeoutRetainsClaimWithoutRetryOrStop(t *testing.T) {
	for _, phase := range []string{"workspace", "create", "shell", "report", "type"} {
		t.Run(phase, func(t *testing.T) {
			p := newProcess(t)
			p.StartTimeout = 10 * time.Second
			spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=400ms"))
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			host := &timeoutTabs{TabHost: panes, phase: phase}
			reports := 0
			p.OpenPresentation = func(ctx context.Context) (*launch.Presentation, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("presentation resolution has no deadline")
				}
				return &launch.Presentation{Host: host, Report: func(ctx context.Context, _ string, _ herdr.AgentReport) error {
					reports++
					if phase == "report" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				}}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := p.Launch(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Launch = %v", err)
			}
			k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
			if _, err := os.Stat(filepath.Join(k.Dir(p.StateDir), launch.PaneClaim)); err != nil {
				t.Fatalf("claim lost: %v", err)
			}
			beforeReports := reports
			if _, err := p.Launch(context.Background(), spec); err == nil {
				t.Fatal("timed-out launch retried")
			}
			wantSent := 0
			if phase == "type" {
				wantSent = 1
			}
			wantCreates := 1
			if phase == "workspace" {
				wantCreates = 0
			}
			if host.workspaces != 1 || host.creates != wantCreates || reports != beforeReports || len(panes.Sent()) != wantSent || host.interrupts != 0 {
				t.Fatalf("workspaces=%d creates=%d reports=%d sent=%q interrupts=%d", host.workspaces, host.creates, reports, panes.Sent(), host.interrupts)
			}
			if phase == "type" {
				for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					a := runningAttempt(t, p)
					if a.Outcome == store.AttemptSucceeded {
						break
					}
					if a.Outcome != "" || time.Now().After(end) {
						t.Fatalf("timed-out typing did not preserve worker: %+v", a)
					}
				}
			}
		})
	}
}

func runningAttempt(t *testing.T, p *launch.Process) store.StageAttempt {
	t.Helper()
	s, err := store.Open(p.Store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	attempts, err := s.Attempts("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return attempts[0]
}
