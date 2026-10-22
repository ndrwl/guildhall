package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestHeldStartRefusesInTheStartTransaction holds starts with AttemptCaps.Hold over each session state: a held start
// is refused with ErrDirectorHeld and writes nothing, a start without Hold is admitted, a replay of a start already
// recorded returns its receipt while held, and a start after resume is admitted.
func TestHeldStartRefusesInTheStartTransaction(t *testing.T) {
	cases := []struct {
		name string
		path []DirectorState
		stop bool
		held bool
	}{
		{"no session", nil, false, false},
		{"recovering", []DirectorState{}, false, false},
		{"running", []DirectorState{DirectorRunning}, false, false},
		{"paused", []DirectorState{DirectorRunning, DirectorPaused}, false, true},
		{"stopping", []DirectorState{DirectorRunning}, true, true},
		{"stopped", []DirectorState{DirectorStopped}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustCreate(t, s, "task-1", sampleTask("app", "t-1"))
			if _, err := s.StartCappedAttempt("start-plan", "app", "t-1", "plan", AttemptCaps{Hold: true}); err != nil {
				t.Fatal(err)
			}
			if c.path != nil {
				mustStartSession(t, s, sampleSession(1))
				for _, st := range c.path {
					mustMove(t, s, 1, st)
				}
			}
			if c.stop {
				d, _ := s.DirectorSession(1)
				if _, err := s.RequestDirectorStop(1, false, d.StateSequence); err != nil {
					t.Fatal(err)
				}
			}
			receipts := countReceipts(t, s)
			_, err := s.StartCappedAttempt("start-build", "app", "t-1", "build", AttemptCaps{Hold: true, MaxRunning: 10})
			if !c.held {
				if err != nil {
					t.Fatalf("start = %v, want admitted", err)
				}
				return
			}
			if !errors.Is(err, ErrDirectorHeld) {
				t.Fatalf("held start = %v, want ErrDirectorHeld", err)
			}
			if got := countReceipts(t, s); got != receipts {
				t.Errorf("a held start wrote %d receipts", got-receipts)
			}
			if a, _ := s.Attempts("app", "t-1"); len(a) != 1 {
				t.Errorf("a held start recorded attempts %+v", a)
			}
			if r, err := s.StartCappedAttempt("start-plan", "app", "t-1", "plan", AttemptCaps{Hold: true}); err != nil || r.Attempt != 1 {
				t.Errorf("replayed start while held = %+v, %v; want its receipt", r, err)
			}
			if _, err := s.StartCappedAttempt("start-review", "app", "t-1", "review", AttemptCaps{}); err != nil {
				t.Errorf("a start without Hold = %v, want admitted", err)
			}
		})
	}
}

// TestHeldStartIsAdmittedAfterResume admits a held start once the session runs again.
func TestHeldStartIsAdmittedAfterResume(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "task-1", sampleTask("app", "t-1"))
	mustStartSession(t, s, sampleSession(1))
	mustMove(t, s, 1, DirectorRunning)
	mustMove(t, s, 1, DirectorPaused)
	if _, err := s.StartCappedAttempt("start-build", "app", "t-1", "build", AttemptCaps{Hold: true}); !errors.Is(err, ErrDirectorHeld) {
		t.Fatalf("paused start = %v", err)
	}
	mustMove(t, s, 1, DirectorRunning)
	if r, err := s.StartCappedAttempt("start-build", "app", "t-1", "build", AttemptCaps{Hold: true}); err != nil || r.Attempt != 1 {
		t.Fatalf("start after resume = %+v, %v", r, err)
	}
}

// TestHeldStartAndPauseNeverInterleave races held starts against a pause across two handles on one file: every
// start is either recorded with a receipt committed before the pause's or refused, never committed after it.
func TestHeldStartAndPauseNeverInterleave(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	mustMove(t, s, 1, DirectorRunning)
	const tasks = 40
	for i := range tasks {
		mustCreate(t, s, fmt.Sprintf("task-%d", i), sampleTask("app", fmt.Sprintf("t%d", i)))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	starts := make(chan error, tasks)
	go func() {
		for i := range tasks {
			st, err := Open(path, Options{LockTimeout: time.Minute})
			if err != nil {
				starts <- err
				continue
			}
			_, err = st.StartCappedAttempt(fmt.Sprintf("start-%d", i), "app", fmt.Sprintf("t%d", i), "build", AttemptCaps{Hold: true})
			starts <- errors.Join(err, st.Close())
		}
	}()
	ps, err := Open(path, Options{LockTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := ps.DirectorSession(1)
	pause, err := ps.SetDirectorState(1, DirectorPaused, d.StateSequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	for range tasks {
		if err := <-starts; err != nil && !errors.Is(err, ErrDirectorHeld) {
			t.Fatal(err)
		}
	}
	s = openTest(t, path)
	for i := range tasks {
		r, err := s.Receipt(fmt.Sprintf("start-%d", i))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if r.Sequence > pause.Sequence {
			t.Errorf("start %d committed at sequence %d, after the pause at %d", i, r.Sequence, pause.Sequence)
		}
	}
}

// TestStopUnownedAttempt stops an attempt nothing owns in one mutation: the outcome is stopped, the running count
// drops, a replay returns the receipt, a later finish of the same stop stands, a later take-over is refused, and an owned, finished or missing attempt is
// refused writing nothing.
func TestStopUnownedAttempt(t *testing.T) {
	t.Run("unowned", func(t *testing.T) {
		s, _ := newStore(t)
		mustCreate(t, s, "task-1", sampleTask("app", "t-1"))
		if _, err := s.StartAttempt("start", "app", "t-1", "build"); err != nil {
			t.Fatal(err)
		}
		r, err := s.StopUnownedAttempt("stop", "app", "t-1", "build", 1)
		if err != nil {
			t.Fatal(err)
		}
		if a, _ := s.Attempts("app", "t-1"); len(a) != 1 || a[0].Outcome != AttemptStopped {
			t.Fatalf("attempts = %+v, want build 1 stopped", a)
		}
		if u, _ := s.AttemptUsage(); u.Running != 0 {
			t.Errorf("running = %d after the stop", u.Running)
		}
		if again, err := s.StopUnownedAttempt("stop", "app", "t-1", "build", 1); err != nil || again.Sequence != r.Sequence {
			t.Errorf("replay = %+v, %v; want receipt %d", again, err, r.Sequence)
		}
		if _, err := s.FinishAttempt("launcher-finish", "app", "t-1", "build", 1, AttemptStopped); err != nil {
			t.Errorf("a later FinishAttempt of the same stop = %v, want it to stand", err)
		}
		o := takeB(1)
		if _, err := s.TakeOwnership("take", o, probeSays(ProbeGone, nil, new(int))); !errors.Is(err, ErrInvalid) {
			t.Errorf("take-over of a stopped attempt = %v, want ErrInvalid", err)
		}
	})
	t.Run("owned", func(t *testing.T) {
		s, _ := newStore(t)
		ownedAttempt(t, s)
		receipts := countReceipts(t, s)
		if _, err := s.StopUnownedAttempt("stop", "app", "t-1", "build", 1); !errors.Is(err, ErrOwned) {
			t.Fatalf("stop of an owned attempt = %v, want ErrOwned", err)
		}
		if countReceipts(t, s) != receipts {
			t.Error("a refused stop wrote a receipt")
		}
		if a, _ := s.Attempts("app", "t-1"); a[0].Outcome != "" {
			t.Errorf("a refused stop finished the attempt: %+v", a)
		}
	})
	t.Run("released", func(t *testing.T) {
		s, _ := newStore(t)
		ownedAttempt(t, s)
		if _, err := s.FinishAttempt("finish", "app", "t-1", "build", 1, AttemptFailed); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StopUnownedAttempt("stop", "app", "t-1", "build", 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("stop of a finished attempt = %v, want ErrInvalid", err)
		}
		if a, _ := s.Attempts("app", "t-1"); a[0].Outcome != AttemptFailed {
			t.Errorf("the finished outcome changed: %+v", a)
		}
	})
	t.Run("missing", func(t *testing.T) {
		s, _ := newStore(t)
		mustCreate(t, s, "task-1", sampleTask("app", "t-1"))
		if _, err := s.StopUnownedAttempt("stop", "app", "t-1", "build", 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stop of a missing attempt = %v, want ErrNotFound", err)
		}
	})
}
