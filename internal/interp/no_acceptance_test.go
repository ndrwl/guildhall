package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// researchNotes is what the scripted research of no-acceptance.json writes.
var researchNotes = []byte("# Research\n\nThe list sorts by id; the comparator ignores priority.\n")

// newNoAcceptanceEnv is no-acceptance.json, the shipped bug workflow's shape, over a fake forge holding heads 1 and 2,
// scripted through review 1, under merge requirements that do not ask for the Guildmaster.
func newNoAcceptanceEnv(t *testing.T, b backend) (*env, *forgetest.Fake) {
	t.Helper()
	e := newEnvDefinition(t, b, loadWorkflow(t, "no-acceptance"),
		interptest.Expectation{Step: "research", Attempt: 1, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("researched", interptest.File{Path: "notes.md", Data: researchNotes})},
		interptest.Expectation{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{"context": "sha256:" + sum(researchNotes)}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})},
		buildExp(t, 1, head, ""),
		reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head, nil)))
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	for _, h := range []string{head, head2} {
		f.AddCommit(repoPath, h)
	}
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	e.requires(unattended)
	return e, f
}

// toChecks runs no-acceptance.json from creation through review 1, which publishes head 1 and reaches validate.
func (e *env) toChecks(t *testing.T) {
	t.Helper()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	// Plan's spec goes straight to build: there is no acceptance step and no decision.
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), waitChecks)
}

// TestNoAcceptanceRunsToDeliver: a workflow whose build and review take plan's spec runs research, plan, build,
// review, publish, validate and deliver with no decision; the review outcome and the merge authorization are bound to
// plan's spec, and every Artisan prompt is rendered with the task's title.
func TestNoAcceptanceRunsToDeliver(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newNoAcceptanceEnv(t, b)
			e.toChecks(t)
			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)

			if ids := e.decisionIDs(t); len(ids) != 0 {
				t.Errorf("decisions = %q, want none", ids)
			}
			for name := range e.documents(t) {
				if name == "accept-1-spec" {
					t.Errorf("an accepted spec %s was recorded without an acceptance step", name)
				}
			}
			var outcome workflow.ReviewOutcome
			read(t, e, "review-1-outcome", &outcome)
			if outcome.Revisions.SpecDigest != specDigest(t) {
				t.Errorf("review outcome spec = %s, want plan's spec %s", outcome.Revisions.SpecDigest, specDigest(t))
			}
			if a := e.pullRequestDoc(t, "deliver-1-authorized"); a["spec_digest"] != specDigest(t) || a["head"] != head {
				t.Errorf("deliver-1-authorized = %v, want plan's spec %s", a, specDigest(t))
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
			for _, st := range e.stages {
				if st.Title != "Straight path" {
					t.Errorf("stage %s rendered with title %q, want the task's", st.Step.ID, st.Title)
				}
			}
		})
	}
}

// TestShippedBugWorkflowDelivers: the shipped bug workflow, exactly as shipped, runs from task creation to the merge
// and the closed task with no Guildmaster wait, its research first, each Artisan step under its role profile.
func TestShippedBugWorkflowDelivers(t *testing.T) {
	def, err := workflow.Bug()
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, def,
				interptest.Expectation{Step: "research", Attempt: 1, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("researched", interptest.File{Path: "notes.md", Data: researchNotes})},
				interptest.Expectation{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{"context": "sha256:" + sum(researchNotes)}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})},
				interptest.Expectation{Step: "build", Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": specDigest(t)}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("built", interptest.File{Path: "head", Data: headFile(head)})},
				reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head, nil)))
			f := forgetest.NewFake()
			f.SetBranch(repoRemote, "main", baseHead)
			f.AddCommit(repoPath, head)
			f.SetChecks(repoRemote, head, ciPassed...)
			e.eng.Forge = f
			e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
			e.requires(unattended)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), done)
			if ids := e.decisionIDs(t); len(ids) != 0 {
				t.Errorf("decisions = %q, want none", ids)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
		})
	}
}

// TestNoAcceptanceSendBackToPlan: after the Director sends plan back, the next build and review read the new plan
// spec, and the review outcome and the merge authorization are bound to it.
func TestNoAcceptanceSendBackToPlan(t *testing.T) {
	v2, err := os.ReadFile(filepath.Join(interptest.Testdata(), "docs", "spec-v2.md"))
	if err != nil {
		t.Fatal(err)
	}
	spec2 := "sha256:" + sum(v2)
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newNoAcceptanceEnv(t, b)
			e.toChecks(t)
			feedback := "Sort ties by id as well."
			review2 := bound(t, workflow.VerdictApproved, head2, nil)
			review2.Revisions.SpecDigest = spec2
			e.script.Expect(
				interptest.Expectation{Step: "plan", Attempt: 2, Profile: "planner", Standards: []string{"standards/go.md"},
					Inputs:  map[string]string{"context": "sha256:" + sum(researchNotes), "feedback": "sha256:" + sum([]byte(feedback))},
					Respond: interptest.Succeed("replanned", interptest.File{Path: "spec.md", Data: v2})},
				interptest.Expectation{Step: "build", Attempt: 2, Profile: "builder", Inputs: map[string]string{"spec": spec2},
					Standards: []string{"standards/go.md"}, Skills: []string{"go-tdd", "guildhall-contributing"},
					Respond: interptest.Succeed("rebuilt", interptest.File{Path: "head", Data: headFile(head2)})},
				interptest.Expectation{Step: "review", Attempt: 2, Profile: "reviewer",
					Inputs:    map[string]string{"spec": spec2, "head": "sha256:" + sum(headFile(head2)), "previous": outcomeDigest(bound(t, workflow.VerdictApproved, head, nil))},
					Standards: []string{"standards/go.md", "standards/review.md"}, Respond: interptest.Review(review2)})
			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "plan", Attempt: 1, Reason: feedback}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
			f.SetChecks(repoRemote, head2, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if a := e.pullRequestDoc(t, "deliver-1-authorized"); a["spec_digest"] != spec2 || a["head"] != head2 {
				t.Errorf("deliver-1-authorized = %v, want the new spec %s at head 2", a, spec2)
			}
		})
	}
}

// misfiled reports decision id as a loop exit instead of the approval it is, as if no approval had been recorded.
type misfiled struct {
	interp.Records
	id string
}

func (m misfiled) Decisions(repo, task string) ([]store.Decision, error) {
	ds, err := m.Records.Decisions(repo, task)
	for i := range ds {
		if ds[i].ID == m.id {
			ds[i].Kind = store.DecisionLoopExit
		}
	}
	return ds, err
}

// TestSpecGateRefusesASpecWithoutApproval: in a workflow with an acceptance step, a build whose accepted spec has no
// approval decision behind it is still refused by the spec gate, now keyed to the source's output type.
func TestSpecGateRefusesASpecWithoutApproval(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", "approved")
			e.eng.Records = misfiled{Records: e.eng.Records, id: "accept-1"}
			err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			if !errors.Is(err, interp.ErrSpecGate) {
				t.Fatalf("Advance = %v, want ErrSpecGate", err)
			}
			for _, a := range e.attempts(t) {
				if a.Step == "build" {
					t.Errorf("build attempt recorded behind a refused spec gate: %+v", a)
				}
			}
		})
	}
}
