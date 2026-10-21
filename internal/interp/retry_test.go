package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

func retried(step string, n int, reason string) interp.Event {
	return interp.Event{Kind: interp.AttemptRetried, Step: step, Attempt: n, Reason: reason}
}

// TestRetryStartsTheNextAttempt: after a failed attempt the Director's attempt-retried starts attempt 2
// exactly as a first attempt starts, and replaying it after attempt 2 exists writes nothing.
func TestRetryStartsTheNextAttempt(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			plan2.Attempt = 2
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")), plan2)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})

			want(t, e.advance(t, retried("plan", 1, "the runner was killed")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			if got := e.attempts(t); len(got) != 2 || got[0].Outcome != store.AttemptFailed || got[1].Number != 2 || got[1].Outcome != "" {
				t.Fatalf("attempts = %+v", got)
			}
			dir := filepath.Join(e.eng.StateDir, "app", "t7", "plan-2")
			for _, f := range []string{"plan-2.inputs.json", "prompt", "launched.json"} {
				p := filepath.Join(dir, f)
				if f == "plan-2.inputs.json" {
					p = filepath.Join(e.eng.DocumentDir, "tasks", "app", "t7", f)
				}
				if _, err := os.Stat(p); err != nil {
					t.Errorf("attempt 2 was not started as a first attempt starts: %v", err)
				}
			}
			msgs := e.messages(t)
			if got := msgs["app.t7.attempt-retried.plan-1"]; got != "the runner was killed" {
				t.Errorf("reason message = %q; messages %v", got, msgs)
			}

			launched, docs := len(e.script.Launched()), len(e.documents(t))
			want(t, e.advance(t, retried("plan", 1, "the runner was killed")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			if got := e.attempts(t); len(got) != 2 {
				t.Errorf("replay recorded attempts: %+v", got)
			}
			if got := len(e.script.Launched()); got != launched {
				t.Errorf("replay launched again: %d launches, want %d", got, launched)
			}
			if got := len(e.documents(t)); got != docs {
				t.Errorf("replay recorded documents: %d, want %d", got, docs)
			}
			if got := len(e.messages(t)); got != len(msgs) {
				t.Errorf("replay sent messages: %v", e.messages(t))
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
		})
	}
}

func TestRetryWithoutAReasonSendsNoMessage(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	want(t, e.advance(t, retried("plan", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	if got := e.inbox(t); !slices.Equal(got, []string{"app.t7.attempt-failed.plan-1"}) {
		t.Errorf("Director inbox = %q", got)
	}
}

// TestRetryRefusedAtStartSendsNoReason: when the next attempt cannot start, the reason is not sent, so a
// later retry with another reason is not refused as a conflicting message.
func TestRetryRefusedAtStartSendsNoReason(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	planner := e.eng.Config.Profiles["planner"]
	delete(e.eng.Config.Profiles, "planner")
	if err := advanceErr(t, e, retried("plan", 1, "first reason")); !errors.Is(err, config.ErrUnknownProfile) {
		t.Fatalf("retry with no planner profile = %v, want ErrUnknownProfile", err)
	}
	if got := e.messages(t)["app.t7.attempt-retried.plan-1"]; got != "" {
		t.Errorf("a refused start sent the reason %q", got)
	}
	if got := e.attempts(t); len(got) != 1 {
		t.Errorf("a refused start recorded attempts: %+v", got)
	}
	e.eng.Config.Profiles["planner"] = planner
	want(t, e.advance(t, retried("plan", 1, "second reason")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	if got := e.messages(t)["app.t7.attempt-retried.plan-1"]; got != "second reason" {
		t.Errorf("reason message = %q, want the second reason", got)
	}
}

// TestRetryOfAStoppedAttempt: a stopped attempt, such as one whose launcher resume settled, is retried too.
func TestRetryOfAStoppedAttempt(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[1], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	if _, err := e.h.Records.FinishAttempt("stop-1", "app", "t7", "plan", 1, store.AttemptStopped); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})
	want(t, e.advance(t, retried("plan", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
}

// TestRetryIsRefusedUnlessTheLatestAttemptFailedOrStopped: a running, succeeded, non-latest or unknown
// attempt is refused with ErrEvent and nothing is recorded or launched.
func TestRetryIsRefusedUnlessTheLatestAttemptFailedOrStopped(t *testing.T) {
	refused := func(t *testing.T, e *env, ev interp.Event) {
		t.Helper()
		attempts, launched, msgs := e.attempts(t), len(e.script.Launched()), len(e.messages(t))
		if err := advanceErr(t, e, ev); !errors.Is(err, interp.ErrEvent) {
			t.Fatalf("Advance(%+v) = %v, want ErrEvent", ev, err)
		}
		if got := e.attempts(t); !slices.Equal(got, attempts) {
			t.Errorf("a refused retry recorded attempts: %+v", got)
		}
		if got := len(e.script.Launched()); got != launched {
			t.Errorf("a refused retry launched")
		}
		if got := len(e.messages(t)); got != msgs {
			t.Errorf("a refused retry sent messages: %v", e.messages(t))
		}
	}
	t.Run("running", func(t *testing.T) {
		e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")))
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		refused(t, e, retried("plan", 1, "impatient"))
	})
	t.Run("succeeded", func(t *testing.T) {
		e := newEnv(t, backends[0], straightScript(t)[0])
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		refused(t, e, retried("plan", 1, "again"))
	})
	t.Run("not the latest", func(t *testing.T) {
		exps := []interptest.Expectation{planExpectation(t, interptest.Fail("no plan"))}
		for n := 2; n <= 3; n++ {
			x := planExpectation(t, interptest.Fail("no plan"))
			x.Attempt = n
			exps = append(exps, x)
		}
		e := newEnv(t, backends[0], exps...)
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		e.advance(t, retried("plan", 1, ""))
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
		e.advance(t, retried("plan", 2, ""))
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 3})
		refused(t, e, retried("plan", 1, ""))
	})
	t.Run("unknown", func(t *testing.T) {
		e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")))
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		refused(t, e, retried("plan", 2, ""))
		refused(t, e, retried("ship", 1, ""))
	})
}

// TestRetryCarriesPendingFeedback: a looped attempt that failed did not serve the feedback it was given,
// so the retried attempt is given it again.
func TestRetryCarriesPendingFeedback(t *testing.T) {
	words := "changes_requested\nTighten the scope."
	feedback := map[string]string{"feedback": "sha256:" + sum([]byte(words))}
	v2, err := os.ReadFile(filepath.Join(interptest.Testdata(), "docs", "spec-v2.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan2 := interptest.Expectation{Step: "plan", Attempt: 2, Profile: "planner", Inputs: feedback,
		Standards: []string{"standards/go.md"}, Respond: interptest.Fail("no plan")}
	plan3 := plan2
	plan3.Attempt, plan3.Respond = 3, interptest.Succeed("replanned", interptest.File{Path: "spec.md", Data: v2})
	e := newEnvDefinition(t, backends[1], loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})), plan2, plan3)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", words)
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 2})
	want(t, e.advance(t, retried("plan", 2, "")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 3})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 3}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
}
