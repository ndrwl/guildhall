//go:build unix

package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// swallowTabs accepts the typed line but never runs it: a launcher that never starts, so never owns.
type swallowTabs struct{ adapter.TabHost }

func (swallowTabs) RunInTab(context.Context, adapter.TabRef, string) error { return nil }

// lockingTabs takes the Director store's lock just before typing and holds it, for hold or, with a zero hold,
// until the launcher's log shows it gave up on the lock: the d216 incident's shape (d221).
type lockingTabs struct {
	adapter.TabHost
	t        *testing.T
	store    string
	log      string
	hold     time.Duration
	released chan struct{}
}

func (h lockingTabs) RunInTab(ctx context.Context, ref adapter.TabRef, line string) error {
	s, err := store.Open(h.store, store.Options{})
	if err != nil {
		h.t.Errorf("hold the store: %v", err)
		close(h.released)
		return h.TabHost.RunInTab(ctx, ref, line)
	}
	go func() {
		defer close(h.released)
		defer func() { _ = s.Close() }()
		if h.hold > 0 {
			time.Sleep(h.hold) // timing:allow the store is held for a fixed stretch shorter than the lock wait; the launch must outwait it.
			return
		}
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if b, _ := os.ReadFile(h.log); strings.Contains(string(b), "store is locked") {
				return
			}
		}
		h.t.Error("the launcher never gave up on the held store lock")
	}()
	return h.TabHost.RunInTab(ctx, ref, line)
}

// owners counts the attempt's ownership records: one once a launcher owns it.
func owners(t *testing.T, p *launch.Process) int {
	t.Helper()
	at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: staleClaimKey}
	if c, err := launch.Status(context.Background(), at); err != nil {
		t.Fatal(err)
	} else if c.State == reconcile.Absent {
		return 0
	}
	return 1
}

// TestTimedOutPaneClaimDoesNotBlockNextStart is d221's regression test, the d216 incident: the Director store is
// held past the launcher's lock wait, so the typed launcher exits before owning the attempt. The launch fails
// promptly, naming the real classification, and the next task-locked start closes the abandoned workspace, sets
// the claims aside and launches the attempt once, with no hand-deleted file.
func TestTimedOutPaneClaimDoesNotBlockNextStart(t *testing.T) {
	p := newProcess(t)
	p.TaskLocked = true
	p.LockWait = time.Second // timing:allow the store is held until the launcher gives up; expiry is the injected failure.
	spec := prepared(t, p, fixtureSpec(t, ""))
	dir := staleClaimKey.Dir(p.StateDir)
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	released := make(chan struct{})
	p.Presentation = healthyPresentation(lockingTabs{TabHost: panes, t: t, store: p.Store, log: filepath.Join(dir, launch.LogFile), released: released}, &strings.Builder{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	began := time.Now()
	_, err := p.Launch(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "the launcher exited before owning") {
		t.Fatalf("first Launch = %v, want the launcher's early exit", err)
	}
	if took := time.Since(began); took > 15*time.Second {
		t.Fatalf("first Launch took %s: it waited out its deadline instead of seeing the launcher exit", took)
	}
	if strings.Contains(err.Error(), "attempt lock") || !strings.Contains(err.Error(), "State:absent") {
		t.Fatalf("first Launch = %v, want the real absent classification", err)
	}
	<-released
	if n := owners(t, p); n != 0 {
		t.Fatalf("owners after the failed launch = %d", n)
	}
	var warnings strings.Builder
	p.Presentation = healthyPresentation(panes, &warnings)
	if _, err := p.Launch(context.Background(), spec); err != nil {
		t.Fatalf("second Launch = %v, want it to recover the bound claim and launch", err)
	}
	if sent := panes.Sent(); len(sent) != 2 {
		t.Fatalf("commands = %q, want one per claim", sent)
	}
	waitSucceeded(t, p)
	for _, name := range []string{"pane-launch.abandoned-1.json", "pane-launcher.abandoned-1.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("abandoned claim evidence %s: %v", name, err)
		}
	}
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
	if len(labelled) != 1 || labelled[0] == "director-2" {
		t.Fatalf("attempt workspaces = %q, want only the replacement", labelled)
	}
	if n := claimRecoveredEvents(t, p); n != 1 || !strings.Contains(warnings.String(), "launcher was proven gone") {
		t.Fatalf("recovery events = %d, warnings %q", n, warnings.String())
	}
}

// TestPaneLaunchOutwaitsAStoreLockHeldWithinLockWait: a store busy for less than the copy's lock wait delays the
// launcher's ownership, as it delays every other store caller, and never fails or retries the launch.
func TestPaneLaunchOutwaitsAStoreLockHeldWithinLockWait(t *testing.T) {
	p := newProcess(t)
	p.TaskLocked = true
	p.LockWait = 10 * time.Second
	spec := prepared(t, p, fixtureSpec(t, ""))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	released := make(chan struct{})
	p.Presentation = healthyPresentation(lockingTabs{TabHost: panes, t: t, store: p.Store, hold: 4 * time.Second, released: released}, &strings.Builder{})
	if _, err := p.Launch(context.Background(), spec); err != nil {
		t.Fatalf("Launch = %v", err)
	}
	<-released
	waitSucceeded(t, p)
	if len(panes.Sent()) != 1 || claimRecoveredEvents(t, p) != 0 {
		t.Fatalf("commands = %q", panes.Sent())
	}
}

// TestPaneClaimWithUnrecordedLauncherRefusesWithARoute: a typed line that never ran records no launcher, so
// nothing proves no launcher will appear; the next start refuses, naming the real classification and the command
// that recovers the attempt.
func TestPaneClaimWithUnrecordedLauncherRefusesWithARoute(t *testing.T) {
	p := newProcess(t)
	p.TaskLocked = true
	spec := prepared(t, p, fixtureSpec(t, ""))
	dir := staleClaimKey.Dir(p.StateDir)
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	p.Presentation = healthyPresentation(swallowTabs{panes}, &strings.Builder{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // timing:allow nothing ever owns; expiry is the injected failure.
	defer cancel()
	if _, err := p.Launch(ctx, spec); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "attempt lock") {
		t.Fatalf("first Launch = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, launch.PaneLauncherFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s after a line that never ran: %v", launch.PaneLauncherFile, err)
	}
	claim, err := os.ReadFile(filepath.Join(dir, launch.PaneClaimFile))
	if err != nil {
		t.Fatal(err)
	}
	p.Presentation = healthyPresentation(panes, &strings.Builder{})
	_, err = p.Launch(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "guildhall artisan resume") || !strings.Contains(err.Error(), "--abandon-pane-claim") ||
		!strings.Contains(err.Error(), "State:absent") || !strings.Contains(err.Error(), "never recorded") {
		t.Fatalf("second Launch = %v, want a refusal naming artisan resume --abandon-pane-claim", err)
	}
	if after, err := os.ReadFile(filepath.Join(dir, launch.PaneClaimFile)); err != nil || string(after) != string(claim) {
		t.Fatalf("claim changed to %s, %v", after, err)
	}
	if len(panes.Sent()) != 0 {
		t.Fatalf("commands = %q", panes.Sent())
	}
}

// TestAbandonUnrecordedPaneClaim: the operator's AbandonPaneClaimFlag sets aside a claim whose launcher was never
// recorded only once the terminal confirms its pane gone, and the next task-locked start then launches once.
func TestAbandonUnrecordedPaneClaim(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "pane gone", true: "pane still listed"}[foreign], func(t *testing.T) {
			p := newProcess(t)
			p.TaskLocked = true
			spec := prepared(t, p, fixtureSpec(t, ""))
			at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: staleClaimKey}
			panes := adaptertest.NewShellPanes(t, "lab", "director")
			p.Presentation = healthyPresentation(swallowTabs{panes}, &strings.Builder{})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // timing:allow nothing ever owns; expiry is the injected failure.
			defer cancel()
			if _, err := p.Launch(ctx, spec); err == nil {
				t.Fatal("first Launch succeeded")
			}
			if foreign {
				// A tab the claim does not record makes the close refuse, so the pane is not confirmed gone.
				if _, err := panes.Reopen().CreateTab(context.Background(), herdr.TabSpec{WorkspaceID: "director-2", Label: launch.TabLabel(staleClaimKey)}); err != nil {
					t.Fatal(err)
				}
			}
			pr := healthyPresentation(panes.Reopen(), &strings.Builder{})
			if _, err := launch.AbandonPaneClaim(context.Background(), at, pr, false); err == nil || !strings.Contains(err.Error(), launch.AbandonPaneClaimFlag) {
				t.Fatalf("AbandonPaneClaim without the flag = %v", err)
			}
			_, err := launch.AbandonPaneClaim(context.Background(), at, pr, true)
			claim := filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneClaimFile)
			if foreign {
				if err == nil || !strings.Contains(err.Error(), "not confirmed gone") {
					t.Fatalf("AbandonPaneClaim with the pane still listed = %v", err)
				}
				if _, err := os.Stat(claim); err != nil {
					t.Fatalf("claim set aside: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AbandonPaneClaim = %v", err)
			}
			if _, err := os.Stat(claim); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim still held: %v", err)
			}
			p.Presentation = healthyPresentation(panes, &strings.Builder{})
			if _, err := p.Launch(context.Background(), spec); err != nil {
				t.Fatalf("Launch after abandoning = %v", err)
			}
			waitSucceeded(t, p)
			if len(panes.Sent()) != 1 {
				t.Fatalf("commands = %q", panes.Sent())
			}
		})
	}
}

// TestPaneLauncherRecord: the bridge records the launcher it started once, as soon as it starts it, and a
// launch that owns its attempt leaves that record beside its claim.
func TestPaneLauncherRecord(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, ""))
	panes := adaptertest.NewShellPanes(t, "lab", "director")
	p.Presentation = healthyPresentation(panes, &strings.Builder{})
	if _, err := p.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	waitSucceeded(t, p)
	b, err := os.ReadFile(filepath.Join(staleClaimKey.Dir(p.StateDir), launch.PaneLauncherFile))
	if err != nil {
		t.Fatal(err)
	}
	var id struct {
		PID   int    `json:"pid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &id); err != nil || id.PID <= 0 || id.Token == "" {
		t.Fatalf("%s = %s, %v", launch.PaneLauncherFile, b, err)
	}
}
