package interp_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestMergeabilityWaitsForDirectorOncePerHeadAndState(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			for i, state := range []string{"conflicting", "behind"} {
				f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: state, Behind: 2, Files: []string{"docs/contracts/cli.md"}})
				var n interp.Next
				if i == 0 {
					n = e.toValidate(t)
				} else {
					n = e.advance(t, interp.Event{Kind: interp.TaskCreated})
				}
				want(t, n, interp.Next{Wait: interp.WaitDirector, Step: "validate"})
				count := len(e.inbox(t))
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), n)
				if len(e.inbox(t)) != count {
					t.Fatal("replayed mergeability sent another message")
				}
				messages := e.messages(t)
				found := false
				for _, text := range messages {
					if strings.Contains(text, "State: "+state) {
						found = true
						for _, required := range []string{head, baseHead, "https://forge.invalid/1", "docs/contracts/cli.md", "2", "step send-back", "hold", "abandoning"} {
							if !strings.Contains(text, required) {
								t.Errorf("missing %q in %s", required, text)
							}
						}
					}
				}
				if !found {
					t.Fatalf("no %s report: %v", state, messages)
				}
			}
			f.SetMergeability(forge.Mergeability{})
			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: "conflicting", Behind: 2, Files: []string{"docs/contracts/cli.md"}})
			count := len(e.inbox(t))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
			if len(e.inbox(t)) != count {
				t.Fatal("deliver duplicated validate's report")
			}
		})
	}
}

func TestSendBackSucceededBuildWithFeedbackAndRepublish(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			base := baseHead
			e.eng.Base = func(string) (string, error) { return base, nil }
			script := &gittest.Script{}
			resolution := []byte("diff --git a/docs/contracts/cli.md b/docs/contracts/cli.md\n")
			nextBase := strings.Repeat("c", 40)
			script.Add(repoPath, git.Range{Base: baseHead, Head: head}, git.Range{Base: nextBase, Head: head2}, git.Classification{Scope: git.ScopeRebase, Diff: resolution})
			e.eng.Rebases = script
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: "conflicting", Behind: 1, Files: []string{"docs/contracts/cli.md"}})
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			base = nextBase
			feedback := "Rebase onto the latest base; resolve the documentation conflict."
			review2 := reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil))
			review2.Inputs[interp.ResolutionPort] = "sha256:" + sum(resolution)
			e.script.Expect(buildExp(t, 2, head2, "sha256:"+sum([]byte(feedback))), review2)
			ev := interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			f.SetChecks(repoRemote, head2, ciPassed...)
			f.SetMergeability(forge.Mergeability{Head: head2, Base: nextBase, State: "conflicting", Behind: 1, Files: []string{"docs/contracts/cli.md"}})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			reports := 0
			for _, text := range e.messages(t) {
				if strings.Contains(text, "State: conflicting") {
					reports++
				}
			}
			if reports != 2 {
				t.Fatalf("reports after new head = %d", reports)
			}
			f.SetMergeability(forge.Mergeability{})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if len(script.Calls) != 1 {
				t.Fatalf("rebase classifications = %v", script.Calls)
			}
			var binding struct {
				Scope git.Scope `json:"scope"`
			}
			read(t, e, "review-2-inputs", &binding)
			if binding.Scope != git.ScopeRebase {
				t.Fatalf("review scope = %s", binding.Scope)
			}
			prs := f.PullRequests(repoRemote)
			if len(prs) != 1 || prs[0].Head != head2 {
				t.Fatalf("republish = %+v", prs)
			}
		})
	}
}
