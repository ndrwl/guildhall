package interp_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const shippedBy = "https://github.com/owner/app/pull/351"

// TestAttemptFailedNamesTheOutcome: a plan whose failed result reports an already-delivered outcome passes the result
// check and intake and ends failed with no rejection; with no forge to check its pull request, no close decision is
// opened, and the attempt-failed message names the outcome, why it was not asked and the Director's next move; the
// task stays open, waiting on the Director. A nothing-to-do outcome needs no evidence, so it asks the Guildmaster
// (close_decision_test.go).
func TestAttemptFailedNamesTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome adapter.Outcome
		says    []string
	}{
		{"already delivered", adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, ByPR: shippedBy},
			[]string{"It reports: already delivered by " + shippedBy + ".", "Guildhall did not ask to close it: not supported by the engine: the forge does not read pull requests", "guildhall slice delivered"}},
	} {
		for _, b := range backends {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				e := newEnv(t, b, planExpectation(t, interptest.Report("Already delivered by "+shippedBy, tc.outcome)))
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				said := checkBefore(t, e, "plan", 1)
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})
				said.agrees(t, e, "plan", 1)
				if a := e.attempts(t)[0]; a.Outcome != store.AttemptFailed || a.IntakeRejection != "" {
					t.Errorf("attempt = %+v; want failed with no rejection", a)
				}
				text := e.messages(t)["app.t7.attempt-failed.plan-1"]
				for _, s := range append([]string{"Attempt 1 of step plan failed"}, tc.says...) {
					if !strings.Contains(text, s) {
						t.Errorf("attempt-failed = %q; want %q", text, s)
					}
				}
				if got := e.task(t).State; got != store.TaskOpen {
					t.Errorf("task state = %s, want open", got)
				}
			})
		}
	}
}

// TestPlainFailureNamesNoOutcome: a failed result without an outcome keeps the plain attempt-failed text.
func TestPlainFailureNamesNoOutcome(t *testing.T) {
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	if text := e.messages(t)["app.t7.attempt-failed.plan-1"]; text != "Attempt 1 of step plan failed; the Director decides what happens next." {
		t.Errorf("attempt-failed = %q", text)
	}
}

// TestReviewOutcomeIsRefused: a review, which reports no outcome, has a result carrying one refused by the result
// check and by intake, with a reason naming the steps that may.
func TestReviewOutcomeIsRefused(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			script := straightScript(t)
			script[2].Respond = interptest.Report("Already delivered.", adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, ByPR: shippedBy})
			e := newEnv(t, b, script...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", "approved")
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1},
				"step review is a review step, which reports no outcome; only research, plan and build report")
		})
	}
}

// TestOutcomeBySliceOnlyForAnEarlierSlice: before the slice range the task is one pull request, so a plan's
// by_slice is refused; slice 1's build cannot name itself or a later slice; slice 2's build may name slice 1.
func TestOutcomeBySliceOnlyForAnEarlierSlice(t *testing.T) {
	bySlice := func(j int) interptest.Response {
		return interptest.Report("Already delivered by slice 1", adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, BySlice: j})
	}
	t.Run("plan", func(t *testing.T) {
		script := sliceScript(t, threeSlices)
		script[0].Respond = bySlice(1)
		e, _, _ := newSliceEnv(t, backends[0], true, script...)
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1},
			"step plan runs before the slice range, where task t7 delivers one pull request: name the pull request that shipped it with by_pr")
	})
	t.Run("slice 1", func(t *testing.T) {
		script := toBuildOf(t, 1)
		script[len(script)-1].Respond = bySlice(1)
		e, _, _ := newSliceEnv(t, backends[0], true, script...)
		want(t, e.toAccepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
		assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}, "by_slice 1 is not a slice earlier than this attempt's slice 1")
	})
	t.Run("slice 2", func(t *testing.T) {
		script := toBuildOf(t, 2)
		script[len(script)-1].Respond = bySlice(1)
		e, _, _ := newSliceEnv(t, backends[0], true, script...)
		e.toAccepted(t)
		e.toMergeDecision(t, 1)
		build := workflow.SliceStep("build", 2)
		want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitAttempt, Step: build, Attempt: 1})
		said := checkBefore(t, e, build, 1)
		// Slice 1 merged, so the evidence holds and the Guildmaster is asked (close_decision_test.go).
		id := interp.CloseDecision(build, 1)
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: build, Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: build, Decision: id})
		said.agrees(t, e, build, 1)
	})
	t.Run("one-PR task", func(t *testing.T) {
		script := toBuildOf(t, 1)
		script[0].Respond = interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})
		script[len(script)-1].Inputs = map[string]string{"spec": "sha256:" + sum(specV1(t))}
		script[len(script)-1].Respond = bySlice(1)
		e, _, _ := newSliceEnv(t, backends[0], true, script...)
		want(t, e.toAccepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
		assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}, "task t7 delivers one pull request: name the pull request that shipped it with by_pr")
	})
}

// TestRetryPromptQuotesTheOutcome: the retry of an attempt that reported an outcome binds it and quotes its label.
func TestRetryPromptQuotesTheOutcome(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	o := adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, ByPR: shippedBy}
	e := newEnv(t, backends[0], planExpectation(t, interptest.Report("Already delivered by "+shippedBy, o)), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	want(t, e.advance(t, retried("plan", 1, "that PR only did half of it")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	if got, _ := boundRetry(t, e, "plan", 2)["reported"].(map[string]any); got["kind"] != string(o.Kind) || got["by_pr"] != shippedBy {
		t.Errorf("bound retry outcome = %v", got)
	}
	wantRetrySection(t, retryPrompt(t, e, "plan", 2), "This is a retry of attempt 1, which failed.",
		"Reported outcome:\n\n```text\nalready delivered by "+shippedBy+"\n```", "that PR only did half of it")
}
