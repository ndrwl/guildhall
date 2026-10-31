package interp_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// squashed is the commit gittest.Workspaces.Squash writes for head onto base.
func squashed(base, head string) string {
	sum := sha1.Sum([]byte(base + head + "squash"))
	return hex.EncodeToString(sum[:])
}

// minor scripts a minor_conflicts replay of from onto base, marked as commit marked.
func minor(ws *queueWorkspaces, base, from, marked string) {
	ws.SetRebase(base, from, git.Rebased{Marked: marked, MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "a.go", Lines: 4, Markers: true}}, HeadPatchID: "p-" + from[:8]})
}

// rebaserExp is rebaser attempt n of step owner, which resolves to head h and reports behaviour change bc.
func rebaserExp(owner string, n int, h string, bc bool) interptest.Expectation {
	report := `{"behaviour_change": false, "reason": "kept both sides"}`
	if bc {
		report = `{"behaviour_change": true, "reason": "the two sides disagree on the default"}`
	}
	return interptest.Expectation{Step: owner + "-rebase", Attempt: n, Profile: "rebaser",
		Inputs:    map[string]string{"spec": interptest.AnyDigest, "conflicts": interptest.AnyDigest},
		Standards: []string{"standards/go.md"},
		Respond:   interptest.Succeed("resolved", interptest.File{Path: "head", Data: headFile(h)}, interptest.File{Path: "rebase.json", Data: []byte(report)})}
}

// narrowExp is narrowed review attempt n of step owner of resolved head h, answering o.
func narrowExp(t *testing.T, owner string, n int, h string, o workflow.ReviewOutcome) interptest.Expectation {
	return interptest.Expectation{Step: owner + "-rebase-review", Attempt: n, Profile: "reviewer",
		Inputs:    map[string]string{"spec": specDigest(t), "head": "sha256:" + sum(headFile(h)), interp.ResolutionPort: interptest.AnyDigest},
		Standards: []string{"standards/go.md", "standards/review.md"}, Respond: interptest.Review(o)}
}

// withRebaser declares the rebaser profile.
func withRebaser(e *env) {
	e.eng.Config.Profiles["rebaser"] = config.Profile{Adapter: interp.MockAdapter}
}

// tier2 runs deliver's front through a minor_conflicts rebase onto baseN(1): the rebaser resolves to commitN(2)
// and the narrowed review answers o. It returns the resolved head.
func tier2(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces, o func(h string) workflow.ReviewOutcome) string {
	t.Helper()
	base := baseN(1)
	minor(ws, base, head, commitN(1))
	resolved := squashed(base, commitN(2))
	f.AddCommit(repoPath, resolved)
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
	withRebaser(e)
	ws.setTip(commitN(2))
	e.script.Expect(rebaserExp("deliver", 1, commitN(2), false), narrowExp(t, "deliver", 1, resolved, o(resolved)))
	e.answer(t, "deliver-1", "merge")
	forwards := ws.fastForwards()
	defer func() {
		if got := ws.fastForwards(); got != forwards {
			t.Errorf("the rebaser and the narrowed review fast-forwarded %d times, want none", got-forwards)
		}
	}()
	want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase", Attempt: 1})
	if ws.adopted[0] != [2]string{head, commitN(1)} {
		t.Fatalf("adopted = %v, want the worktree moved to the marked replay first", ws.adopted)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase-review", Attempt: 1})
	if last := ws.adopted[len(ws.adopted)-1]; last != [2]string{commitN(2), resolved} {
		t.Fatalf("adopted = %v, want the worktree moved to the squashed resolution", ws.adopted)
	}
	return resolved
}

// TestQueueTier2 runs a minor_conflicts rebase at the front end to end: the rebaser resolves the marked replay, the
// resolution is squashed onto the base tip, a narrowed review approves exactly the resolved hunks, and the head is
// pushed, its checks read and merged under the carried-over approval, under every reapprove_after_rebase above
// minor_conflicts; under no_conflicts and minor_conflicts it asks a new decision instead.
func TestQueueTier2(t *testing.T) {
	for _, b := range backends {
		for _, threshold := range thresholds {
			t.Run(b.name+"/"+string(threshold), func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = threshold
				resolved := tier2(t, e, f, ws, func(h string) workflow.ReviewOutcome { return bound(t, workflow.VerdictApproved, h, nil) })
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase-review", Attempt: 1}), deliverWait)
				if rb := e.pullRequestDoc(t, "deliver-1-rebase-1"); rb["kind"] != "minor_conflicts" || rb["marked"] != commitN(1) {
					t.Fatalf("rebase = %v", rb)
				}
				if c := e.pullRequestDoc(t, "deliver-1-rebase-1-covered"); c["head"] != resolved || c["findings"] != float64(0) {
					t.Fatalf("coverage = %v", c)
				}
				f.SetChecks(repoRemote, resolved, ciPassed...)
				f.SetMergeability(forge.Mergeability{})
				next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
				if threshold == config.RebaseMinorConflicts || threshold == config.RebaseNoConflicts {
					want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
					if d := e.decision(t, "deliver-2"); !strings.Contains(d.Question, "a minor_conflicts rebase is at or above reapprove_after_rebase = "+string(threshold)) {
						t.Fatalf("question = %q", d.Question)
					}
					e.answer(t, "deliver-2", "merge")
					next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-2"})
				} else if a := e.pullRequestDoc(t, "deliver-2-authorized"); a["kind"] != "minor_conflicts" {
					t.Fatalf("carried authorization = %v", a)
				}
				want(t, next, done)
				if got := mergedHead(f); got != resolved {
					t.Fatalf("merged head = %q, want the resolution", got)
				}
			})
		}
	}
}

// TestQueueTier2Escalates: a behaviour change, a narrowed review asking for changes and no rebaser profile each send
// the build back; a narrowed approval with a suggestion asks the Guildmaster for the rebased head.
func TestQueueTier2Escalates(t *testing.T) {
	rebuild := func(e *env) {
		exp := buildExp(t, 2, head3, "")
		exp.Inputs["feedback"] = interptest.AnyDigest
		e.script.Expect(exp)
	}
	building := interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2}
	t.Run("behaviour change", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		base := baseN(1)
		minor(ws, base, head, commitN(1))
		f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
		withRebaser(e)
		ws.setTip(commitN(2))
		e.script.Expect(rebaserExp("deliver", 1, commitN(2), true))
		e.answer(t, "deliver-1", "merge")
		want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase", Attempt: 1})
		rebuild(e)
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase", Attempt: 1}), building)
		if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); esc["carry"] == true || !strings.Contains(esc["reason"].(string), "changes behaviour") {
			t.Fatalf("escalation = %v", esc)
		}
	})
	t.Run("changes requested", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		tier2(t, e, f, ws, func(h string) workflow.ReviewOutcome {
			return bound(t, workflow.VerdictChangesRequested, h, []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "The resolution drops the base's guard."}})
		})
		rebuild(e)
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase-review", Attempt: 1}), building)
		if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); !strings.Contains(esc["reason"].(string), "drops the base's guard") {
			t.Fatalf("escalation = %v", esc)
		}
	})
	t.Run("approved with a suggestion", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		resolved := tier2(t, e, f, ws, func(h string) workflow.ReviewOutcome {
			o := bound(t, workflow.VerdictApproved, h, nil)
			o.Suggestions = []workflow.Suggestion{{ID: "smell-1", Summary: "A comment could say why."}}
			return o
		})
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase-review", Attempt: 1}), deliverWait)
		f.SetChecks(repoRemote, resolved, ciPassed...)
		f.SetMergeability(forge.Mergeability{})
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
		if d := e.decision(t, "deliver-2"); !strings.Contains(d.Question, "1 finding") {
			t.Fatalf("question = %q", d.Question)
		}
	})
	t.Run("no rebaser profile", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[0])
		base := baseN(1)
		minor(ws, base, head, commitN(1))
		f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
		e.answer(t, "deliver-1", "merge")
		rebuild(e)
		want(t, e.advance(t, answeredMerge), building)
		if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); !strings.Contains(esc["reason"].(string), `no "rebaser" profile`) {
			t.Fatalf("escalation = %v", esc)
		}
	})
}

// TestShippedRebaserAndRoleReviewerResolveMinorConflicts: a Director config parsed with no [profile.rebaser] has the
// shipped rebaser, so a minor conflict runs a rebaser Artisan rather than escalating, and with the workflow's review
// naming no profile the narrowed review runs under the reviewer role profile.
func TestShippedRebaserAndRoleReviewerResolveMinorConflicts(t *testing.T) {
	parsed, err := config.Parse([]byte("schema_version = 1\nmode = \"director\"\nname = \"alpha\"\n\n[[repository]]\nid = \"app\"\npath = \"/srv/app\"\nremote = \"git@github.com:example/app.git\"\nowner_machine = \"workstation\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := parsed.Profile("rebaser")
	if err != nil || shipped.Adapter != interp.ClaudeAdapter {
		t.Fatalf("parsed rebaser = %+v, %v; want the shipped claude profile", shipped, err)
	}
	// The mock script stands in for every launch, so the shipped profile runs on the mock adapter here.
	shipped.Adapter = interp.MockAdapter

	def := loadWorkflow(t, "validate")
	for i := range def.Steps {
		if def.Steps[i].ID == "review" {
			delete(def.Steps[i].Settings, "profile")
		}
	}
	e, f, ws := queueEnvDefinition(t, backends[0], def)
	e.eng.Config.Profiles["rebaser"] = shipped
	base := baseN(1)
	minor(ws, base, head, commitN(1))
	resolved := squashed(base, commitN(2))
	f.AddCommit(repoPath, resolved)
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
	ws.setTip(commitN(2))
	e.script.Expect(rebaserExp("deliver", 1, commitN(2), false), narrowExp(t, "deliver", 1, resolved, bound(t, workflow.VerdictApproved, resolved, nil)))
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase-review", Attempt: 1})
	launched := e.script.Launched()
	if n := len(launched); n < 2 || launched[n-2].Profile != "rebaser" || launched[n-1].Profile != "reviewer" {
		t.Fatalf("launched %+v, want the rebaser then the narrowed review under reviewer", launched)
	}
}

// perHead is the fake forge with each head's mergeability its own: a head without one is clear.
type perHead struct {
	*forgetest.Fake
	mu sync.Mutex
	by map[string]forge.Mergeability
}

func (p *perHead) set(m forge.Mergeability) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.by[m.Head] = m
}

func (p *perHead) Mergeability(_ context.Context, req forge.Merge) (forge.Mergeability, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.by[req.Head]
	if !ok {
		return forge.Mergeability{Head: req.Head, State: "clear"}, nil
	}
	return m, nil
}

// validateRebaseEnv runs validate.json to the review result with the queue's workspace double installed after the
// build, so validate meets the pull request conflicting with baseN(1).
func validateRebaseEnv(t *testing.T, b backend) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	return validateRebaseEnvTasks(t, b, nil)
}

// validateRebaseEnvTasks is validateRebaseEnv with app/t7 changed by edit, as newEnvTasks changes it.
func validateRebaseEnvTasks(t *testing.T, b backend, edit func(*store.Task)) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	return validateRebaseEnvDef(t, b, loadWorkflow(t, "validate"), edit)
}

// validateRebaseEnvDef is validateRebaseEnvTasks over workflow def, a variant of validate.json.
func validateRebaseEnvDef(t *testing.T, b backend, def *workflow.Definition, edit func(*store.Task)) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	e, f := newValidateEnvTasks(t, b, def, edit)
	for n := 1; n < 10; n++ {
		f.AddCommit(repoPath, commitN(n))
	}
	e.accepted(t)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	ws := &queueWorkspaces{Workspaces: gittest.NewWorkspaces(), tip: head}
	e.eng.Workspaces = ws
	p := &perHead{Fake: f, by: map[string]forge.Mergeability{}}
	p.set(forge.Mergeability{Head: head, Base: baseN(1), State: "conflicting", Behind: 1, Files: []string{"a.go"}})
	e.eng.Forge = p
	return e, f, ws
}

var reviewed = interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}

// TestValidateRebasesAConflictingPullRequest: a pull request conflicting before it is ready is rebased at validate,
// with no Director turn: a clean replay is pushed to the same pull request and its checks read, the result is bound
// to the rebased head and names the rebase, and deliver asks the merge decision for that head.
func TestValidateRebasesAConflictingPullRequest(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := validateRebaseEnv(t, b)
			clean(ws, baseN(1), head, head2)
			want(t, e.advance(t, reviewed), waitChecks)
			if rb := e.pullRequestDoc(t, "validate-1-rebase-1"); rb["kind"] != "no_conflicts" || rb["rebased"] != head2 {
				t.Fatalf("rebase = %v", rb)
			}
			f.SetChecks(repoRemote, head2, ciPassed...)
			f.SetMergeability(forge.Mergeability{})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			v := e.pullRequestDoc(t, "validate-1-result")
			if v["head"] != head2 || v["verdict"] != "passed" || len(v["rebase"].([]any)) != 1 {
				t.Fatalf("result = %v", v)
			}
			if d := e.decision(t, "deliver-1"); d.Subject.Digest != head2 {
				t.Fatalf("decision subject = %+v", d.Subject)
			}
			for id := range e.messages(t) {
				if strings.Contains(id, "pr-blocked") {
					t.Fatalf("validate waited for the Director: %s", id)
				}
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
			if mergedHead(f) != head2 {
				t.Fatal("not merged at the rebased head")
			}
		})
	}
}

// TestValidateRebaseFailsToBuild: a major_conflicts replay at validate is a failed result whose feedback names the
// base, the files and the reason, and the failed loop sends the build back.
func TestValidateRebaseFailsToBuild(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, ws := validateRebaseEnv(t, b)
			ws.SetRebase(baseN(1), head, git.Rebased{Marked: commitN(1), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "logo.png"}}, HeadPatchID: "p"})
			exp := buildExp(t, 2, head3, "")
			exp.Inputs["feedback"] = interptest.AnyDigest
			e.script.Expect(exp)
			want(t, e.advance(t, reviewed), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			v := e.pullRequestDoc(t, "validate-1-result")
			esc, _ := v["rebase_escalation"].(map[string]any)
			if v["verdict"] != "failed" || v["head"] != head || esc == nil || esc["onto"] != baseN(1) || !strings.Contains(esc["reason"].(string), "logo.png") {
				t.Fatalf("result = %v", v)
			}
		})
	}
}

// TestValidateTier2: a minor_conflicts replay at validate runs the rebaser and the narrowed review, then pushes the
// resolution and reads its checks.
func TestValidateTier2(t *testing.T) {
	e, f, ws := validateRebaseEnv(t, backends[1])
	minor(ws, baseN(1), head, commitN(1))
	withRebaser(e)
	resolved := squashed(baseN(1), commitN(2))
	f.AddCommit(repoPath, resolved)
	ws.setTip(commitN(2))
	e.script.Expect(rebaserExp("validate", 1, commitN(2), false), narrowExp(t, "validate", 1, resolved, bound(t, workflow.VerdictApproved, resolved, nil)))
	want(t, e.advance(t, reviewed), interp.Next{Wait: interp.WaitAttempt, Step: "validate-rebase", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "validate-rebase", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "validate-rebase-review", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "validate-rebase-review", Attempt: 1}), waitChecks)
	f.SetChecks(repoRemote, resolved, ciPassed...)
	f.SetMergeability(forge.Mergeability{})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if v := e.pullRequestDoc(t, "validate-1-result"); v["head"] != resolved {
		t.Fatalf("result = %v", v)
	}
}
