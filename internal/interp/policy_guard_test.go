package interp_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/policies"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// policyEnv is validate.json under guildmaster_approval = false, through validate with green checks on head, with the
// queue's workspace double installed and the heads in changing scripted to change the policy file.
func policyEnv(t *testing.T, b backend, changing ...string) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	e, f := newValidateEnv(t, b)
	e.requires(unattended)
	want(t, e.toValidate(t), waitChecks)
	ws := &queueWorkspaces{Workspaces: gittest.NewWorkspaces(), tip: head}
	for _, h := range changing {
		ws.SetChanges(h, policies.Path)
	}
	e.eng.Workspaces = ws
	f.SetChecks(repoRemote, head, ciPassed...)
	return e, f, ws
}

// TestPolicyChangeNeedsTheGuildmaster: under guildmaster_approval = false, a head that changes the policy file opens
// the merge decision, whose question names the change, and Guildhall records no authorization by itself; the
// Guildmaster's merge answer merges it.
func TestPolicyChangeNeedsTheGuildmaster(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, _ := policyEnv(t, b, head)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			d := e.decision(t, "deliver-1")
			for _, line := range []string{
				"The Guildmaster's answer is required because the head changes .guildhall/policies.md, the repository's policies, whatever merge_requires says.\n",
				"- guildmaster_approval: required (the head changes .guildhall/policies.md)\n",
			} {
				if !strings.Contains(d.Question, line) {
					t.Errorf("merge question lacks %q:\n%s", line, d.Question)
				}
			}
			if strings.Contains(d.Question, "Guildhall did not merge by itself") {
				t.Errorf("merge question blames a failing requirement:\n%s", d.Question)
			}
			if got := e.authorizations(t); len(got) != 0 {
				t.Fatalf("authorizations = %q, want none", got)
			}
			// A replay waits on the same decision and still records nothing.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if _, merges := f.Calls(); merges != 0 || len(e.authorizations(t)) != 0 {
				t.Fatalf("merges = %d, authorizations = %q before the answer", merges, e.authorizations(t))
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), done)
			if got := mergedHead(f); got != head {
				t.Fatalf("merged head = %q, want %s", got, head)
			}
		})
	}
}

// TestNoPolicyChangeMergesByItself: under guildmaster_approval = false, a head that does not change the policy file
// still merges with no decision.
func TestNoPolicyChangeMergesByItself(t *testing.T) {
	e, f, _ := policyEnv(t, backends[0])
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if ids := e.decisionIDs(t); slices.Contains(ids, "deliver-1") {
		t.Errorf("decisions = %q, want no merge decision", ids)
	}
	a := e.pullRequestDoc(t, "deliver-1-authorized")
	for _, c := range a["conditions"].([]any) {
		if c := c.(map[string]any); c["requirement"] == "guildmaster_approval" && (c["required"] != false || c["required_by"] != nil) {
			t.Errorf("guildmaster_approval = %v, want not required", c)
		}
	}
	if got := mergedHead(f); got != head {
		t.Fatalf("merged head = %q, want %s", got, head)
	}
}

// TestRebasedPolicyChangeFollowsReapprove: a rebase of a head that changes the policy file follows
// reapprove_after_rebase as guildmaster_approval requires, though merge_requires does not: under no_conflicts the
// rebased head needs a new decision, which names the policy change, where it would otherwise carry over.
func TestRebasedPolicyChangeFollowsReapprove(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		t.Run(map[bool]string{true: "changes the file", false: "does not"}[guarded], func(t *testing.T) {
			var changing []string
			if guarded {
				changing = []string{head, head2}
			}
			e, f, ws := policyEnv(t, backends[0], changing...)
			e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = config.RebaseNoConflicts
			base := baseN(1)
			clean(ws, base, head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
			if guarded {
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				e.answer(t, "deliver-1", "merge")
				want(t, e.advance(t, answeredMerge), deliverWait)
			} else {
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			}
			f.SetChecks(repoRemote, head2, ciPassed...)
			f.SetMergeability(forge.Mergeability{})
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if !guarded {
				want(t, next, done)
				return
			}
			want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
			d := e.decision(t, "deliver-2")
			if d.Subject == nil || d.Subject.Digest != head2 || !strings.Contains(d.Question, "reapprove_after_rebase") ||
				!strings.Contains(d.Question, "- guildmaster_approval: required (the head changes .guildhall/policies.md)\n") {
				t.Fatalf("decision = %+v", d)
			}
			if got := e.authorizations(t); len(got) != 0 {
				t.Fatalf("authorizations = %q, want none carried", got)
			}
		})
	}
}

// TestRebuiltPolicyChangeCarriesOnlyTheGuildmaster: under guildmaster_approval = false and reapprove_after_rebase =
// never, a head Guildhall authorized by itself and sent back for major conflicts does not carry that authorization over
// to a rebuilt head that changes the policy file: a merge decision opens and no authorization is recorded. The
// Guildmaster's own answer still carries over as never says.
func TestRebuiltPolicyChangeCarriesOnlyTheGuildmaster(t *testing.T) {
	t.Run("self-authorized", func(t *testing.T) {
		e, f, ws := policyEnv(t, backends[0], head3)
		e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = config.RebaseNever
		base := baseN(1)
		ws.SetRebase(base, head, git.Rebased{Marked: commitN(1), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "logo.png"}}, HeadPatchID: "p"})
		// Behind, so validate reads the checks and the front's replay meets the major conflict.
		f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
		exp := buildExp(t, 2, head3, "")
		exp.Inputs["feedback"] = interptest.AnyDigest
		e.script.Expect(exp)
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), building2)
		if got := e.authorizations(t); !slices.Equal(got, []string{"deliver-1-authorized"}) {
			t.Fatalf("authorizations = %q, want Guildhall's own deliver-1", got)
		}
		next := rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, nil))
		want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
		d := e.decision(t, "deliver-2")
		for _, line := range []string{
			"the head changes .guildhall/policies.md, and Guildhall authorized deliver-1 by itself",
			"- guildmaster_approval: required (the head changes .guildhall/policies.md)\n",
		} {
			if !strings.Contains(d.Question, line) {
				t.Errorf("merge question lacks %q:\n%s", line, d.Question)
			}
		}
		if got := e.authorizations(t); !slices.Equal(got, []string{"deliver-1-authorized"}) {
			t.Fatalf("authorizations = %q, want none carried", got)
		}
		e.answer(t, "deliver-2", "merge")
		want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-2"}), done)
		if got := mergedHead(f); got != head3 {
			t.Fatalf("merged head = %q, want %s", got, head3)
		}
	})
	t.Run("the Guildmaster's answer", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[0])
		e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = config.RebaseNever
		ws.SetChanges(head3, policies.Path)
		toMajor(t, e, f, ws)
		want(t, rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, nil)), done)
		if a := e.pullRequestDoc(t, "deliver-2-authorized"); a["carried_from"] != "deliver-1" || a["head"] != head3 {
			t.Fatalf("carried authorization = %v", a)
		}
	})
}
