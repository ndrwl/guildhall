package launch_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// runningUnit starts worker tree w in a real unit this test holds, as a
// launcher would have, and returns its recorded containment and the
// inventory directory the tree records into.
func runningUnit(t *testing.T, w artisantest.Worker) (store.Containment, string, artisan.Unit) {
	t.Helper()
	u, err := artisan.NewUnit("resume-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	inv := t.TempDir()
	t.Cleanup(func() {
		_ = u.Close()
		entries, _ := artisantest.Inventory(inv)
		for _, e := range entries {
			_ = artisan.Kill(e.Identity)
		}
	})
	if w.Lifetime == 0 {
		w.Lifetime = deadline
	}
	worker, err := u.Start(artisantest.Command(inv, w))
	if err != nil {
		t.Fatal(err)
	}
	c := u.Containment()
	return store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(worker)}, inv, u
}

// tree waits for n recorded processes in inv.
func tree(t *testing.T, inv string, n int) []artisan.Identity {
	t.Helper()
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		entries, _ := artisantest.Inventory(inv)
		if len(entries) >= n {
			var ids []artisan.Identity
			for _, e := range entries {
				ids = append(ids, e.Identity)
			}
			return ids
		}
	}
	t.Fatalf("waited %v for %d recorded processes", deadline, n)
	return nil
}

func resume(t *testing.T, a launch.Attempt) (launch.ResumeReport, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return launch.Resume(ctx, a)
}

func noOutcome(t *testing.T, a launch.Attempt) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(a.Dir(), artisan.OutcomeFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("outcome.json written: %v", err)
	}
}

func TestResumeWithoutAnOwnerDoesNothing(t *testing.T) {
	a := newAttempt(t)
	rep, err := resume(t, a)
	if err != nil || rep.Owner != launch.OwnerNone || len(rep.Swept) != 0 || rep.AttemptOutcome != "" {
		t.Fatalf("Resume = %+v, %v; want owner none and nothing done", rep, err)
	}
	noOutcome(t, a)
	if got := storedOutcome(t, a); got != "" {
		t.Errorf("store attempt finished %q", got)
	}
}

func TestResumeRefusesALiveOrUnprovenOwner(t *testing.T) {
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		launcher    func(t *testing.T) artisan.Identity
		containment bool
		want        error
		owner       string
	}{
		{"live launcher", func(*testing.T) artisan.Identity { return self }, true, store.ErrOwnerLive, launch.OwnerLive},
		{"gone launcher without containment", func(t *testing.T) artisan.Identity { id, _ := exitedOwner(t); return id }, false, store.ErrOwnerUnknown, launch.OwnerUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAttempt(t)
			c, inv, _ := runningUnit(t, artisantest.Worker{Grandchildren: 1})
			running := tree(t, inv, 2)
			if !tc.containment {
				c = store.Containment{}
			}
			recordOwner(t, a, tc.launcher(t), c, "")
			before := ownership(t, a)
			rep, err := resume(t, a)
			if !errors.Is(err, tc.want) || rep.Owner != tc.owner || len(rep.Swept) != 0 {
				t.Fatalf("Resume = %+v, %v; want owner %s and %v", rep, err, tc.owner, tc.want)
			}
			for _, id := range running {
				if lv, _ := artisan.Probe(id); lv != artisan.Alive {
					t.Errorf("process %v is %v after a refused resume", id, lv)
				}
			}
			noOutcome(t, a)
			if after := ownership(t, a); after != before {
				t.Errorf("ownership changed: %+v, was %+v", after, before)
			}
		})
	}
}

func TestResumeSweepsAndSettlesAnExitedOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		worker  artisantest.Worker
		files   func(t *testing.T, a launch.Attempt)
		state   artisan.OutcomeState
		outcome store.AttemptOutcome
	}{
		{"surviving tree without a result", artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true}, nil, artisan.Failed, store.AttemptFailed},
		{"emptied unit with a result", artisantest.Worker{Lifetime: 50 * time.Millisecond}, func(t *testing.T, a launch.Attempt) {
			if err := adapter.WriteResult(a.Files().ResultFile, adapter.Result{Identity: a.Identity(), Status: adapter.StatusSucceeded, Summary: "done"}); err != nil {
				t.Fatal(err)
			}
			if err := adapter.WriteUsage(a.Files().ResultFile, reported); err != nil {
				t.Fatal(err)
			}
		}, artisan.Finished, store.AttemptSucceeded},
		{"surviving tree with the marker", artisantest.Worker{Grandchildren: 1}, func(t *testing.T, a launch.Attempt) {
			if err := a.Files().Mark(); err != nil {
				t.Fatal(err)
			}
		}, artisan.Cancelled, store.AttemptStopped},
		{"an outcome already decided", artisantest.Worker{Lifetime: 50 * time.Millisecond}, func(t *testing.T, a launch.Attempt) {
			out := `{"schema":"guildhall.attempt-outcome/1","state":"cancelled","detail":"stopped","rung":1}` + "\n"
			if err := os.WriteFile(filepath.Join(a.Dir(), artisan.OutcomeFile), []byte(out), 0o600); err != nil {
				t.Fatal(err)
			}
		}, artisan.Cancelled, store.AttemptStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAttempt(t)
			c, inv, u := runningUnit(t, tc.worker)
			n := 1 + tc.worker.Grandchildren
			running := tree(t, inv, n)
			if tc.worker.Grandchildren == 0 {
				select {
				case <-u.Exited():
				case <-time.After(deadline):
					t.Fatal("worker did not exit")
				}
			}
			if tc.files != nil {
				tc.files(t, a)
			}
			gone, _ := exitedOwner(t)
			recordOwner(t, a, gone, c, "")
			rep, err := resume(t, a)
			if err != nil || rep.Owner != launch.OwnerExited {
				t.Fatalf("Resume = %+v, %v; want owner exited and no error", rep, err)
			}
			if tc.worker.Grandchildren > 0 {
				for _, id := range running {
					if !slices.Contains(rep.Swept, id) {
						t.Errorf("swept %v does not name %v", rep.Swept, id)
					}
				}
			}
			for _, id := range running {
				if lv, _ := artisan.Probe(id); lv != artisan.Gone {
					t.Errorf("process %v is %v after resume", id, lv)
				}
			}
			if rep.Outcome == nil || rep.Outcome.State != tc.state || rep.AttemptOutcome != tc.outcome {
				t.Fatalf("Resume = %+v, want %s recorded %s", rep, tc.state, tc.outcome)
			}
			if file, err := artisan.ReadOutcome(a.Dir()); err != nil || file.State != tc.state {
				t.Errorf("outcome.json %+v, %v", file, err)
			}
			if got := storedOutcome(t, a); got != tc.outcome {
				t.Errorf("store outcome %q, want %q", got, tc.outcome)
			}
			// The settled attempt's spend is what its worker reported, or unknown when it reported none.
			wantSpend := store.UnknownSpend
			if tc.outcome == store.AttemptSucceeded {
				wantSpend = spent
			}
			if got := storedSpend(t, a); got == nil || *got != wantSpend {
				t.Errorf("stored spend %+v, want %+v", got, wantSpend)
			}
			if o := ownership(t, a); o.State != store.OwnerReleased {
				t.Errorf("ownership %+v not released", o)
			}
			again, err := resume(t, a)
			if err != nil || again.Owner != launch.OwnerReleased || len(again.Swept) != 0 || again.AttemptOutcome != "" {
				t.Errorf("second Resume = %+v, %v; want owner released and nothing done", again, err)
			}
			if got := storedOutcome(t, a); got != tc.outcome {
				t.Errorf("store outcome after a second resume %q, want %q", got, tc.outcome)
			}
		})
	}
}
