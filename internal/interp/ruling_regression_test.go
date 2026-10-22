package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

type failReviewStart struct {
	interp.Records
	failed bool
}

func (r *failReviewStart) StartCappedAttempt(id, repo, task, step string, caps store.AttemptCaps) (store.Receipt, error) {
	if step == "review" && !r.failed {
		r.failed = true
		return store.Receipt{}, errInjected
	}
	return r.Records.StartCappedAttempt(id, repo, task, step, caps)
}

func TestRulingAfterBindingDoesNotReachReplay(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			f := workflow.Finding{ID: "f1", Rule: "S2", Summary: "Missing test"}
			o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f})
			o2 := bound(t, workflow.VerdictChangesRequested, head2, []workflow.Finding{f}, workflow.Disposition{Finding: "f1", State: workflow.StillOpen})
			def := loadWorkflow(t, "loop")
			def.Steps[3].On["changes_requested"] = workflow.Rule{Goto: "build", Max: 3}
			e := newEnvDefinition(t, b, def,
				planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
				buildExp(t, 2, head2, outcomeDigest(o1)), reviewExp(t, "review", "reviewer", 2, head2, &o1, o2),
				buildExp(t, 3, head3, outcomeDigest(o2)))
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			e.eng.Records = &failReviewStart{Records: e.h.Records}
			ev := interp.Event{Kind: interp.AttemptResult, Repository: "app", Task: "t7", Step: "build", Attempt: 2}
			if _, err := e.eng.Advance(context.Background(), ev); !errors.Is(err, errInjected) {
				t.Fatalf("start = %v", err)
			}
			binding := e.documents(t)["review-2-inputs"]
			if _, err := e.h.RuleFinding("late-ruling", store.Ruling{Repository: "app", TaskID: "t7", Document: "review-1-outcome", Finding: "f1", Move: store.RulingOutOfScope, Reason: "Outside spec"}); err != nil {
				t.Fatal(err)
			}
			e.advance(t, ev)
			if got := e.documents(t)["review-2-inputs"]; !reflect.DeepEqual(got, binding) {
				t.Fatal("binding changed")
			}
			if _, ok := e.documents(t)["review-2-rulings"]; ok {
				t.Fatal("late ruling snapshot created")
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			rs, err := e.h.Records.Rulings("app", "t7")
			if err != nil || rs[0].ContestedBy != "" {
				t.Fatalf("late ruling contested: %+v, %v", rs, err)
			}
		})
	}
}

func TestLargeDisputeAndAcceptedProjection(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			fs := []workflow.Finding{{ID: "f1", Rule: "S2", Summary: strings.Repeat("a", 20<<10)}, {ID: "f2", Rule: "S3", Summary: strings.Repeat("b", 20<<10)}}
			ds := []workflow.Disposition{{Finding: "f1", State: workflow.StillOpen}, {Finding: "f2", State: workflow.StillOpen}}
			o1 := bound(t, workflow.VerdictChangesRequested, head, fs)
			o2 := bound(t, workflow.VerdictChangesRequested, head2, fs, ds...)
			rs := []store.Ruling{}
			for _, f := range fs {
				rs = append(rs, store.Ruling{Repository: "app", TaskID: "t7", Document: "review-1-outcome", Finding: f.ID, Move: store.RulingOutOfScope, Reason: strings.Repeat("r", 20<<10)})
			}
			given, _ := json.Marshal(rs)
			review := reviewExp(t, "review", "reviewer", 2, head2, &o1, o2)
			review.Inputs["rulings"] = "sha256:" + sum(given)
			def := loadWorkflow(t, "loop")
			def.Steps[3].On["changes_requested"] = workflow.Rule{Goto: "build", Max: 3}
			e := newEnvDefinition(t, b, def, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})), buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1), buildExp(t, 2, head2, outcomeDigest(o1)), review)
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			for _, r := range rs {
				if _, err := e.h.RuleFinding("rule-"+r.Finding, r); err != nil {
					t.Fatal(err)
				}
			}
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
			for range 2 {
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x2"})
			}
			q := e.decision(t, "review-x2").Question
			if len(q) > store.MaxTextBytes {
				t.Fatalf("question has %d bytes", len(q))
			}
			for _, s := range []string{"f1", "f2", "S2", "S3", "out-of-scope", "review-2-outcome", "ruling/review-1-outcome/f1", "ruling/review-1-outcome/f2"} {
				if !strings.Contains(q, s) {
					t.Errorf("question missing %q", s)
				}
			}
			e.answer(t, "review-x2", "accept")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := e.documents(t)["review-x2-outcome"].Review; !reflect.DeepEqual(got, &store.Review{Findings: fs, Dispositions: ds}) {
				t.Fatalf("accepted projection = %+v", got)
			}
		})
	}
}
