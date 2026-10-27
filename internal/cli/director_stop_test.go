package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// stopDirector runs director stop over d's store and state and checks it reports the session stopping and every
// attempt stopped.
func (d runDirector) stopDirector(t *testing.T) {
	t.Helper()
	code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.State != store.DirectorStopping || stderr != "" {
		t.Fatalf("director stop: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
}

// running reads how many attempts the caps count as running.
func (d runDirector) running(t *testing.T) int {
	t.Helper()
	u, err := withStore(d.store, true, func(s *store.Store) (store.AttemptUsage, error) { return s.AttemptUsage() })
	if err != nil {
		t.Fatal(err)
	}
	return u.Running
}

// TestDirectorStopStopsADirectorStage: director stop during the Director's own grill records the grill stopped,
// releasing its running capacity, lists it in needs_me as retryable, and a later step finish of it is refused.
func TestDirectorStopStopsADirectorStage(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.stopDirector(t)
	if got := d.attempts(t); len(got) != 1 || got[0].Step != "grill" || got[0].Outcome != store.AttemptStopped {
		t.Fatalf("attempts after director stop = %+v; want grill-1 stopped", got)
	}
	if n := d.running(t); n != 0 {
		t.Errorf("%d attempts still count as running", n)
	}
	st, err := readDirectorStatus(context.Background(), d.store, d.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry := false
	for _, it := range st.NeedsMe {
		retry = retry || it.Kind == "retry" && it.Retryable && it.Step == "grill" && it.Attempt == 1
	}
	if !retry {
		t.Errorf("needs_me = %+v; want grill-1 retryable", st.NeedsMe)
	}
	file, _ := grillNotesFile(t)
	if code, out, _ := d.finish(t, "grill", "1", "--summary", "agreed", "--output", "notes="+file); code == 0 {
		t.Errorf("step finish of the stopped grill succeeded: %s", out)
	}
	if _, err := os.Stat(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "grill", Attempt: 1}.Dir(d.state), artisan.ResultFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused finish published a result: %v", err)
	}
}

// TestDirectorStopLeavesAPublishedStageResult: a Director stage whose result step finish already published has
// finished its work, so director stop leaves it for its advance to take, as a stop after a result is no stop.
func TestDirectorStopLeavesAPublishedStageResult(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, stderr %s", code, stderr)
	}
	file, _ := grillNotesFile(t)
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "grill", Attempt: 1}
	if err := finishStage(advanceFlags{store: d.store, config: d.config, state: d.state, repo: "app"}, k, "agreed", stageOutputs{{"notes", file}}, false); err != nil {
		t.Fatal(err)
	}
	d.stopDirector(t)
	if got := d.attempts(t); len(got) != 1 || got[0].Outcome != "" {
		t.Fatalf("attempts after director stop = %+v; want grill-1 left for its advance", got)
	}
}

// TestDirectorStopStopsAnUnlaunchedAttempt: an Artisan attempt an advance recorded and never launched, as when it
// ended between the two, has no owner to ask; director stop records it stopped, so no later advance launches it.
func TestDirectorStopStopsAnUnlaunchedAttempt(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("recorded-not-launched", "app", "t-1", "plan")
	}); err != nil {
		t.Fatal(err)
	}
	d.stopDirector(t)
	if got := d.attempts(t); len(got) != 1 || got[0].Outcome != store.AttemptStopped {
		t.Fatalf("attempts after director stop = %+v; want plan-1 stopped", got)
	}
	// With the session ended, an advance of the task takes the stop and launches nothing.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		ds, err := s.DirectorSession(1)
		if err != nil {
			return store.Receipt{}, err
		}
		return s.SetDirectorState(1, store.DirectorStopped, ds.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"director","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start after the stop: exit %d, %q, stderr %s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "launched.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stopped attempt was launched: %v", err)
	}
}

// TestDirectorStopWaitsForAnInFlightAdvance: while an advance holds the task's lock, having recorded an attempt it
// has not yet launched, director stop waits for it rather than deciding the attempt has no owner; once the advance
// has launched it and released the lock, stop asks the owner, here a live one with no endpoint, which it cannot
// prove stopped, and says so.
func TestDirectorStopWaitsForAnInFlightAdvance(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning)
	taskDir := filepath.Join(d.state, "app", "t-1")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := artisan.LockAttempt(context.Background(), taskDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("in-flight", "app", "t-1", "plan")
	}); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{})
	testHookStopWaitsForTask = func() { close(waiting) }
	t.Cleanup(func() { testHookStopWaitsForTask = nil })
	type result struct {
		code        int
		out, stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
		done <- result{code, out, stderr}
	}()
	select {
	case <-waiting:
	case r := <-done:
		release()
		t.Fatalf("director stop ended without waiting for the task: exit %d, %s %s; attempts %+v", r.code, r.out, r.stderr, d.attempts(t))
	case <-time.After(2 * time.Minute):
		t.Fatal("director stop never reached the task lock")
	}
	launcher := liveLauncher(t)
	own := store.Ownership{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1, LauncherIdentity: store.ProcessIdentity(launcher)}
	_, err = withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.TakeOwnership("launched", own, func(store.Ownership) (store.Liveness, error) { return store.ProbeGone, nil })
	})
	release()
	if err != nil {
		t.Fatalf("the in-flight advance could not take its attempt: %v", err)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("director stop did not end once the task lock was released")
	}
	if r.code == 0 {
		t.Errorf("director stop reported success over a live owner it never reached: %s", r.out)
	}
	if got := d.attempts(t); len(got) != 1 || got[0].Outcome != "" {
		t.Errorf("attempts = %+v; want plan-1 left to its owner", got)
	}
	// The stand-in owner has no endpoint the shared cleanup can reach: it finishes its attempt and exits.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt("owner-finish", "app", "t-1", "plan", 1, store.AttemptStopped)
	}); err != nil {
		t.Fatal(err)
	}
	if err := artisantest.KillVerified(launcher); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "before cleanup of the stand-in owner", launcher)
}

// duringSweep runs fn once, as director stop's sweep reaches an attempt nothing owns.
func duringSweep(t *testing.T, fn func()) {
	t.Helper()
	var once sync.Once
	testHookStopWaitsForTask = func() { once.Do(fn) }
	t.Cleanup(func() { testHookStopWaitsForTask = nil })
}

// recordUnlaunched records attempt plan-1 of task id as an advance that never launched it would, for director
// stop's sweep to stop with no owner.
func (d runDirector) recordUnlaunched(t *testing.T, id string) {
	t.Helper()
	newTask(t, d.store, d.config, "app", "Another task", id)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("recorded-not-launched", "app", id, "plan")
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDirectorStopOfAGoneSupervisorHoldsUntilItsSweepEnds: with the supervisor gone, director stop records the
// session stopping before its sweep, so a task start during the sweep is held and records nothing; once the sweep
// is done the session is stopped as superseded and a task start launches again with no Director running.
func TestDirectorStopOfAGoneSupervisorHoldsUntilItsSweepEnds(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, true, store.DirectorRunning)
	d.recordUnlaunched(t, "t-2")
	duringSweep(t, func() {
		if got, _ := latestSession(t, d.store); got.State != store.DirectorStopping || !got.Sweeping {
			t.Errorf("session during the sweep = %+v; want stopping and sweeping", got)
		}
		if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"paused","step":"plan"}`+"\n" {
			t.Errorf("task start during the sweep: exit %d, %q, stderr %s", code, out, stderr)
		}
	})
	code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.State != store.DirectorStopped ||
		printed.EndReason != store.EndSuperseded || printed.Sweeping {
		t.Fatalf("director stop: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("attempts of t-1 after the held start = %+v", got)
	}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start after the sweep: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
}

// TestDirectorStopNowHoldsUntilItsSweepEnds: stop --now lets the supervisor end the runtime and record the session
// stopped while the stop is still sweeping; an advance starting a new attempt then is held and records nothing, and
// the hold lifts once the sweep is done, after which a second director stop is refused as stopped.
func TestDirectorStopNowHoldsUntilItsSweepEnds(t *testing.T) {
	dir, d := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	d.recordUnlaunched(t, "t-2")
	duringSweep(t, func() {
		for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(20 * time.Millisecond) {
			if got := sessions(t, dir); len(got) == 1 && got[0].State == store.DirectorStopped {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("the supervisor never recorded the session stopped: %+v", sessions(t, dir))
			}
		}
		if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"paused","step":"plan"}`+"\n" {
			t.Errorf("task start after the runtime ended, during the sweep: exit %d, %q, stderr %s", code, out, stderr)
		}
		startRefused(t, dir)
	})
	code, out, stderr := d.run(t, "director", "stop", "--now", "--copy", dir)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.State != store.DirectorStopped || printed.Sweeping {
		t.Fatalf("director stop --now: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("attempts of t-1 after the held start = %+v", got)
	}
	if hold, err := withStore(d.store, true, held); err != nil || hold {
		t.Errorf("held after the sweep = %v, %v; want the hold lifted", hold, err)
	}
	if code, _, stderr := d.run(t, "director", "stop", "--copy", dir); code != 1 || !strings.Contains(stderr, "is stopped") {
		t.Errorf("director stop of a swept stopped session: exit %d, stderr %s; want it refused", code, stderr)
	}
	d.restart(t, dir)
}

// restart checks director start is admitted over dir's stopped session, as session 2, and stops it again.
func (d runDirector) restart(t *testing.T, dir string) {
	t.Helper()
	done := supervise(t, dir, time.Hour)
	if code, out, stderr := d.run(t, "director", "stop", "--now", "--copy", dir); code != 0 {
		t.Fatalf("director stop --now of session 2: exit %d, %s %s", code, out, stderr)
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start of session 2 exited %d, want 0", code)
	}
	if got := sessions(t, dir); len(got) != 2 || got[1].State != store.DirectorStopped {
		t.Errorf("sessions = %+v; want session 2 started and stopped", got)
	}
}

// TestDirectorStartWaitsForAnInterruptedSweep: a session recorded stopped while its stop's sweep had not finished
// refuses director start, recording nothing; director stop again finishes the sweep and prints that session, and
// director start is then admitted.
func TestDirectorStartWaitsForAnInterruptedSweep(t *testing.T) {
	dir, d := supervisedCopy(t)
	recordSession(t, d.store, false, store.DirectorRunning, store.DirectorStopping, store.DirectorStopped)
	d.recordUnlaunched(t, "t-2")
	startRefused(t, dir)
	code, out, stderr := d.run(t, "director", "stop", "--copy", dir)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.Number != 1 || printed.State != store.DirectorStopped || printed.Sweeping {
		t.Fatalf("director stop: exit %d, %q (%v), stderr %s; want session 1 stopped and swept", code, out, err, stderr)
	}
	d.restart(t, dir)
}

// TestDirectorStartRefusesAStoppingGoneSession: a session left stopping by a supervisor that is gone, its sweep
// done, still needs director stop to record its end, so a start is refused until then.
func TestDirectorStartRefusesAStoppingGoneSession(t *testing.T) {
	path := emptyStore(t)
	recordSession(t, path, true, store.DirectorRunning, store.DirectorStopping)
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		ds, err := s.DirectorSession(1)
		if err != nil {
			return store.Receipt{}, err
		}
		return s.FinishDirectorStop(1, ds.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := withStore(path, true, checkSupersede); !errors.Is(err, errStopSweeping) {
		t.Fatalf("start over a stopping session whose supervisor is gone = %v, want errStopSweeping", err)
	}
	if code, out, stderr := runCLI(t, "director", "stop", "--store", path, "--state", t.TempDir()); code != 0 {
		t.Fatalf("director stop: exit %d, %s %s", code, out, stderr)
	}
	if sp, err := withStore(path, true, checkSupersede); err != nil || sp.Number != 1 {
		t.Errorf("start after director stop = %d, %v; want session 1 superseded", sp.Number, err)
	}
}

// TestDirectorStopFinishesAnInterruptedSweep: a session recorded stopped while its stop's sweep had not finished
// still holds new attempts; director stop again sweeps and lifts the hold rather than refusing it as stopped.
func TestDirectorStopFinishesAnInterruptedSweep(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning, store.DirectorStopping, store.DirectorStopped)
	d.recordUnlaunched(t, "t-2")
	if hold, err := withStore(d.store, true, held); err != nil || !hold {
		t.Fatalf("held before the sweep = %v, %v; want held", hold, err)
	}
	code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.State != store.DirectorStopped || printed.Sweeping {
		t.Fatalf("director stop: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	if hold, err := withStore(d.store, true, held); err != nil || hold {
		t.Errorf("held after the sweep = %v, %v; want the hold lifted", hold, err)
	}
}

// liveLauncher starts a process that lives for a minute to stand in for a launcher, and returns its identity once
// it has recorded it; the test's cleanup kills it.
func liveLauncher(t *testing.T) artisan.Identity {
	t.Helper()
	inv := t.TempDir()
	cmd := artisantest.Command(inv, artisantest.Worker{Lifetime: time.Minute})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for end := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
		if entries, err := artisantest.Inventory(inv); err == nil && len(entries) == 1 {
			return entries[0].Identity
		}
		if time.Now().After(end) {
			t.Fatal("the stand-in launcher never recorded itself")
		}
	}
}
