package interp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// mergeOverrides gives the engine task's overrides setting each merge-requires key to true, as `task start` does;
// the config is left as it is, since merge-requires overrides never reach it.
func (e *env) mergeOverrides(task string, keys ...string) {
	o := &interp.TaskOverrides{Repository: "app", TaskID: task, Config: e.eng.Config}
	for i, key := range keys {
		o.Active = append(o.Active, store.TaskOverride{Repository: "app", TaskID: task, Number: i + 1, Op: store.TaskOverrideSet, Key: key, Value: "true"})
	}
	e.eng.Overrides = o
}

// TestMergeOverrideNeedsTheGuildmaster: under guildmaster_approval = false, a task override turning it on opens the
// merge decision where Guildhall would have merged by itself, whose question leads with the override and marks the
// requirement as required by it; the shared config keeps the repository's requirement, and the Guildmaster's merge
// answer merges it. A sibling task's override changes nothing for this task.
func TestMergeOverrideNeedsTheGuildmaster(t *testing.T) {
	t.Run("this task", func(t *testing.T) {
		e, f := newValidateEnv(t, backends[0])
		e.requires(unattended)
		e.mergeOverrides("t7", "merge.requires.guildmaster_approval")
		want(t, e.toValidate(t), waitChecks)
		f.SetChecks(repoRemote, head, ciPassed...)
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		q := e.decision(t, "deliver-1").Question
		for _, line := range []string{
			"This task also requires your approval (its override), whatever merge_requires says.\n",
			"- guildmaster_approval: required (this task's override)\n",
			"- ci_green: holds\n",
		} {
			if !strings.Contains(q, line) {
				t.Errorf("merge question lacks %q:\n%s", line, q)
			}
		}
		if strings.Contains(q, "the repository's policies") {
			t.Errorf("merge question names the policy guard:\n%s", q)
		}
		if e.eng.Config.Repositories[0].Merge.Requires.Guildmaster() {
			t.Error("the override wrote through to the shared config")
		}
		if got := e.authorizations(t); len(got) != 0 {
			t.Fatalf("authorizations = %q, want none before the answer", got)
		}
		e.answer(t, "deliver-1", "merge")
		want(t, e.advance(t, answeredMerge), done)
		if got := mergedHead(f); got != head {
			t.Fatalf("merged head = %q, want %s", got, head)
		}
	})
	t.Run("sibling task", func(t *testing.T) {
		e, f := newValidateEnv(t, backends[0])
		e.requires(unattended)
		e.mergeOverrides("t8", "merge.requires.guildmaster_approval")
		want(t, e.toValidate(t), waitChecks)
		f.SetChecks(repoRemote, head, ciPassed...)
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
		if ids := e.decisionIDs(t); slices.Contains(ids, "deliver-1") {
			t.Errorf("decisions = %q, want no merge decision for t7", ids)
		}
	})
}

// TestMergeOverrideRecordsRequires: a task override turning ci_green on, with everything holding, merges by itself
// under an authorization that records the tightened requirements.
func TestMergeOverrideRecordsRequires(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	e.requires(config.MergeRequires{GuildmasterApproval: ptr(false), CIGreen: ptr(false)})
	e.mergeOverrides("t7", "repository.merge.requires.ci_green")
	want(t, e.toValidate(t), waitChecks)
	f.SetChecks(repoRemote, head, ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	a := e.pullRequestDoc(t, "deliver-1-authorized")
	requires := a["requires"].(map[string]any)
	if requires["guildmaster_approval"] != false || requires["ci_green"] != true {
		t.Errorf("authorized requires = %v, want ci_green on and guildmaster_approval off", requires)
	}
	for _, c := range a["conditions"].([]any) {
		if c := c.(map[string]any); c["requirement"] == "ci_green" && c["required_by"] != "this task's override" {
			t.Errorf("ci_green condition = %v, want required by this task's override", c)
		}
	}
}

// TestMergeOverrideHardGates: a gate the repository turned off, turned back on by a task override, is the gate's
// error again when it fails, naming the override; without the override the same task opens the merge decision
// (TestMergeRequiresHardGates).
func TestMergeOverrideHardGates(t *testing.T) {
	reviewDone := interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}
	t.Run("ci_green", func(t *testing.T) {
		e, f := rebuildEnv(t, config.MergeRequires{CIGreen: ptr(false)}, false)
		e.mergeOverrides("t7", "merge.requires.ci_green")
		err := advanceErr(t, e, reviewDone)
		if !errors.Is(err, interp.ErrValidationGate) || !strings.Contains(err.Error(), "this task's override requires ci_green") {
			t.Fatalf("advance = %v, want ErrValidationGate naming the override", err)
		}
		if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") {
			t.Errorf("merges = %d, decisions = %q, want neither", merges, e.decisionIDs(t))
		}
	})
	t.Run("artisan_approval", func(t *testing.T) {
		e, _ := rebuildEnv(t, config.MergeRequires{ArtisanApproval: ptr(false), CIGreen: ptr(false)}, true)
		e.mergeOverrides("t7", "merge.requires.artisan_approval")
		err := advanceErr(t, e, reviewDone)
		if !errors.Is(err, interp.ErrReviewGate) || !strings.Contains(err.Error(), "this task's override requires artisan_approval") {
			t.Fatalf("advance = %v, want ErrReviewGate naming the override", err)
		}
	})
}

// TestMergeOverrideNothingOpen: a task override turning nothing_open on opens the merge decision for an open finding,
// marked as required by the override.
func TestMergeOverrideNothingOpen(t *testing.T) {
	e, f := newRequiresEnv(t, backends[0], config.MergeRequires{GuildmasterApproval: ptr(false), NothingOpen: ptr(false)}, approvedWith(t, ""))
	e.mergeOverrides("t7", "merge.requires.nothing_open")
	want(t, e.toValidate(t), waitChecks)
	f.SetChecks(repoRemote, head, ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if q := e.decision(t, "deliver-1").Question; !strings.Contains(q, "This task also requires nothing_open (its override)") ||
		!strings.Contains(q, "- nothing_open: required (this task's override)\n") {
		t.Errorf("merge question = %q, want nothing_open required by the override", q)
	}
}

// TestMergeOverrideWithPolicyChange: a head that changes the policy file, on a task whose override also requires the
// Guildmaster's approval, gives both reasons: the policy guard's lead unchanged, the override's, and both in the
// condition.
func TestMergeOverrideWithPolicyChange(t *testing.T) {
	e, _, _ := policyEnv(t, backends[0], head)
	e.mergeOverrides("t7", "merge.requires.guildmaster_approval")
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	q := e.decision(t, "deliver-1").Question
	for _, line := range []string{
		"The Guildmaster's answer is required because the head changes .guildhall/policies.md, the repository's policies, whatever merge_requires says.\n",
		"This task also requires your approval (its override), whatever merge_requires says.\n",
		"- guildmaster_approval: required (the head changes .guildhall/policies.md; this task's override)\n",
	} {
		if !strings.Contains(q, line) {
			t.Errorf("merge question lacks %q:\n%s", line, q)
		}
	}
}

// TestMergeOverrideAfterAuthorization: an override added after the head's merge was authorized does not retract the
// authorization: the queued head merges though the gate the override adds does not hold for it.
func TestMergeOverrideAfterAuthorization(t *testing.T) {
	reviewDone := interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}
	e, f := rebuildEnv(t, config.MergeRequires{GuildmasterApproval: ptr(false), CIGreen: ptr(false)}, false)
	blocked := &mergeabilityAfter{Fake: f, state: forge.Mergeability{Base: baseHead, State: "conflicting", Behind: 1, Files: []string{"go.mod"}}}
	e.eng.Forge = blocked
	want(t, e.advance(t, reviewDone), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
	if got := e.authorizations(t); len(got) != 1 {
		t.Fatalf("authorizations = %q, want one", got)
	}
	e.mergeOverrides("t7", "merge.requires.ci_green")
	blocked.mu.Lock()
	blocked.clear = 100
	blocked.mu.Unlock()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if _, merges := f.Calls(); merges != 1 {
		t.Errorf("merges = %d, want 1", merges)
	}
}
