package interp_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestDirectorRulings: the Director rules review 1's rule-cited finding f1 out of scope; review 2 is given
// the ruling on port rulings. A reviewer who withdraws f1 lets the step complete; one who keeps it open
// contests the ruling, once, and the disputed blocker reaches the Guildmaster as a loop-exit decision
// before max is exhausted.
func TestDirectorRulings(t *testing.T) {
	f1 := workflow.Finding{ID: "f1", Rule: "S2", File: "a.go", Line: 3, Summary: "No red test shown."}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
	ruling := store.Ruling{Repository: "app", TaskID: "t7", Document: "review-1-outcome", Finding: "f1", Move: store.RulingOutOfScope, Reason: "Spec §2 leaves tests to the follow-up."}
	given, err := json.Marshal([]store.Ruling{ruling})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		o2   workflow.ReviewOutcome
		next interp.Next
	}{
		{"withdrawn", bound(t, workflow.VerdictApproved, head2, nil, workflow.Disposition{Finding: "f1", State: workflow.Withdrawn}),
			interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"}},
		{"contested", bound(t, workflow.VerdictChangesRequested, head2, []workflow.Finding{f1}, workflow.Disposition{Finding: "f1", State: workflow.StillOpen}),
			interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x2"}},
	} {
		for _, b := range backends {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				def := loadWorkflow(t, "loop")
				def.Steps[3].On["changes_requested"] = workflow.Rule{Goto: "build", Max: 3}
				review2 := reviewExp(t, "review", "reviewer", 2, head2, &o1, tc.o2)
				review2.Inputs["rulings"] = "sha256:" + sum(given)
				exps := []interptest.Expectation{planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
					buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
					buildExp(t, 2, head2, outcomeDigest(o1)), review2}
				if tc.name == "contested" {
					exps = append(exps, buildExp(t, 3, head3, outcomeDigest(tc.o2)))
				}
				e := newEnvDefinition(t, b, def, exps...)
				e.accepted(t)
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
				if got := e.documents(t)["review-1-outcome"].Review; !reflect.DeepEqual(got, &store.Review{Findings: []workflow.Finding{f1}}) {
					t.Fatalf("review-1-outcome projects %+v", got)
				}
				if _, err := e.h.RuleFinding("ruling:1", ruling); err != nil {
					t.Fatal(err)
				}
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
				if tc.name == "contested" {
					if _, err := e.h.Records.FinishAttempt("launcher-review-2", "app", "t7", "review", 2, store.AttemptSucceeded); err != nil {
						t.Fatal(err)
					}
					e.eng.Records = &interruptedIntake{Records: e.h.Records, failContest: true}
					if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.AttemptResult, Repository: "app", Task: "t7", Step: "review", Attempt: 2}); err == nil {
						t.Fatal("expected interrupted contest")
					}
				}
				for range 2 { // a replayed advance contests nothing twice
					want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), tc.next)
				}
				rulings, err := e.h.Records.Rulings("app", "t7")
				if err != nil || len(rulings) != 1 {
					t.Fatalf("Rulings = %+v, %v", rulings, err)
				}
				if tc.name == "withdrawn" {
					if rulings[0].ContestedBy != "" {
						t.Errorf("a withdrawn finding contested its ruling: %+v", rulings[0])
					}
					return
				}
				if rulings[0].ContestedBy != "review-2" {
					t.Errorf("ruling contested by %q, want review-2", rulings[0].ContestedBy)
				}
				d := e.decision(t, "review-x2")
				for _, part := range []string{"contested", "f1", "S2", "out-of-scope", ruling.Reason, head2} {
					if !strings.Contains(d.Question, part) {
						t.Errorf("dispute question %q does not quote %q", d.Question, part)
					}
				}
				// continue upholds the finding: the loop goes on within max.
				e.answer(t, "review-x2", "continue")
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			})
		}
	}
}
