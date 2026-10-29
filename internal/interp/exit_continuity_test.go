package interp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func recordedExitOutcome(t *testing.T, e *env) *workflow.ReviewOutcome {
	t.Helper()
	doc := e.documents(t)["review-x2-outcome"]
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(doc.Path)))
	if err != nil {
		t.Fatal(err)
	}
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestArtisanExitPreservesFindings(t *testing.T) {
	for _, kind := range []string{"review", "document-review"} {
		for _, b := range backends {
			t.Run(b.name+"/"+kind, func(t *testing.T) {
				def := loadWorkflow(t, "loop")
				findings := []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "Missing regression"}}
				suggestions := []workflow.Suggestion{{ID: "f2", Summary: "Naming suggestion"}}
				dispositions := []workflow.Disposition{{Finding: "f1", State: workflow.Unresolved}}
				o1 := bound(t, workflow.VerdictChangesRequested, head, findings)
				o1.Suggestions = suggestions
				o2 := bound(t, workflow.VerdictChangesRequested, head2, findings, dispositions...)
				o2.Suggestions = suggestions
				if kind == "document-review" {
					def.Steps[3].Type = kind
					def.Steps[3].Inputs = map[string]workflow.Refs{"subject": {"plan.spec"}}
					revisions := workflow.Revisions{Subject: workflow.Subject{DocumentDigest: specDigest(t)}, StandardsRevision: "standards-1"}
					o1.Revisions, o2.Revisions = revisions, revisions
				}
				r1 := reviewExp(t, "review", "reviewer", 1, head, nil, o1)
				r2 := reviewExp(t, "review", "reviewer", 2, head2, &o1, o2)
				if kind == "document-review" {
					r1.Inputs = map[string]string{"subject": specDigest(t)}
					r2.Inputs = map[string]string{"subject": specDigest(t), "previous": outcomeDigest(o1)}
					r1.Standards, r2.Standards = []string{"standards/go.md"}, []string{"standards/go.md"}
				}
				accepted := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.HumanReviewer, Verdict: workflow.VerdictApproved,
					Summary:  "Accepted by the Guildmaster at loop exit in decision review-x2; 1 finding(s) stay unresolved.",
					Findings: findings, Suggestions: suggestions, Dispositions: dispositions, Revisions: o2.Revisions}
				if err := accepted.Validate(); err != nil {
					t.Fatal(err)
				}
				acceptedData, err := json.Marshal(accepted)
				if err != nil {
					t.Fatal(err)
				}
				next := o2
				next.Verdict, next.Findings, next.Dispositions = workflow.VerdictApproved, nil, nil
				next.Revisions.StandardsRevision = "standards-2"
				r3 := reviewExp(t, "review", "reviewer", 3, head2, nil, next)
				if kind == "document-review" {
					r3.Inputs = map[string]string{"subject": specDigest(t)}
					r3.Standards = []string{"standards/go.md"}
				}
				r3.Inputs["previous"] = "sha256:" + sum(acceptedData)
				e := newEnvDefinition(t, b, def,
					planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
					buildExp(t, 1, head, ""), r1, buildExp(t, 2, head2, outcomeDigest(o1)), r2, r3)
				e.accepted(t)
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x2"})
				e.answer(t, "review-x2", "accept")
				for range 2 {
					want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
					o := recordedExitOutcome(t, e)
					if o.Reviewer != workflow.HumanReviewer || o.Verdict != workflow.VerdictApproved || o.Blocking() || o.Revisions != o2.Revisions || !reflect.DeepEqual(o.Findings, findings) || !reflect.DeepEqual(o.Suggestions, suggestions) || !reflect.DeepEqual(o.Dispositions, dispositions) || o.Summary != accepted.Summary {
						t.Fatalf("accepted outcome lost originating review: %+v", o)
					}
				}
				// The Guildmaster accepted f1 for the loop, not for the merge: it still holds nothing_open and is listed.
				q := e.decision(t, "deliver-1").Question
				for _, part := range []string{"- nothing_open: fails: review-x2-outcome has 1 unresolved and 0 disputed finding(s), 1 suggestion(s), not counted\n",
					"- review-x2-outcome finding f1 (S2, unresolved): Missing regression\n", "- review-x2-outcome suggestion f2: Naming suggestion\n"} {
					if !strings.Contains(q, part) {
						t.Errorf("merge question %q does not contain %q", q, part)
					}
				}
				e.eng.StandardsRevision = "standards-2"
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 3})
				assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 3}, "f1")
			})
		}
	}
}
