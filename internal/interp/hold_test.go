package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestHoldStartsNoNewAttempt holds the engine around the straight path's first two Artisan steps: a held advance
// records and launches no attempt and waits paused, while the plan's result is still taken and its approval still
// opened; once the hold lifts, the next advance starts the held attempt.
func TestHoldStartsNoNewAttempt(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			script := straightScript(t)
			e := newEnv(t, b, script[0], script[1])
			held := true
			e.eng.Hold = func() (bool, error) { return held, nil }

			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if want := (interp.Next{Wait: interp.WaitPaused, Step: "plan"}); next != want {
				t.Fatalf("held task-created: %+v, want %+v", next, want)
			}
			if got := e.attempts(t); len(got) != 0 {
				t.Fatalf("held task-created recorded attempts %+v", got)
			}
			if _, err := os.Stat(filepath.Join(e.eng.StateDir, "app", "t7", "plan-1")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("held task-created prepared the attempt: %v", err)
			}

			held = false
			if next = e.advance(t, interp.Event{Kind: interp.TaskCreated}); next != (interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1}) {
				t.Fatalf("task-created after the hold: %+v", next)
			}

			// Held again, the plan's result is taken and its approval opened and answered; the build is held.
			held = true
			next = e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"}); next != want {
				t.Fatalf("held plan result: %+v, want %+v", next, want)
			}
			if got := e.attempts(t); len(got) != 1 || got[0].Outcome != store.AttemptSucceeded {
				t.Fatalf("attempts after the held result = %+v", got)
			}
			e.answer(t, "accept-1", "approved")
			next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			if want := (interp.Next{Wait: interp.WaitPaused, Step: "build"}); next != want {
				t.Fatalf("held approval: %+v, want %+v", next, want)
			}
			if d := e.documents(t)["accept-1-spec"]; d.Name == "" {
				t.Error("the held approval did not record the accepted spec")
			}
			if got := e.attempts(t); len(got) != 1 {
				t.Fatalf("held approval recorded attempts %+v", got)
			}

			held = false
			if next = e.advance(t, interp.Event{Kind: interp.TaskCreated}); next != (interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1}) {
				t.Fatalf("advance after the hold: %+v", next)
			}
		})
	}
}

// TestHoldErrorFailsTheAdvance fails an advance whose hold cannot be read, recording no attempt.
func TestHoldErrorFailsTheAdvance(t *testing.T) {
	e := newEnv(t, backends[0])
	boom := errors.New("store unreadable")
	e.eng.Hold = func() (bool, error) { return false, boom }
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, boom) {
		t.Fatalf("Advance = %v, want the hold's error", err)
	}
	if got := e.attempts(t); len(got) != 0 {
		t.Fatalf("attempts = %+v", got)
	}
}
