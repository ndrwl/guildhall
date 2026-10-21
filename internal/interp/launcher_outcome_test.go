package interp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp/interptest"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

type interruptedOutbox struct {
	interp.Outbox
	fail bool
}

func (o *interruptedOutbox) Send(to store.Recipient, envelope messaging.Envelope) (store.Receipt, error) {
	if o.fail {
		o.fail = false
		return store.Receipt{}, errors.New("interrupted notification")
	}
	return o.Outbox.Send(to, envelope)
}

type interruptedIntake struct {
	interp.Records
	failDocument  string
	finishOutcome store.AttemptOutcome
	failContest   bool
}

func (r *interruptedIntake) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	if d.Name == r.failDocument {
		r.failDocument = ""
		return store.Receipt{}, errors.New("interrupted document intake")
	}
	return r.Records.PutDocument(id, d)
}

func (r *interruptedIntake) ContestRuling(id, repo, task, doc, finding, by string) (store.Receipt, error) {
	if r.failContest {
		r.failContest = false
		return store.Receipt{}, errors.New("interrupted contest")
	}
	return r.Records.ContestRuling(id, repo, task, doc, finding, by)
}

func (r *interruptedIntake) FinishAttempt(id, repo, task, step string, n int, outcome store.AttemptOutcome) (store.Receipt, error) {
	if r.finishOutcome != "" {
		if _, err := r.Records.FinishAttempt("launcher-race", repo, task, step, n, r.finishOutcome); err != nil {
			return store.Receipt{}, err
		}
		r.finishOutcome = ""
	}
	return r.Records.FinishAttempt(id, repo, task, step, n, outcome)
}

func TestInterruptedLauncherIntake(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Succeed("planned",
				interptest.File{Path: "spec.md", Data: specV1(t)},
				interptest.File{Path: "extra.md", Data: []byte("additional evidence")})))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, err := e.h.Records.FinishAttempt("launcher-finish", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			e.eng.Records = &interruptedIntake{Records: e.h.Records, failDocument: "plan-1-spec-2"}
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); err == nil {
				t.Fatal("expected interrupted intake")
			}
			if _, ok := e.documents(t)["plan-1-spec"]; !ok {
				t.Fatal("first output missing")
			}
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, ok := e.documents(t)["plan-1-spec-2"]; !ok {
				t.Fatal("second output lost on recovery")
			}
		})
	}
}

func TestInterpreterFinishRace(t *testing.T) {
	for _, b := range backends {
		for _, outcome := range []store.AttemptOutcome{store.AttemptSucceeded, store.AttemptStopped, store.AttemptFailed} {
			t.Run(b.name+"/"+string(outcome), func(t *testing.T) {
				e := newEnv(t, b, straightScript(t)[:1]...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				e.eng.Records = &interruptedIntake{Records: e.h.Records, finishOutcome: outcome}
				next, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.AttemptResult, Repository: "app", Task: "t7", Step: "plan", Attempt: 1})
				if outcome == store.AttemptSucceeded {
					if err != nil || next.Wait != interp.WaitDecision {
						t.Fatalf("same outcome: %+v, %v", next, err)
					}
				} else if err == nil {
					t.Fatalf("conflicting outcome advanced: %+v", next)
				}
				if got := e.attempts(t)[0].Outcome; got != outcome {
					t.Fatalf("outcome = %s, want %s", got, outcome)
				}
			})
		}
	}
}

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
