package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// unattended requires everything but the Guildmaster's approval.
var unattended = config.MergeRequires{GuildmasterApproval: ptr(false)}

// requires sets the repository's merge requirements.
func (e *env) requires(m config.MergeRequires) { e.eng.Config.Repositories[0].Merge.Requires = m }

// newRequiresEnv is validate.json scripted through a review 1 that records o, under merge requirements m.
func newRequiresEnv(t *testing.T, b backend, m config.MergeRequires, o workflow.ReviewOutcome) (*env, *forgetest.Fake) {
	t.Helper()
	e := newEnvDefinition(t, b, loadWorkflow(t, "validate"),
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""),
		reviewExp(t, "review", "reviewer", 1, head, nil, o))
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	for _, h := range []string{head, head2, head3} {
		f.AddCommit(repoPath, h)
	}
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	e.requires(m)
	return e, f
}

// decisions returns the task's decision ids.
func (e *env) decisionIDs(t *testing.T) []string {
	t.Helper()
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	return ids
}

// authorizations returns the names of the task's recorded merge authorizations.
func (e *env) authorizations(t *testing.T) []string {
	t.Helper()
	var out []string
	for name := range e.documents(t) {
		if strings.HasSuffix(name, "-authorized") {
			out = append(out, name)
		}
	}
	return out
}

var done = interp.Next{Wait: interp.WaitDone, Step: "deliver"}

// TestMergeWithoutGuildmaster: with guildmaster_approval off and everything else holding, deliver records
// deliver-1-authorized with the requirements and the evidence it read, merges without a decision, tells the Director
// who merged, runs the audit sample and closes the task.
func TestMergeWithoutGuildmaster(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.requires(unattended)
			e.eng.Config.Local = auditEveryTask
			want(t, e.toValidate(t), waitChecks)
			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)

			if ids := e.decisionIDs(t); slices.Contains(ids, "deliver-1") || !slices.Contains(ids, "audit") {
				t.Errorf("decisions = %q, want no merge decision and the audit", ids)
			}
			a := e.pullRequestDoc(t, "deliver-1-authorized")
			requires := a["requires"].(map[string]any)
			if a["schema"] != "guildhall.merge-authorization/1" || a["head"] != head || a["mergeability"] != "clear" ||
				requires["guildmaster_approval"] != false || requires["artisan_approval"] != true || requires["ci_green"] != true || requires["nothing_open"] != true {
				t.Errorf("deliver-1-authorized = %v", a)
			}
			if pr := a["pull_request"].(map[string]any); pr["url"] != f.PullRequests(repoRemote)[0].URL || pr["number"] != float64(1) {
				t.Errorf("authorized pull request = %v", pr)
			}
			evidence := map[string][]any{}
			for _, c := range a["conditions"].([]any) {
				c := c.(map[string]any)
				if c["requirement"] != "guildmaster_approval" && c["holds"] != true {
					t.Errorf("condition %v does not hold", c)
				}
				ev, _ := c["evidence"].([]any)
				evidence[c["requirement"].(string)] = ev
			}
			if !slices.Equal(evidence["artisan_approval"], []any{"review-1-outcome"}) || !slices.Equal(evidence["ci_green"], []any{"validate-1-result"}) {
				t.Errorf("evidence = %v, want the review outcome and the validation result", evidence)
			}
			if prs := f.PullRequests(repoRemote); prs[0].State != forge.Merged || prs[0].Head != head {
				t.Errorf("pull requests = %+v, want #1 merged at %s", prs, head)
			}
			if merged := e.pullRequestDoc(t, "deliver-1-merge"); merged["state"] != "merged" {
				t.Errorf("deliver-1-merge = %v", merged)
			}
			if msg := e.messages(t)["app.t7.merged.deliver-1"]; !strings.Contains(msg, "Merged head "+head+" (merged by Guildhall, deliver-1-authorized)") {
				t.Errorf("merged message = %q", msg)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
		})
	}
}

// TestMergeWithoutGuildmasterReplay: a failure around the merge record after the forge merged replays to the same
// merge, one authorization and one merged message.
func TestMergeWithoutGuildmasterReplay(t *testing.T) {
	for _, b := range backends {
		for _, name := range []string{"deliver-1-authorized", "deliver-1-merge"} {
			for _, after := range []bool{false, true} {
				t.Run(b.name+"/"+name+map[bool]string{false: "/before", true: "/after"}[after], func(t *testing.T) {
					e, f := newValidateEnv(t, b)
					e.requires(unattended)
					want(t, e.toValidate(t), waitChecks)
					f.SetChecks(repoRemote, head, ciPassed...)
					e.eng.Records = &failPut{Records: e.eng.Records, name: name, after: after}
					if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
						t.Fatalf("advance = %v, want the injected failure", err)
					}
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
					if got := e.authorizations(t); !slices.Equal(got, []string{"deliver-1-authorized"}) {
						t.Errorf("authorizations = %q, want one", got)
					}
					n := 0
					for _, id := range e.inbox(t) {
						if strings.HasPrefix(id, "app.t7.merged.") {
							n++
						}
					}
					if n != 1 {
						t.Errorf("%d merged messages, want 1", n)
					}
					if prs := f.PullRequests(repoRemote); len(prs) != 1 || prs[0].State != forge.Merged {
						t.Errorf("pull requests = %+v, want one merged", prs)
					}
				})
			}
		}
	}
}

// TestMergeRequiresRedChecks: under guildmaster_approval off, failed checks take validate's failed loop and its loop
// exit; nothing merges.
func TestMergeRequiresRedChecks(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	e.requires(unattended)
	failed1 := result(head, "failed", ciFailed, "ci")
	e.script.Expect(buildExp(t, 2, head2, failed1.digest(t)),
		reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil)))
	f.SetChecks(repoRemote, head, ciFailed...)
	f.SetChecks(repoRemote, head2, ciFailed...)
	want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "validate", Decision: "validate-x2"})
	if _, merges := f.Calls(); merges != 0 {
		t.Errorf("merges = %d with red checks", merges)
	}
}

// rebuildEnv is rebuild.json under merge requirements m: validate passes head 1, then the rebuild's head 2 is reviewed
// (or, with staleReview, head 1 is) and published, so deliver's validation result is not for the head it delivers.
func rebuildEnv(t *testing.T, m config.MergeRequires, staleReview bool) (*env, *forgetest.Fake) {
	t.Helper()
	def := loadWorkflow(t, "rebuild")
	reviewed := head2
	if staleReview {
		reviewed = head
		for i := range def.Steps {
			if def.Steps[i].ID == "review" {
				def.Steps[i].Inputs["head"] = workflow.Refs{"build.head"}
			}
		}
		if err := workflow.Validate(def); err != nil {
			t.Fatal(err)
		}
	}
	rebuild := buildExp(t, 1, head2, "")
	rebuild.Step = "rebuild"
	e := newEnvDefinition(t, backends[0], def,
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""), rebuild,
		reviewExp(t, "review", "reviewer", 1, reviewed, nil, bound(t, workflow.VerdictApproved, reviewed, nil)))
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	f.AddCommit(repoPath, head)
	f.AddCommit(repoPath, head2)
	f.SetChecks(repoRemote, head, ciPassed...)
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	e.requires(m)
	e.accepted(t)
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "rebuild", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "rebuild", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
	return e, f
}

// TestMergeRequiresHardGates: a required ci_green or artisan_approval that does not hold is the gate's error,
// whatever guildmaster_approval is, and nothing merges or is asked; a requirement turned off skips its gate, and the
// merge question shows it as not required.
func TestMergeRequiresHardGates(t *testing.T) {
	reviewDone := interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}
	for _, guildmaster := range []bool{false, true} {
		t.Run("validation gate", func(t *testing.T) {
			e, f := rebuildEnv(t, config.MergeRequires{GuildmasterApproval: ptr(guildmaster)}, false)
			if err := advanceErr(t, e, reviewDone); !errors.Is(err, interp.ErrValidationGate) {
				t.Fatalf("advance = %v, want ErrValidationGate", err)
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") {
				t.Errorf("merges = %d, decisions = %q, want neither", merges, e.decisionIDs(t))
			}
		})
		t.Run("review gate", func(t *testing.T) {
			e, f := rebuildEnv(t, config.MergeRequires{GuildmasterApproval: ptr(guildmaster), CIGreen: ptr(false)}, true)
			if err := advanceErr(t, e, reviewDone); !errors.Is(err, interp.ErrReviewGate) {
				t.Fatalf("advance = %v, want ErrReviewGate", err)
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") {
				t.Errorf("merges = %d, decisions = %q, want neither", merges, e.decisionIDs(t))
			}
		})
	}
	t.Run("ci_green off", func(t *testing.T) {
		e, f := rebuildEnv(t, config.MergeRequires{CIGreen: ptr(false)}, false)
		want(t, e.advance(t, reviewDone), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		q := e.decision(t, "deliver-1").Question
		for _, line := range []string{"- guildmaster_approval: fails: this decision is not answered yet\n", "- artisan_approval: holds\n", "- ci_green: not required\n", "- nothing_open: holds\n"} {
			if !strings.Contains(q, line) {
				t.Errorf("question %q does not list %q", q, line)
			}
		}
		e.answer(t, "deliver-1", "merge")
		want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
		if _, merges := f.Calls(); merges != 1 {
			t.Errorf("merges = %d, want 1", merges)
		}
	})
	t.Run("artisan_approval and ci_green off", func(t *testing.T) {
		e, _ := rebuildEnv(t, config.MergeRequires{ArtisanApproval: ptr(false), CIGreen: ptr(false)}, true)
		want(t, e.advance(t, reviewDone), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		if q := e.decision(t, "deliver-1").Question; !strings.Contains(q, "- artisan_approval: not required\n") || !strings.Contains(q, "- ci_green: not required\n") {
			t.Errorf("question = %q, want both gates not required", q)
		}
	})
}

// mergeabilityAfter is a forge whose first clear observations are the fake's, and every later one is state:
// the pull request falls behind or conflicts after validate read it clear.
type mergeabilityAfter struct {
	*forgetest.Fake
	mu    sync.Mutex
	clear int
	state forge.Mergeability
}

func (m *mergeabilityAfter) Mergeability(ctx context.Context, req forge.Merge) (forge.Mergeability, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clear > 0 {
		m.clear--
		return m.Fake.Mergeability(ctx, req)
	}
	s := m.state
	s.Head = req.Head
	return s, nil
}

// TestMergeWithoutGuildmasterBlocked: a behind or conflicting pull request waits for the Director with pr-blocked,
// at validate and at deliver, and nothing merges or is asked.
func TestMergeWithoutGuildmasterBlocked(t *testing.T) {
	for _, state := range []string{"behind", "conflicting"} {
		blocked := forge.Mergeability{Base: baseHead, State: state, Behind: 1, Files: []string{"go.mod"}}
		t.Run(state+"/validate", func(t *testing.T) {
			e, f := newValidateEnv(t, backends[0])
			e.requires(unattended)
			f.SetChecks(repoRemote, head, ciPassed...)
			f.SetMergeability(blocked)
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			if _, ok := e.documents(t)["pr-blocked-"+head+"-"+state]; !ok {
				t.Errorf("pr-blocked not recorded")
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") {
				t.Errorf("merges = %d, decisions = %q, want neither", merges, e.decisionIDs(t))
			}
		})
		t.Run(state+"/deliver", func(t *testing.T) {
			e, f := newValidateEnv(t, backends[0])
			e.requires(unattended)
			e.eng.Forge = &mergeabilityAfter{Fake: f, clear: 1, state: blocked}
			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
			if _, ok := e.documents(t)["pr-blocked-"+head+"-"+state]; !ok {
				t.Errorf("pr-blocked not recorded")
			}
			if _, merges := f.Calls(); merges != 0 || slices.Contains(e.decisionIDs(t), "deliver-1") || len(e.authorizations(t)) != 0 {
				t.Errorf("merges = %d, decisions = %q, authorizations %q, want none", merges, e.decisionIDs(t), e.authorizations(t))
			}
		})
	}
}

// TestMergeWithoutGuildmasterFallsBack: when nothing_open fails, with an advisory finding on the approving outcome or
// an unanswered decision on the task, deliver opens deliver-1 saying why it did not merge by itself; merge merges
// through the decision and hold holds.
func TestMergeWithoutGuildmasterFallsBack(t *testing.T) {
	advisory := bound(t, workflow.VerdictApproved, head, []workflow.Finding{{ID: "f1", Summary: "Consider a shorter name."}})
	for _, c := range []struct {
		name, why string
		outcome   workflow.ReviewOutcome
		question  bool
	}{
		{"advisory finding", "nothing_open (review-1-outcome lists 1 finding(s))", advisory, false},
		{"open question", "nothing_open (decision ask-1 is unanswered)", bound(t, workflow.VerdictApproved, head, nil), true},
	} {
		for _, choice := range []string{"merge", "hold"} {
			t.Run(c.name+"/"+choice, func(t *testing.T) {
				e, f := newRequiresEnv(t, backends[0], unattended, c.outcome)
				want(t, e.toValidate(t), waitChecks)
				if c.question {
					if _, err := e.h.Records.OpenDecision("ask-1", store.Decision{Repository: "app", TaskID: "t7", ID: "ask-1", Kind: store.DecisionQuestion, Question: "Which name?"}); err != nil {
						t.Fatal(err)
					}
				}
				f.SetChecks(repoRemote, head, ciPassed...)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				q := e.decision(t, "deliver-1").Question
				if !strings.HasPrefix(q, "Guildhall did not merge by itself: "+c.why+".\n") || !strings.Contains(q, "- guildmaster_approval: not required\n") ||
					!strings.Contains(q, "- nothing_open: fails: ") {
					t.Errorf("question = %q", q)
				}
				if len(e.authorizations(t)) != 0 {
					t.Errorf("authorizations = %q before any merge", e.authorizations(t))
				}
				e.answer(t, "deliver-1", choice)
				next := e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"})
				_, merges := f.Calls()
				if choice == "hold" {
					want(t, next, interp.Next{Wait: interp.WaitDirector, Step: "deliver", Decision: "deliver-1"})
					if merges != 0 {
						t.Errorf("merges = %d after a hold", merges)
					}
					return
				}
				want(t, next, done)
				if msg := e.messages(t)["app.t7.merged.deliver-1"]; merges != 1 || !strings.Contains(msg, "(decision deliver-1)") {
					t.Errorf("merges = %d, merged message %q, want one merge through the decision", merges, msg)
				}
			})
		}
	}
}

// TestMergeWithoutGuildmasterRefused: a merge the forge refuses records deliver-1-refused and merge-refused and waits
// for the Director; advancing again retries the same authorized merge.
func TestMergeWithoutGuildmasterRefused(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.requires(unattended)
			want(t, e.toValidate(t), waitChecks)
			f.SetChecks(repoRemote, head, ciPassed...)
			f.Refuse("Required review is missing")
			director := interp.Next{Wait: interp.WaitDirector, Step: "deliver"}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			if r := e.pullRequestDoc(t, "deliver-1-refused"); !strings.Contains(r["reason"].(string), "Required review is missing") {
				t.Errorf("deliver-1-refused = %v", r)
			}
			if !slices.ContainsFunc(e.inbox(t), func(id string) bool { return strings.HasPrefix(id, "app.t7.merge-refused.deliver-1.") }) {
				t.Errorf("inbox = %q, want merge-refused", e.inbox(t))
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			f.Refuse("")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if got := e.authorizations(t); !slices.Equal(got, []string{"deliver-1-authorized"}) || slices.Contains(e.decisionIDs(t), "deliver-1") {
				t.Errorf("authorizations = %q, decisions = %q, want one authorization and no decision", got, e.decisionIDs(t))
			}
		})
	}
}

// TestGuildmasterMergesOverNothingOpen: with every requirement on, a failing nothing_open is listed in the merge
// question, and the Guildmaster's merge merges over it.
func TestGuildmasterMergesOverNothingOpen(t *testing.T) {
	advisory := bound(t, workflow.VerdictApproved, head, []workflow.Finding{{ID: "f1", Summary: "Consider a shorter name."}})
	e, f := newRequiresEnv(t, backends[0], config.MergeRequires{}, advisory)
	want(t, e.toValidate(t), waitChecks)
	f.SetChecks(repoRemote, head, ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	q := e.decision(t, "deliver-1").Question
	if !strings.HasPrefix(q, "Merge needed for app/t7") || !strings.Contains(q, "- nothing_open: fails: review-1-outcome lists 1 finding(s)\n") ||
		!strings.Contains(q, "- ci_green: holds\n") {
		t.Errorf("question = %q", q)
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
	if _, merges := f.Calls(); merges != 1 {
		t.Errorf("merges = %d, want 1", merges)
	}
}
