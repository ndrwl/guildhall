package interp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestChangesRequestedWithoutARuleLoopsToBuild is d16's case: a /1 changes_requested outcome whose only finding
// cites no rule, read as a suggestion, sends the build back with the outcome as feedback, and past max opens the
// loop exit; the task never reaches publish or deliver.
func TestChangesRequestedWithoutARuleLoopsToBuild(t *testing.T) {
	v1 := func(h string) workflow.ReviewOutcome {
		o := bound(t, workflow.VerdictChangesRequested, h, nil)
		o.Schema, o.Summary = workflow.ReviewOutcomeSchemaV1, ""
		o.Suggestions = []workflow.Suggestion{{ID: "d16-lab-evidence-missing", Summary: "The lab evidence the spec asks for is missing."}}
		return o
	}
	o1, o2 := v1(head), v1(head2)
	d1 := "sha256:" + sum(interptest.ReviewV1Data(o1))
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			review2 := reviewV1Exp(t, "review", "reviewer", 2, head2, nil, o2)
			review2.Inputs["previous"] = d1
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"),
				planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, head, ""), reviewV1Exp(t, "review", "reviewer", 1, head, nil, o1),
				buildExp(t, 2, head2, d1), review2)
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			// buildExp holds build 2 to the outcome's digest on its feedback port.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x2"})
			if d := e.decision(t, "review-x2"); d.Kind != store.DecisionLoopExit {
				t.Errorf("loop exit = %+v", d)
			}
			for name := range e.documents(t) {
				if strings.HasPrefix(name, "publish-") || strings.HasPrefix(name, "deliver-") {
					t.Errorf("document %s recorded past a changes_requested review", name)
				}
			}
			for _, id := range e.decisionIDs(t) {
				if strings.HasPrefix(id, "deliver-") {
					t.Errorf("decision %s opened past a changes_requested review", id)
				}
			}
		})
	}
}

// TestPublishGuardRefusesAReviewThatIsNotApproved: should routing ever let a changes_requested review through,
// publish runs the review gate over the reviews before it, bound to the head it would publish. Under a required
// artisan_approval it pushes nothing, records <publish>-refused and fails the advance with ErrReviewGate; when
// artisan_approval is not required the failing gate is recorded as a reason and publish goes on.
//
// The routing regression is simulated by recording the Guildmaster's sign-off outcome as changes_requested before
// the approved answer is read: the sign-off routes on the answer, the gate reads the outcome.
func TestPublishGuardRefusesAReviewThatIsNotApproved(t *testing.T) {
	for _, b := range backends {
		for _, required := range []bool{true, false} {
			name := b.name + "/artisan_approval required"
			if !required {
				name = b.name + "/artisan_approval not required"
			}
			t.Run(name, func(t *testing.T) {
				e, f := newPublishEnv(t, b)
				e.requires(config.MergeRequires{ArtisanApproval: ptr(required)})
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
				e.answer(t, "accept-1", "approved")
				e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"})
				e.plant(t, "sign-off-1-outcome", "tasks/app/t7/sign-off-1/outcome.json", workflow.ReviewOutcome{
					Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.HumanReviewer, Verdict: workflow.VerdictChangesRequested,
					Summary:   "The lab evidence is missing.",
					Findings:  []workflow.Finding{{ID: "d16-lab-evidence-missing", Rule: "S2", Summary: "The lab evidence the spec asks for is missing."}},
					Revisions: bound(t, "", head, nil).Revisions,
				})
				e.answer(t, "sign-off-1", "approved")
				ev := interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}
				if !required {
					next := e.advance(t, ev)
					if next.Step != "deliver" {
						t.Fatalf("next = %+v, want deliver", next)
					}
					if _, ok := e.documents(t)["publish-1-head"]; !ok {
						t.Error("publish did not record its head")
					}
					if reason := e.gateRecord(t, "publish-1-refused")["reason"]; !strings.Contains(reason.(string), "step sign-off has no outcome approving") {
						t.Errorf("recorded reason = %q", reason)
					}
					return
				}
				for range 2 {
					if err := advanceErr(t, e, ev); !errors.Is(err, interp.ErrReviewGate) {
						t.Fatalf("Advance = %v, want ErrReviewGate", err)
					}
				}
				if prs := f.PullRequests(repoRemote); len(prs) != 0 {
					t.Errorf("published behind a failing review gate: %+v", prs)
				}
				docs := e.documents(t)
				if _, ok := docs["publish-1-head"]; ok {
					t.Error("publish recorded a head behind a failing review gate")
				}
				if _, ok := docs["publish-1-refused-2"]; ok {
					t.Error("a repeated refusal was recorded twice")
				}
				rec := e.gateRecord(t, "publish-1-refused")
				if rec["head"] != head || rec["required"] != true || !strings.Contains(rec["reason"].(string), "step sign-off has no outcome approving") {
					t.Errorf("refusal = %+v", rec)
				}
			})
		}
	}
}

// plant records document name at path p holding o, as if the engine had written it.
func (e *env) plant(t *testing.T, name, p string, o workflow.ReviewOutcome) {
	t.Helper()
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Records.PutDocument("plant:"+name, store.DocumentRef{Repository: "app", TaskID: "t7", Name: name, Path: p, SHA256: sum(data)}); err != nil {
		t.Fatal(err)
	}
}

// gateRecord reads a recorded publish review-gate document.
func (e *env) gateRecord(t *testing.T, name string) map[string]any {
	t.Helper()
	return e.pullRequestDoc(t, name)
}
