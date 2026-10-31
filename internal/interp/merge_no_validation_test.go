package interp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
)

// noValidationEnv is publish.json, whose deliver step takes no validation input, under merge requirements m, run up
// to the Guildmaster's sign-off answer; the forge reports no checks for the head.
func noValidationEnv(t *testing.T, m config.MergeRequires) (*env, *forgetest.Fake) {
	t.Helper()
	e, f := newPublishEnv(t, backends[0])
	e.requires(m)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
	e.answer(t, "sign-off-1", "approved")
	return e, f
}

var signedOff = interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}

// TestMergeRequiresValidationInput: with ci_green required, a deliver step that takes no validation input fails the
// validation gate in the floor's wording, whatever guildmaster_approval is: nothing merges, no authorization is
// recorded and no merge decision is opened (d64 M1). A task override that turns ci_green back on is named in the
// error.
func TestMergeRequiresValidationInput(t *testing.T) {
	for _, c := range []struct {
		name     string
		requires config.MergeRequires
		override bool
	}{
		{"guildmaster_approval=false", config.MergeRequires{GuildmasterApproval: ptr(false)}, false},
		{"guildmaster_approval=true", config.MergeRequires{}, false},
		{"override", config.MergeRequires{GuildmasterApproval: ptr(false), CIGreen: ptr(false)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, f := noValidationEnv(t, c.requires)
			if c.override {
				e.mergeOverrides("t7", "merge.requires.ci_green")
			}
			err := advanceErr(t, e, signedOff)
			if !errors.Is(err, interp.ErrValidationGate) || !strings.Contains(err.Error(), `deliver step "deliver" takes no validate result`) {
				t.Fatalf("advance = %v, want ErrValidationGate: deliver step \"deliver\" takes no validate result", err)
			}
			if got := strings.Contains(err.Error(), "this task's override requires ci_green"); got != c.override {
				t.Errorf("advance = %v, naming the override = %t, want %t", err, got, c.override)
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") || len(e.authorizations(t)) != 0 {
				t.Errorf("merges = %d, decisions = %q, authorizations = %q, want none", merges, e.decisionIDs(t), e.authorizations(t))
			}
		})
	}
}

// TestMergeWithoutValidationInputCIGreenOff: with ci_green off, a deliver step that takes no validation input still
// asks the merge, showing ci_green as not required, and the Guildmaster's merge merges.
func TestMergeWithoutValidationInputCIGreenOff(t *testing.T) {
	e, f := noValidationEnv(t, config.MergeRequires{CIGreen: ptr(false)})
	want(t, e.advance(t, signedOff), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if q := e.decision(t, "deliver-1").Question; !strings.Contains(q, "- ci_green: not required\n") {
		t.Errorf("question = %q, want ci_green not required", q)
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
	if _, merges := f.Calls(); merges != 1 {
		t.Errorf("merges = %d, want 1", merges)
	}
}
