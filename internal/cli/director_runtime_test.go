package cli

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestSessionEndEmptiesTheRuntimesUnit runs a real runtime that starts a child and either waits until a stop ends it
// or exits at once, leaving the child running, as a runtime that does not clean up its tools does. Either way the
// supervisor empties the runtime's unit and every process it started finishes terminating (docs/director.md#stop).
func TestSessionEndEmptiesTheRuntimesUnit(t *testing.T) {
	for _, c := range []struct {
		name string
		w    artisantest.Worker
		stop bool
	}{
		{"stopped while its child runs", artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true}, true},
		{"exits before its child", artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true, ExitAfterSpawn: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := emptyStore(t)
			states := []store.DirectorState{store.DirectorRunning}
			if c.stop {
				states = append(states, store.DirectorStopping)
			}
			recordSession(t, path, false, states...)
			inv := t.TempDir()
			cmd := artisantest.Command(inv, c.w)
			u, err := startRuntime(cmd, 1, path)
			if err != nil {
				t.Fatal(err)
			}
			var tree []artisan.Identity
			t.Cleanup(func() {
				for _, id := range tree {
					_ = artisan.Kill(id)
				}
			})
			for end := time.Now().Add(2 * time.Minute); len(tree) < 2; time.Sleep(10 * time.Millisecond) {
				entries, err := artisantest.Inventory(inv)
				if err != nil {
					t.Fatal(err)
				}
				tree = tree[:0]
				for _, e := range entries {
					tree = append(tree, e.Identity)
				}
				if time.Now().After(end) {
					t.Fatalf("the runtime's tree never started: %+v", entries)
				}
			}
			app, _, _ := newTestApp(nil, nil)
			app.SessionPoll, app.StopGrace = 10*time.Millisecond, time.Nanosecond
			done := make(chan error, 1)
			var ending endAction
			go func() {
				var err error
				w := sessionWatch{storePath: path, n: 1, grace: time.Nanosecond}
				_, ending, err = app.waitSession(u, cmd, make(chan os.Signal), &w)
				done <- err
			}()
			select {
			case err := <-done:
				t.Logf("waitSession: ending %v, %v", ending, err)
			case <-time.After(2 * time.Minute):
				t.Fatal("the session's wait never returned")
			}
			want := keepRunning
			if c.stop {
				want = endForStop
			}
			if ending != want {
				t.Errorf("ending = %v, want %v", ending, want)
			}
			waitGone(t, "after the session ended", tree...)
		})
	}
}

// Separate stores can both have session 1 in the same supervisor process. Their
// recorded units must remain independently startable and stoppable.
func TestRuntimeUnitsAreIsolatedAcrossStores(t *testing.T) {
	var units []runtimeUnit
	var trees [][]artisan.Identity
	var records []store.DirectorSession
	for range 2 {
		path := emptyStore(t)
		recordSession(t, path, false, store.DirectorRunning)
		inv := t.TempDir()
		u, err := startRuntime(artisantest.Command(inv, artisantest.Worker{Grandchildren: 1}), 1, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = u.Close() })
		units = append(units, u)
		trees = append(trees, runtimeTree(t, inv))
		rec, _ := latestSession(t, path)
		records = append(records, rec)
	}
	if records[0].Runtime.Name != "" && records[0].Runtime.Name == records[1].Runtime.Name {
		t.Fatalf("separate stores recorded the same runtime unit: %+v", records)
	}
	if err := units[0].Kill(); err != nil {
		t.Fatal(err)
	}
	if err := empty(units[0], 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "after ending only the first runtime", trees[0]...)
	for _, id := range trees[1] {
		if lv, err := artisan.Probe(id); err != nil || lv != artisan.Alive {
			t.Errorf("second runtime process %d = %v, %v; want alive", id.PID, lv, err)
		}
	}
}

// survivingUnit is a runtime unit whose runtime has exited and whose one other member never ends.
type survivingUnit struct{ exited chan struct{} }

func (u survivingUnit) Members() ([]artisan.Identity, error) {
	return []artisan.Identity{{PID: 42}}, nil
}
func (u survivingUnit) Terminate() error        { return nil }
func (u survivingUnit) Kill() error             { return nil }
func (u survivingUnit) Exited() <-chan struct{} { return u.exited }
func (u survivingUnit) Close() error            { return nil }

// TestSessionEndReportsASurvivingProcess: a stopped runtime whose unit is not proven empty stopKill after it ended is
// an error naming the survivor, not a successful stop.
func TestSessionEndReportsASurvivingProcess(t *testing.T) {
	path := emptyStore(t)
	recordSession(t, path, false, store.DirectorRunning, store.DirectorStopping)
	u := survivingUnit{exited: make(chan struct{})}
	close(u.exited)
	app, _, _ := newTestApp(nil, nil)
	app.SessionPoll = 10 * time.Millisecond
	w := sessionWatch{storePath: path, n: 1, grace: time.Nanosecond}
	_, _, err := app.waitSession(u, &exec.Cmd{}, make(chan os.Signal), &w)
	if !errors.Is(err, errRuntimeOutlived) || !strings.Contains(err.Error(), "42") {
		t.Fatalf("waitSession = %v, want errRuntimeOutlived naming process 42", err)
	}
}

// TestARuntimeWhoseUnitCannotBeRecordedEnds: a runtime whose unit the session record refuses, here because the
// store has no session 1, is ended with its unit and startRuntime fails, so no runtime runs that the record does not
// name (docs/director.md#the-runtimes-record).
func TestRuntimeWaitsForRecord(t *testing.T) {
	inv := t.TempDir()
	u, err := artisan.NewSessionUnit("test-record-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	cmd := artisantest.Command(inv, artisantest.Worker{Grandchildren: 1})
	_, err = artisan.StartRecorded(u, cmd, func(id artisan.Identity) error {
		time.Sleep(200 * time.Millisecond)
		entries, err := artisantest.Inventory(inv)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			t.Errorf("runtime executed before record: %+v", entries)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeTree(t, inv)
}

func TestARuntimeWhoseUnitCannotBeRecordedEnds(t *testing.T) {
	path := emptyStore(t)
	inv := t.TempDir()
	cmd := artisantest.Command(inv, artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true})
	u, err := startRuntime(cmd, 1, path)
	if err == nil {
		_ = u.Close()
		t.Fatal("startRuntime with no session to record it on succeeded")
	}
	if !strings.Contains(err.Error(), "record") {
		t.Errorf("startRuntime = %v, want it to name the failed record", err)
	}
	entries, err := artisantest.Inventory(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("unrecorded runtime executed: %+v", entries)
	}
	for _, e := range entries {
		if lv, err := artisan.Probe(e.Identity); lv != artisan.Gone {
			_ = artisan.Kill(e.Identity)
			t.Errorf("%s %d is %v (%v) after the failed record", e.Role, e.PID, lv, err)
		}
	}
}
