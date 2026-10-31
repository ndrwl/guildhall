package interp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// noReviewEnv is publish.json with its review steps and review inputs removed, so no review or document-review step
// comes before deliver, under merge requirements m, run up to the build's result.
func noReviewEnv(t *testing.T, m config.MergeRequires) (*env, *forgetest.Fake) {
	t.Helper()
	def := publishDefinition(t)
	def.Steps = slices.DeleteFunc(def.Steps, func(s workflow.Step) bool { return s.Type == "review" })
	for i := range def.Steps {
		delete(def.Steps[i].Inputs, "review")
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	e := newEnvTasks(t, backends[0], def, nil, straightScript(t)[:2]...)
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	f.AddCommit(repoPath, head)
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	e.requires(m)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	return e, f
}

var built = interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}

// TestMergeRequiresReviewStep: with artisan_approval required, a deliver step with no review or document-review step
// before it fails the review gate in the floor's wording, whatever guildmaster_approval is: nothing merges, no
// authorization is recorded and no merge decision is opened (d107 H16). A task override that turns artisan_approval
// back on is named in the error. ci_green is off throughout: the workflow has no validate step either, and a required
// ci_green would fail first.
func TestMergeRequiresReviewStep(t *testing.T) {
	for _, c := range []struct {
		name     string
		requires config.MergeRequires
		override bool
	}{
		{"guildmaster_approval=true", config.MergeRequires{CIGreen: ptr(false)}, false},
		{"guildmaster_approval=false", config.MergeRequires{CIGreen: ptr(false), GuildmasterApproval: ptr(false)}, false},
		{"override", config.MergeRequires{CIGreen: ptr(false), ArtisanApproval: ptr(false)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, f := noReviewEnv(t, c.requires)
			if c.override {
				e.mergeOverrides("t7", "merge.requires.artisan_approval")
			}
			err := advanceErr(t, e, built)
			if !errors.Is(err, interp.ErrReviewGate) || !strings.Contains(err.Error(), `review gate: deliver step "deliver" takes no review outcome`) {
				t.Fatalf("advance = %v, want ErrReviewGate: deliver step \"deliver\" takes no review outcome", err)
			}
			if got := strings.Contains(err.Error(), "this task's override requires artisan_approval"); got != c.override {
				t.Errorf("advance = %v, naming the override = %t, want %t", err, got, c.override)
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") || len(e.authorizations(t)) != 0 {
				t.Errorf("merges = %d, decisions = %q, authorizations = %q, want none", merges, e.decisionIDs(t), e.authorizations(t))
			}
		})
	}
}

// TestMergeWithoutReviewStepArtisanApprovalOff: with artisan_approval off, a deliver step with no review step before
// it still asks the merge, showing artisan_approval as not required, and the Guildmaster's merge merges.
func TestMergeWithoutReviewStepArtisanApprovalOff(t *testing.T) {
	e, f := noReviewEnv(t, config.MergeRequires{CIGreen: ptr(false), ArtisanApproval: ptr(false)})
	want(t, e.advance(t, built), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if q := e.decision(t, "deliver-1").Question; !strings.Contains(q, "- artisan_approval: not required\n") {
		t.Errorf("question = %q, want artisan_approval not required", q)
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
	if _, merges := f.Calls(); merges != 1 {
		t.Errorf("merges = %d, want 1", merges)
	}
}
