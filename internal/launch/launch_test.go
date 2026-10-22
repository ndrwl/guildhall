package launch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const deadline = 30 * time.Second

var key = artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}

// fastLadder keeps stop cases short; the ladder itself is artisan's contract.
var fastLadder = artisan.Ladder{Cooperative: 2 * time.Second, Interrupt: 500 * time.Millisecond, Terminate: time.Second, Kill: 3 * time.Second}

// newAttempt returns attempt app/t-1/build/1, recorded running in a new
// store, with its attempt directory and a short state directory.
func newAttempt(t *testing.T) launch.Attempt {
	t.Helper()
	state, err := os.MkdirTemp("", "ln")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	a := launch.Attempt{Store: filepath.Join(t.TempDir(), "guildhall.store"), StateDir: state, Key: key}
	withStore(t, a, func(s *store.Store) {
		digest := "sha256:" + strings.Repeat("a", 64)
		task := store.Task{Repository: "app", ID: "t-1", Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
		if _, err := s.CreateTask("task", task); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StartAttempt("start", "app", "t-1", "build"); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.MkdirAll(a.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return a
}

func withStore(t *testing.T, a launch.Attempt, fn func(*store.Store)) {
	t.Helper()
	s, err := store.Open(a.Store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fn(s)
}

func ownership(t *testing.T, a launch.Attempt) store.Ownership {
	t.Helper()
	var o store.Ownership
	withStore(t, a, func(s *store.Store) {
		var err error
		if o, err = s.Ownership("app", "t-1", "build", 1); err != nil {
			t.Fatal(err)
		}
	})
	return o
}

// storedSpend is the spend recorded on attempt app/t-1/build/1, nil when none is.
func storedSpend(t *testing.T, a launch.Attempt) *store.Spend {
	t.Helper()
	var sp *store.Spend
	withStore(t, a, func(s *store.Store) {
		as, err := s.Attempts("app", "t-1")
		if err != nil || len(as) != 1 {
			t.Fatalf("attempts %+v, %v", as, err)
		}
		sp = as[0].Spend
	})
	return sp
}

// reported is the usage a worker reports in the launch tests, and spent the spend it is recorded as.
var (
	reported = adapter.Usage{Tokens: adapter.Measured, InputTokens: 7, OutputTokens: 8, CacheCreationInputTokens: 9, CacheReadInputTokens: 10, Cost: adapter.Estimated, CostUSD: 0.25}
	spent    = store.Spend{Tokens: store.SpendMeasured, InputTokens: 7, OutputTokens: 8, CacheCreationInputTokens: 9, CacheReadInputTokens: 10, Cost: store.SpendEstimated, CostUSD: 0.25}
)

func storedOutcome(t *testing.T, a launch.Attempt) store.AttemptOutcome {
	t.Helper()
	var out store.AttemptOutcome
	withStore(t, a, func(s *store.Store) {
		as, err := s.Attempts("app", "t-1")
		if err != nil || len(as) != 1 {
			t.Fatalf("attempts %+v, %v", as, err)
		}
		out = as[0].Outcome
	})
	return out
}

// worker is a test-binary worker tree for the attempt, recording into inv.
func worker(t *testing.T, a launch.Attempt, w artisantest.Worker) (adapter.Command, string) {
	t.Helper()
	inv := t.TempDir()
	if w.Lifetime == 0 {
		w.Lifetime = deadline
	}
	cmd := artisantest.Command(inv, w)
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(inv, "release"), nil, 0o600)
		entries, _ := artisantest.Inventory(inv)
		for _, e := range entries {
			for end := time.Now().Add(deadline); ; time.Sleep(20 * time.Millisecond) {
				if lv, _ := artisan.Probe(e.Identity); lv != artisan.Alive {
					break
				}
				if time.Now().After(end) {
					t.Errorf("%s %v outlived the case", e.Role, e.Identity)
					_ = artisantest.KillVerified(e.Identity)
					break
				}
			}
		}
	})
	return adapter.Command{Argv: cmd.Args, Env: cmd.Env}, inv
}

type ran struct {
	r   launch.Report
	err error
}

func start(a launch.Attempt, cmd adapter.Command) (<-chan ran, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ran, 1)
	go func() {
		r, err := launch.Run(ctx, launch.Config{Attempt: a, Command: cmd, Ladder: fastLadder, ResultGrace: time.Second, HeartbeatInterval: 100 * time.Millisecond})
		done <- ran{r, err}
	}()
	return done, cancel
}

func wait(t *testing.T, done <-chan ran) ran {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(deadline):
		t.Fatalf("Run did not return within %v", deadline)
		return ran{}
	}
}

// running waits until Status classifies the attempt running.
func running(t *testing.T, a launch.Attempt) {
	t.Helper()
	var c reconcile.Classification
	var owner store.Ownership
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		var err error
		if c, err = launch.Status(context.Background(), a); err == nil && c.State == reconcile.Running {
			owner = ownership(t, a)
			if owner.State == store.OwnerRunning && owner.Containment.Kind != "" {
				return
			}
		}
	}
	t.Fatalf("waited %v for running with recorded containment; classification %+v; ownership %+v", deadline, c, owner)
}

func TestRunFinishesAndRecordsTheAttempt(t *testing.T) {
	for _, status := range []adapter.Status{adapter.StatusSucceeded, adapter.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			a := newAttempt(t)
			// The succeeded worker reports its usage, as an adapter writes it beside the result; the failed one
			// reports none.
			want := store.UnknownSpend
			if status == adapter.StatusSucceeded {
				if err := adapter.WriteUsage(a.Files().ResultFile, reported); err != nil {
					t.Fatal(err)
				}
				want = spent
			}
			cmd, _ := worker(t, a, artisantest.Worker{Grandchildren: 1, Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), status), ResultAfter: 300 * time.Millisecond})
			done, cancel := start(a, cmd)
			defer cancel()
			r := wait(t, done)
			if r.err != nil || r.r.Outcome.State != artisan.Finished || r.r.Result == nil || r.r.Result.Status != status || r.r.Attempt != key.String() {
				t.Fatalf("Run() = %+v, %v", r.r, r.err)
			}
			if want := store.AttemptOutcome(status); r.r.AttemptOutcome != want || storedOutcome(t, a) != want {
				t.Errorf("attempt outcome %q, stored %q; want %q", r.r.AttemptOutcome, storedOutcome(t, a), want)
			}
			if got := storedSpend(t, a); got == nil || *got != want {
				t.Errorf("stored spend %+v; want %+v", got, want)
			}
			o := ownership(t, a)
			self, _ := artisan.Self()
			if o.State != store.OwnerReleased || o.LauncherIdentity != store.ProcessIdentity(self) || o.Endpoint == "" || o.Containment.Kind == "" {
				t.Errorf("ownership %+v; want released, naming this launcher, its endpoint and containment", o)
			}
			if _, err := os.Stat(o.Endpoint); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("endpoint %s left behind: %v", o.Endpoint, err)
			}
			for _, f := range []string{artisan.ContainmentFile, artisan.HeartbeatFile, artisan.OutcomeFile} {
				if _, err := os.Stat(filepath.Join(a.Dir(), f)); err != nil {
					t.Errorf("%s: %v", f, err)
				}
			}
			if h, err := artisan.ReadHeartbeat(a.Dir()); err != nil || h.Key() != key {
				t.Errorf("heartbeat %+v, %v", h, err)
			}
		})
	}
}

func TestRunWorkerExitingWithoutResultFails(t *testing.T) {
	a := newAttempt(t)
	if err := os.WriteFile(adapter.UsageFile(a.Files().ResultFile), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 200 * time.Millisecond})
	done, cancel := start(a, cmd)
	defer cancel()
	r := wait(t, done)
	if r.err != nil || r.r.Outcome.State != artisan.Failed || r.r.AttemptOutcome != store.AttemptFailed || storedOutcome(t, a) != store.AttemptFailed {
		t.Errorf("Run() = %+v, %v; want failed and recorded", r.r, r.err)
	}
	// A malformed usage file is recorded as unknown, never trusted.
	if got := storedSpend(t, a); got == nil || *got != store.UnknownSpend {
		t.Errorf("stored spend %+v; want unknown", got)
	}
}

func TestRunRefusesALiveOwnerAndStartsNothing(t *testing.T) {
	a := newAttempt(t)
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	prior := store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1, LauncherIdentity: store.ProcessIdentity(self)}
	withStore(t, a, func(s *store.Store) {
		if _, err := s.TakeOwnership("prior", prior, launch.ProbeOwner); err != nil {
			t.Fatal(err)
		}
	})
	cmd, inv := worker(t, a, artisantest.Worker{})
	done, cancel := start(a, cmd)
	defer cancel()
	if r := wait(t, done); !errors.Is(r.err, store.ErrOwnerLive) {
		t.Errorf("Run() = %+v, %v; want ErrOwnerLive", r.r, r.err)
	}
	if entries, _ := artisantest.Inventory(inv); len(entries) != 0 {
		t.Errorf("a refused run started %v", entries)
	}
	if o := ownership(t, a); o.LauncherIdentity != store.ProcessIdentity(self) || o.Endpoint != "" || o.State != store.OwnerStarting {
		t.Errorf("ownership changed: %+v", o)
	}
	if storedOutcome(t, a) != "" {
		t.Errorf("a refused run finished the attempt")
	}
}

func TestStopThroughTheEndpoint(t *testing.T) {
	a := newAttempt(t)
	cmd, _ := worker(t, a, artisantest.Worker{Grandchildren: 1, CancelMarker: a.Files().ResultFile + ".cancelled"})
	done, cancel := start(a, cmd)
	defer cancel()
	running(t, a)
	rep, err := launch.Stop(context.Background(), a)
	if err != nil || rep.Endpoint != "stopped" || rep.Classification.State != reconcile.Cancelled {
		t.Errorf("Stop() = %+v, %v; want stopped by the endpoint and cancelled", rep, err)
	}
	r := wait(t, done)
	if r.err != nil || r.r.Outcome.State != artisan.Cancelled || r.r.Outcome.Rung != 1 || r.r.AttemptOutcome != store.AttemptStopped || storedOutcome(t, a) != store.AttemptStopped {
		t.Errorf("Run() = %+v, %v; want cancelled at rung 1 and stopped", r.r, r.err)
	}
	if m, err := a.Files().Marked(); !m || err != nil {
		t.Errorf("marker %v, %v", m, err)
	}
	if rep, err := launch.Stop(context.Background(), a); !errors.Is(err, launch.ErrNotStopped) || rep.Classification.State != reconcile.Cancelled {
		t.Errorf("second Stop() with released but live launcher = %+v, %v; want ErrNotStopped", rep, err)
	}
}

func TestRunStopsWhenTheContextEnds(t *testing.T) {
	a := newAttempt(t)
	cmd, _ := worker(t, a, artisantest.Worker{IgnoreInterrupt: true})
	done, cancel := start(a, cmd)
	defer cancel()
	running(t, a)
	// Cancellation must stop the attempt whether the worker has started or not;
	// the shared artisan stop contract tests escalation through specific rungs.
	cancel()
	r := wait(t, done)
	if r.err != nil || r.r.Outcome.State != artisan.Cancelled || r.r.AttemptOutcome != store.AttemptStopped || storedOutcome(t, a) != store.AttemptStopped {
		t.Errorf("Run() = %+v, %v; want cancelled and stopped", r.r, r.err)
	}
}

func TestStopWithoutAnOwnerOnlyClassifies(t *testing.T) {
	a := newAttempt(t)
	rep, err := launch.Stop(context.Background(), a)
	if err != nil || rep.Classification.State != reconcile.Absent || rep.Endpoint == "stopped" || rep.Endpoint == "" {
		t.Errorf("Stop() = %+v, %v; want absent with the reason the endpoint was not asked", rep, err)
	}
}

func TestStopASilentLiveOwnerIsNotStopped(t *testing.T) {
	a := newAttempt(t)
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	path, err := artisan.EndpointPath(a.StateDir, key)
	if err != nil {
		t.Fatal(err)
	}
	o := store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1, LauncherIdentity: store.ProcessIdentity(self)}
	withStore(t, a, func(s *store.Store) {
		if _, err := s.TakeOwnership("own", o, launch.ProbeOwner); err != nil {
			t.Fatal(err)
		}
		o.Endpoint, o.State = path, store.OwnerRunning
		if _, err := s.UpdateOwnership("endpoint", o); err != nil {
			t.Fatal(err)
		}
	})
	rep, err := launch.Stop(context.Background(), a)
	if !errors.Is(err, launch.ErrNotStopped) || rep.Classification.State != reconcile.Unknown || rep.Endpoint == "stopped" {
		t.Errorf("Stop() = %+v, %v; want ErrNotStopped and unknown", rep, err)
	}
	if m, _ := a.Files().Marked(); m {
		t.Error("Stop wrote the marker without the owner")
	}
}

func TestStatusReportsAStoreItCannotRead(t *testing.T) {
	a := newAttempt(t)
	a.Store = filepath.Join(t.TempDir(), "missing.store")
	if c, err := launch.Status(context.Background(), a); err == nil {
		t.Errorf("Status() = %+v with no store", c)
	}
}
