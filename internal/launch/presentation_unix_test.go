//go:build unix

package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		p.Presentation = &launch.Presentation{Host: panes, Report: func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error) {
			return herdr.ReportOutcome{}, nil
		}}
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
	p.Presentation = &launch.Presentation{Host: panes, Report: func(_ context.Context, id string, r herdr.AgentReport) (herdr.ReportOutcome, error) {
		pane = id
		at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}}
		if r.Source != "guildhall" || r.Agent != "artisan" || !reflect.DeepEqual(r.Resume, launch.ResumeArgv(at)) {
			t.Errorf("report = %+v", r)
		}
		return herdr.ReportOutcome{}, nil
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
	// An unset pane output is the Workbench, and the bridge is told so.
	if !strings.Contains(panes.Sent()[0], "'--workspace-close-after' '5m0s' '--pane-output' 'workbench'") {
		t.Errorf("bridge line %q does not pass the pane output", panes.Sent()[0])
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

// taskLocking runs fn as subtests with Process.TaskLocked unset and set: a
// claim holding a tab binding is refused either way.
func taskLocking(t *testing.T, fn func(t *testing.T, locked bool)) {
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprintf("task-locked=%t", locked), func(t *testing.T) { fn(t, locked) })
	}
}

func TestAmbiguousPaneLaunchNeverTypesTwice(t *testing.T) {
	taskLocking(t, func(t *testing.T, locked bool) {
		p := newProcess(t)
		p.TaskLocked = locked
		spec := prepared(t, p, fixtureSpec(t, ""))
		panes := adaptertest.NewShellPanes(t, "lab", "director")
		refused := errors.New("report refused")
		p.Presentation = &launch.Presentation{Host: panes, Report: func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error) {
			return herdr.ReportOutcome{}, refused
		}}
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
	})
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
	taskLocking(t, func(t *testing.T, locked bool) {
		p := newProcess(t)
		p.TaskLocked = locked
		spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=200ms"))
		panes := adaptertest.NewShellPanes(t, "lab", "director")
		p.Presentation = &launch.Presentation{Host: uncertainTabs{panes}, Report: func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error) {
			return herdr.ReportOutcome{}, nil
		}}
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
	})
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
			p.Presentation = &launch.Presentation{Host: panes, Report: func(_ context.Context, _ string, r herdr.AgentReport) (herdr.ReportOutcome, error) {
				resume = r.Resume
				return herdr.ReportOutcome{}, nil
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

// TestPaneOperationsTimeoutRetainsClaimWithoutRetryOrStop leaves
// Process.TaskLocked unset: a caller not holding the task lock never recovers
// even a launching claim.
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
				return &launch.Presentation{Host: host, Report: func(ctx context.Context, _ string, _ herdr.AgentReport) (herdr.ReportOutcome, error) {
					reports++
					if phase == "report" {
						<-ctx.Done()
						return herdr.ReportOutcome{Exit: -1}, ctx.Err()
					}
					return herdr.ReportOutcome{}, nil
				}}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := p.Launch(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Launch = %v", err)
			}
			k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
			if _, err := os.Stat(filepath.Join(k.Dir(p.StateDir), launch.PaneClaimFile)); err != nil {
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

// staleClaimKey is the attempt every pane test launches.
var staleClaimKey = artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}

// waitSucceeded waits for p's attempt to record a succeeded outcome.
func waitSucceeded(t *testing.T, p *launch.Process) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		a := runningAttempt(t, p)
		if a.Outcome == store.AttemptSucceeded {
			return
		}
		if a.Outcome != "" || time.Now().After(end) {
			t.Fatalf("attempt did not succeed: %+v", a)
		}
	}
}

// claimRecoveredEvents counts the pane-claim-recovered lines in p's attempt events.
func claimRecoveredEvents(t *testing.T, p *launch.Process) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(staleClaimKey.Dir(p.StateDir), artisan.EventsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Count(string(b), `"event":"pane-claim-recovered"`)
}

func healthyPresentation(host adapter.TabHost, warnings *strings.Builder) *launch.Presentation {
	return &launch.Presentation{Host: host, Warnings: warnings, Report: func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error) {
		return herdr.ReportOutcome{}, nil
	}}
}

// claimRecoveredEvent returns p's attempt's one pane-claim-recovered event.
func claimRecoveredEvent(t *testing.T, p *launch.Process) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(staleClaimKey.Dir(p.StateDir), artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["event"] == "pane-claim-recovered" {
			return e
		}
	}
	t.Fatalf("no pane-claim-recovered event in %s", b)
	return nil
}

// recovery is what recoversClaim expects of the workspace the stale claim
// records: abandoned is its id, or empty when the claim records none, and
// leftOpen says the recovery refuses to close it.
type recovery struct {
	abandoned string
	leftOpen  bool
}

// recoversClaim launches p's attempt over its existing launching claim through
// a healthy presentation on panes and checks it runs once to success, closing
// the workspace the claim records unless want.leftOpen, and recording that.
func recoversClaim(t *testing.T, p *launch.Process, spec interp.Spec, panes *adaptertest.ShellPanes, want recovery) {
	t.Helper()
	var warnings strings.Builder
	p.Presentation = healthyPresentation(panes, &warnings)
	if _, err := p.Launch(context.Background(), spec); err != nil {
		t.Fatalf("task-locked launch over a stale launching claim: %v", err)
	}
	if len(panes.Sent()) != 1 {
		t.Fatalf("commands = %q, want exactly one", panes.Sent())
	}
	b, err := os.ReadFile(filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile))
	if err != nil {
		t.Fatal(err)
	}
	var tab adapter.TabRef
	if err := json.Unmarshal(b, &tab); err != nil || tab.PaneID == "" || tab.TabID == "" || tab.WorkspaceID == "" {
		t.Fatalf("claim %s does not hold the new tab binding: %v", b, err)
	}
	waitSucceeded(t, p)
	if n := claimRecoveredEvents(t, p); n != 1 {
		t.Fatalf("pane-claim-recovered events = %d, want 1", n)
	}
	// d91: the workspace the failed launch abandoned is closed, so only the
	// bound replacement is still listed, unless the close was refused.
	list, err := panes.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var labelled []string
	for _, w := range list {
		if w.Label == launch.WorkspaceLabel(staleClaimKey) {
			labelled = append(labelled, w.ID)
		}
	}
	wantOpen := []string{tab.WorkspaceID}
	if want.leftOpen {
		wantOpen = []string{want.abandoned, tab.WorkspaceID}
	}
	if !reflect.DeepEqual(labelled, wantOpen) {
		t.Fatalf("attempt workspaces %q, want %q", labelled, wantOpen)
	}
	e := claimRecoveredEvent(t, p)
	closed := want.abandoned != "" && !want.leftOpen
	if e["abandoned_workspace"] != want.abandoned || e["closed"] != closed || (e["reason"] != "") != want.leftOpen {
		t.Fatalf("pane-claim-recovered event %v, want %+v", e, want)
	}
	w := warnings.String()
	switch {
	case !strings.Contains(w, "stale launching claim"):
		t.Fatalf("warnings = %q", w)
	case want.abandoned == "" && !strings.Contains(w, "lost create response, so close it by hand"):
		t.Fatalf("legacy claim warnings = %q", w)
	case want.leftOpen && (!strings.Contains(w, want.abandoned) || !strings.Contains(w, e["reason"].(string)) || !strings.Contains(w, "close it by hand")):
		t.Fatalf("refused close warnings = %q", w)
	case closed && (!strings.Contains(w, "closed the Herdr workspace "+want.abandoned) || strings.Contains(w, "by hand")):
		t.Fatalf("closed warnings = %q", w)
	}
}

// TestTaskLockedLaunchRecoversLaunchingClaim: a pane launch that failed before
// recording its tab binding leaves a launching claim, and a caller holding the
// task lock launches the same attempt again over it, typing once (d48). The
// claim records the workspace and tab the failed launch created, and the
// recovery closes that workspace before creating its replacement (d91).
func TestTaskLockedLaunchRecoversLaunchingClaim(t *testing.T) {
	for _, phase := range []string{"workspace", "create", "shell"} {
		t.Run(phase, func(t *testing.T) {
			p := newProcess(t)
			p.TaskLocked = true
			spec := prepared(t, p, fixtureSpec(t, ""))
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			p.Presentation = &launch.Presentation{Host: &timeoutTabs{TabHost: panes, phase: phase}, Report: func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error) {
				return herdr.ReportOutcome{}, nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second) // timing:allow the host blocks until this deadline; expiry is the injected failure, never success.
			defer cancel()
			if _, err := p.Launch(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Launch = %v", err)
			}
			b, err := os.ReadFile(filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile))
			if err != nil {
				t.Fatal(err)
			}
			// ShellPanes' workspaces hold no starting tab, so only the
			// attempt's tab, once created, is recorded.
			want := `{"state":"launching","workspace":"director-2"}` + "\n"
			if phase != "workspace" {
				want = `{"state":"launching","workspace":"director-2","tabs":[{"Session":"lab","WorkspaceID":"director-2","TabID":"director-2:t1","PaneID":"director-2:p1"}]}` + "\n"
			}
			if string(b) != want {
				t.Fatalf("claim = %s, want %s", b, want)
			}
			rec := recovery{abandoned: "director-2"}
			if phase == "workspace" {
				// The workspace holds no tab, so no recorded tab to close it
				// through, and is left open. Real Herdr's WorkspaceCreate
				// always returns a starting tab, which the claim records, so a
				// real recovery closes through that tab; the HerdrTabs run of
				// adaptertest.AbandonedCloserContract covers that path.
				rec.leftOpen = true
			}
			recoversClaim(t, p, spec, panes, rec)
			if r, _ := claimRecoveredEvent(t, p)["reason"].(string); rec.leftOpen && !strings.Contains(r, "holds no recorded tab") {
				t.Fatalf("reason = %q", r)
			}
		})
	}
}

// abandonedClaim creates, on panes, the workspace and attempt tab a launch of
// p's attempt that died before its binding would have left, and writes the
// launching claim recording them. It returns the workspace id.
func abandonedClaim(t *testing.T, p *launch.Process, panes *adaptertest.ShellPanes) string {
	t.Helper()
	ctx := context.Background()
	w, err := panes.CreateWorkspace(ctx, launch.WorkspaceLabel(staleClaimKey), "")
	if err != nil {
		t.Fatal(err)
	}
	tab, err := panes.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: launch.TabLabel(staleClaimKey)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"state": "launching", "workspace": w.ID, "tabs": []adapter.TabRef{tab}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return w.ID
}

// TestTaskLockedLaunchClosesAbandonedWorkspaceAfterProcessDeath: a later
// process, owning none of the dead launch's tabs, adopts and closes the
// workspace its launching claim records (d91).
func TestTaskLockedLaunchClosesAbandonedWorkspaceAfterProcessDeath(t *testing.T) {
	p := newProcess(t)
	p.TaskLocked = true
	spec := prepared(t, p, fixtureSpec(t, ""))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	ws := abandonedClaim(t, p, panes)
	recoversClaim(t, p, spec, panes.Reopen(), recovery{abandoned: ws})
}

// TestTaskLockedLaunchLeavesAbandonedWorkspaceWithForeignTabOpen: a tab the
// claim does not record makes the recovery refuse the close; the replacement
// still launches, and the event and warning say why the workspace stayed open.
func TestTaskLockedLaunchLeavesAbandonedWorkspaceWithForeignTabOpen(t *testing.T) {
	p := newProcess(t)
	p.TaskLocked = true
	spec := prepared(t, p, fixtureSpec(t, ""))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	ws := abandonedClaim(t, p, panes)
	if _, err := panes.Reopen().CreateTab(context.Background(), herdr.TabSpec{WorkspaceID: ws, Label: launch.TabLabel(staleClaimKey)}); err != nil {
		t.Fatal(err)
	}
	recoversClaim(t, p, spec, panes.Reopen(), recovery{abandoned: ws, leftOpen: true})
	if r := claimRecoveredEvent(t, p)["reason"].(string); !strings.Contains(r, "did not record") {
		t.Fatalf("reason = %q", r)
	}
}

// TestTaskLockedLaunchStillRefusesBoundClaim: a claim holding a tab binding may
// have had its command typed, so even a task-locked caller never relaunches it.
func TestTaskLockedLaunchStillRefusesBoundClaim(t *testing.T) {
	for _, phase := range []string{"report", "type"} {
		t.Run(phase, func(t *testing.T) {
			p := newProcess(t)
			p.TaskLocked = true
			spec := prepared(t, p, fixtureSpec(t, "guildhall-fake: delay=400ms"))
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			host := &timeoutTabs{TabHost: panes, phase: phase}
			p.Presentation = &launch.Presentation{Host: host, Report: func(ctx context.Context, _ string, _ herdr.AgentReport) (herdr.ReportOutcome, error) {
				if phase == "report" {
					<-ctx.Done()
					return herdr.ReportOutcome{Exit: -1}, ctx.Err()
				}
				return herdr.ReportOutcome{}, nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // timing:allow the host blocks until this deadline; expiry is the injected failure, never success.
			defer cancel()
			if _, err := p.Launch(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Launch = %v", err)
			}
			sent := len(panes.Sent())
			if _, err := p.Launch(context.Background(), spec); err == nil {
				t.Fatal("bound claim relaunched")
			}
			if len(panes.Sent()) != sent || host.workspaces != 1 || claimRecoveredEvents(t, p) != 0 {
				t.Fatalf("sent=%q workspaces=%d", panes.Sent(), host.workspaces)
			}
			if phase == "type" {
				waitSucceeded(t, p)
			}
		})
	}
}

// launchingClaim is the claim a pane launch writes before its tab binding.
const launchingClaim = "{\"state\":\"launching\"}\n"

// TestTaskLockedLaunchRecoversOnlyAnUnownedLaunchingClaim: a hand-written
// legacy launching claim, as guildhall/d14 plan-1 left, launches under the task
// lock; any other claim, or any claim without the lock, is refused untouched.
func TestTaskLockedLaunchRecoversOnlyAnUnownedLaunchingClaim(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		p := newProcess(t)
		p.TaskLocked = true
		spec := prepared(t, p, fixtureSpec(t, ""))
		if err := os.WriteFile(filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile), []byte(launchingClaim), 0o600); err != nil {
			t.Fatal(err)
		}
		recoversClaim(t, p, spec, adaptertest.NewShellPanes(t, "lab", "director"), recovery{})
	})
	for name, c := range map[string]struct {
		claim  string
		locked bool
	}{
		"unlocked":                 {launchingClaim, false},
		"unparseable":              {"{\"state\":\"launch", true},
		"empty":                    {"", true},
		"other state":              {"{\"state\":\"typed\"}\n", true},
		"binding":                  {"{\"state\":\"launching\",\"PaneID\":\"p1\"}\n", true},
		"unknown field":            {"{\"state\":\"launching\",\"workspace\":\"w1\",\"extra\":1}\n", true},
		"tab in another workspace": {"{\"state\":\"launching\",\"workspace\":\"w1\",\"tabs\":[{\"Session\":\"lab\",\"WorkspaceID\":\"w2\",\"TabID\":\"t1\",\"PaneID\":\"p1\"}]}\n", true},
		"tabs without workspace":   {"{\"state\":\"launching\",\"tabs\":[{\"Session\":\"lab\",\"WorkspaceID\":\"\",\"TabID\":\"t1\",\"PaneID\":\"p1\"}]}\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProcess(t)
			p.TaskLocked = c.locked
			spec := prepared(t, p, fixtureSpec(t, ""))
			path := filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile)
			if err := os.WriteFile(path, []byte(c.claim), 0o600); err != nil {
				t.Fatal(err)
			}
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			p.Presentation = healthyPresentation(panes, &strings.Builder{})
			if _, err := p.Launch(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "already claimed") {
				t.Fatalf("Launch = %v", err)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != c.claim {
				t.Fatalf("claim changed to %q, %v", b, err)
			}
			if workspaces, err := panes.Workspaces(context.Background()); err != nil || len(workspaces) != 1 || len(panes.Sent()) != 0 {
				t.Fatalf("workspaces %+v (%v) sent %q", workspaces, err, panes.Sent())
			}
		})
	}
	t.Run("owned", func(t *testing.T) {
		p := newProcess(t)
		p.TaskLocked = true
		spec := prepared(t, p, fixtureSpec(t, ""))
		panes := adaptertest.NewShellPanes(t, "lab", "director")
		p.Presentation = healthyPresentation(panes, &strings.Builder{})
		if _, err := p.Launch(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		waitSucceeded(t, p)
		path := filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile)
		if err := os.WriteFile(path, []byte(launchingClaim), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Launch(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "already owned") {
			t.Fatalf("Launch = %v", err)
		}
		if len(panes.Sent()) != 1 || claimRecoveredEvents(t, p) != 0 {
			t.Fatalf("sent %q", panes.Sent())
		}
	})
}
