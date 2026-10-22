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
// supervisor's wait returns only once every process the runtime started has gone (docs/director.md#stop).
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
			u, err := startRuntime(cmd, 1)
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
			for _, id := range tree {
				if lv, err := artisan.Probe(id); lv != artisan.Gone {
					t.Errorf("process %d of the runtime's tree is %v (%v) after the session ended", id.PID, lv, err)
				}
			}
		})
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
