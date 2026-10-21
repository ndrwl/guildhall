package interp_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestLauncherRecordedOutcome: the owned launcher (`artisan run`) records an attempt's store outcome when it
// decides it, which may be before or after the result's intake. Either order records the outputs once and
// advances the same way; a stopped or failed outcome the launcher recorded stands over a succeeded result.
func TestLauncherRecordedOutcome(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name+"/succeeded before intake", func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[:1]...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, err := e.h.Records.FinishAttempt("launch-finish:x", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"}); next != want {
				t.Fatalf("after the result: %+v, want %+v", next, want)
			}
			if _, ok := e.documents(t)["plan-1-spec"]; !ok {
				t.Error("the plan's output was not recorded")
			}
			if again := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}); again != next {
				t.Errorf("replayed intake: %+v, want %+v", again, next)
			}
		})
		t.Run(b.name+"/succeeded before any event names it", func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[:1]...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, err := e.h.Records.FinishAttempt("launch-finish:x", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"}); next != want {
				t.Fatalf("after a later advance: %+v, want %+v", next, want)
			}
			if _, ok := e.documents(t)["plan-1-spec"]; !ok {
				t.Error("the plan's output was not recorded")
			}
		})
		t.Run(b.name+"/stopped stands over a succeeded result", func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[:1]...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, err := e.h.Records.FinishAttempt("launch-finish:x", "app", "t7", "plan", 1, store.AttemptStopped); err != nil {
				t.Fatal(err)
			}
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1}); next != want {
				t.Fatalf("after the result: %+v, want %+v", next, want)
			}
			if _, ok := e.documents(t)["plan-1-spec"]; ok {
				t.Error("a stopped attempt's output was recorded")
			}
		})
	}
}
