//go:build unix

package launch_test

import (
	"context"
	"errors"
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
)

func TestPaneProcessPassesLauncherContract(t *testing.T) {
	interptest.RunLauncherContract(t, func(t *testing.T) interptest.Harness {
		p := newProcess(t)
		panes := adaptertest.NewShellPanes(t, "lab", "director")
		p.Presentation = &launch.Presentation{Host: panes, Workspace: "director", Report: func(context.Context, string, herdr.AgentReport) error { return nil }}
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
	p.Presentation = &launch.Presentation{Host: panes, Workspace: "director", Report: func(_ context.Context, id string, r herdr.AgentReport) error {
		pane = id
		at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}}
		if r.Source != "guildhall" || r.Agent != "artisan" || !reflect.DeepEqual(r.Resume, launch.ResumeArgv(at)) {
			t.Errorf("report = %+v", r)
		}
		return nil
	}}
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
	ref := adapter.TabRef{Session: "lab", WorkspaceID: "director", TabID: "director:t1", PaneID: pane}
	before := runningAttempt(t, p)
	if err := panes.InterruptTab(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
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
	p.Presentation = &launch.Presentation{Host: panes, Workspace: "director", Report: func(context.Context, string, herdr.AgentReport) error { return refused }}
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
	p.Presentation = &launch.Presentation{Host: uncertainTabs{panes}, Workspace: "director", Report: func(context.Context, string, herdr.AgentReport) error { return nil }}
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
