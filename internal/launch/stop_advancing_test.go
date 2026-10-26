package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/launch/launchtest"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestStopNamesALauncherStillAdvancing: a launcher that released its attempt and still runs its advance, held as
// the task lock holds it, is named advancing and not proven stopped; launchtest.Stop waits it out and then proves
// the attempt stopped with the launcher gone.
func TestStopNamesALauncherStillAdvancing(t *testing.T) {
	dir, err := os.MkdirTemp("", "lc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := newProcess(t)
	p.Store, p.StateDir = filepath.Join(dir, "director.store"), filepath.Join(dir, "state")
	// Let the worker finish normally so containment is recorded on every OS.
	// A startup timeout can cancel before a process-group worker is started,
	// leaving no containment with which Stop can prove termination.
	spec := prepared(t, p, fixtureSpec(t, instructions[interptest.Succeeds]))
	spec.Copy = dir
	k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
	record, unlocked := filepath.Join(t.TempDir(), "advances.jsonl"), filepath.Join(t.TempDir(), "unlocked")
	b, err := json.Marshal(advanceDouble{Record: record, Dir: k.Dir(p.StateDir), Store: p.Store, Until: unlocked})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(advanceEnv, string(b))
	// Unblock the advance however the test ends, so its launcher does not outlive the test.
	t.Cleanup(func() { _ = os.WriteFile(unlocked, nil, 0o600) })
	if _, err := p.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	a := launch.Attempt{Key: k, Store: p.Store, StateDir: p.StateDir}
	for end := time.Now().Add(deadline); ; time.Sleep(20 * time.Millisecond) {
		state, err := launch.Status(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		// Status is read-only: do not stop the worker before it starts.
		if state.State == reconcile.Finished {
			rep, err := launch.Stop(context.Background(), a)
			if errors.Is(err, launch.ErrNotStopped) && rep.Launcher == launch.LauncherAdvancing {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("Stop while the advance is held = %+v, %v; want launcher %s and %v", rep, err, launch.LauncherAdvancing, launch.ErrNotStopped)
			}
		}
		if time.Now().After(end) {
			t.Fatalf("Status = %+v; want a finished worker with its advance held", state)
		}
	}
	stops := 0
	err = launchtest.Stop(func() (launch.StopReport, error) {
		if stops++; stops == 3 {
			if err := os.WriteFile(unlocked, nil, 0o600); err != nil {
				t.Error(err)
			}
		}
		return launch.Stop(context.Background(), a)
	})
	if err != nil {
		t.Fatalf("launchtest.Stop = %v after %d stops, want the attempt proven stopped", err, stops)
	}
	if stops < 3 {
		t.Errorf("launchtest.Stop proved the attempt stopped after %d stops, before the advance was released", stops)
	}
	s, err := store.Open(p.Store, store.Options{ReadOnly: true, LockTimeout: storeWait})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Ownership(k.Repository, k.TaskID, k.Step, k.Attempt)
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if lv, err := artisan.Probe(artisan.Identity(o.LauncherIdentity)); err != nil || lv != artisan.Gone {
		t.Errorf("launcher after launchtest.Stop = %v, %v; want gone", lv, err)
	}
}
