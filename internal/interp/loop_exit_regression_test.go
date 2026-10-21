package interp_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestApprovalLoopExitAcceptedSpec(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			plan2 := plan
			plan2.Attempt = 2
			plan2.Inputs = map[string]string{"feedback": "sha256:" + sum([]byte("changes_requested"))}
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), plan, plan2, buildExp(t, 1, head, ""))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", "changes_requested")
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
			e.answer(t, "accept-2", "changes_requested")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-x2"})
			e.answer(t, "accept-x2", "accept")
			for range 2 {
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-x2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			}
			if doc := e.documents(t)["accept-x2-spec"]; doc.SHA256 != sum(specV1(t)) {
				t.Fatalf("accepted spec = %+v", doc)
			}
		})
	}
}

func TestHumanReviewLoopExitResolvesOutcome(t *testing.T) {
	for _, stale := range []bool{false, true} {
		for _, kind := range []string{"review", "document-review"} {
			for _, b := range backends {
				t.Run(b.name+"/"+kind+map[bool]string{false: "/current", true: "/stale"}[stale], func(t *testing.T) {
					def := loadWorkflow(t, "loop")
					if kind == "document-review" {
						def.Steps[3].Type = kind
						def.Steps[3].Inputs = map[string]workflow.Refs{"subject": {"plan.spec"}}
					}
					def.Steps[3].Settings = map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}
					e := newEnvDefinition(t, b, def,
						planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
						buildExp(t, 1, head, ""), buildExp(t, 2, head2, "sha256:"+sum([]byte("changes_requested"))))
					e.accepted(t)
					e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
					e.answer(t, "review-1", "changes_requested")
					e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-1"})
					e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
					e.answer(t, "review-2", "changes_requested")
					e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-2"})
					e.answer(t, "review-x2", "accept")
					if stale {
						e.eng.StandardsRevision = "standards-2"
					}
					for range 2 {
						if stale {
							if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}); !errors.Is(err, interp.ErrReviewGate) {
								t.Fatalf("Advance = %v, want ErrReviewGate", err)
							}
						} else {
							want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
						}
						if o := recordedExitOutcome(t, e); o.Revisions.StandardsRevision != "standards-1" {
							t.Fatalf("exit revisions = %+v", o.Revisions)
						}
					}
				})
			}
		}

	}

}

func TestDocumentReviewDeliveryGate(t *testing.T) {
	for _, stale := range []bool{false, true} {
		for _, b := range backends {
			t.Run(b.name+map[bool]string{false: "/current", true: "/stale"}[stale], func(t *testing.T) {
				def := loadWorkflow(t, "loop")
				review := workflow.Step{ID: "doc-review", Type: "document-review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}, Inputs: map[string]workflow.Refs{"subject": {"plan.spec"}}}
				def.Steps = append(def.Steps[:1], append([]workflow.Step{review}, def.Steps[1:]...)...)
				def.Steps[4].Settings = map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}
				e := newEnvDefinition(t, b, def, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})), buildExp(t, 1, head, ""))
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
				e.answer(t, "doc-review-1", "approved")
				e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "doc-review-1"})
				e.answer(t, "accept-1", "approved")
				e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
				if stale {
					e.eng.StandardsRevision = "standards-2"
				}
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				e.answer(t, "review-1", "approved")
				if stale {
					if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-1"}); !errors.Is(err, interp.ErrReviewGate) {
						t.Fatalf("Advance = %v, want ErrReviewGate", err)
					}
				} else {
					want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-1"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				}
			})
		}
	}
}
