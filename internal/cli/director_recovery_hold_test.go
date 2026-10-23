package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestDirectorStoppedRuntimeRecoveryHoldsAdmission(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, true, store.DirectorRunning)
	d.recordUnlaunched(t, "t-2")
	inv := t.TempDir()
	u, err := startRuntime(artisantest.Command(inv, artisantest.Worker{Grandchildren: 1}), 1, d.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	tree := runtimeTree(t, inv)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		current, err := s.LatestDirectorSession()
		if err != nil {
			return store.Receipt{}, err
		}
		return s.EndDirectorSession(1, store.EndSuperseded, current.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := latestSession(t, d.store)
	launcher := liveLauncher(t)
	t.Cleanup(func() { _ = artisan.Kill(launcher) })
	visited := false
	duringSweep(t, func() {
		visited = true
		waitGone(t, "before the attempt sweep", tree...)
		current, _ := latestSession(t, d.store)
		if !current.Sweeping || current.State != store.DirectorStopped || current.EndedAt != before.EndedAt || current.EndReason != before.EndReason {
			t.Errorf("recovery hold changed end metadata or is absent: %+v", current)
		}
		if _, err := withStore(d.store, true, checkSupersede); !errors.Is(err, errStopSweeping) {
			t.Errorf("replacement during recovery = %v; want stop sweeping", err)
		}
		if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"paused","step":"plan"}`+"\n" {
			t.Errorf("start during recovery: %d, %s, %s", code, out, stderr)
		}
		_, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
			return s.TakeOwnership("recovery-owner", store.Ownership{Repository: "app", TaskID: "t-2", Step: "plan", Attempt: 1, LauncherIdentity: store.ProcessIdentity(launcher)}, func(store.Ownership) (store.Liveness, error) { return store.ProbeGone, nil })
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	if !visited || code != 1 || !strings.Contains(stderr, "run guildhall director stop again") {
		t.Fatalf("failed recovery: visited %v, exit %d, %s, %s", visited, code, out, stderr)
	}
	if hold, err := withStore(d.store, true, held); err != nil || !hold {
		t.Fatalf("failed recovery hold = %v, %v", hold, err)
	}
	if _, err := withStore(d.store, true, checkSupersede); !errors.Is(err, errStopSweeping) {
		t.Fatalf("replacement after failure = %v", err)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt("recovery-owner-finish", "app", "t-2", "plan", 1, store.AttemptStopped)
	}); err != nil {
		t.Fatal(err)
	}
	if err := artisantest.KillVerified(launcher); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state); code != 0 {
		t.Fatalf("retry recovery: %d, %s, %s", code, out, stderr)
	}
	if hold, err := withStore(d.store, true, held); err != nil || hold {
		t.Fatalf("completed recovery hold = %v, %v", hold, err)
	}
	if _, err := withStore(d.store, true, checkSupersede); err != nil {
		t.Fatalf("replacement after recovery: %v", err)
	}
}
