package interp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestRebaseAwareReview: review 1 of head 1 on base 1 requests changes, the build lands head 2 on base 2 and
// review 2 is classified against the range review 1 reviewed. A rebase gives review 2 the resolution diff
// beside its previous outcome and marks the round scope rebase; anything else is a full review. In every case
// review 2's outcome binds to head 2. A full-review request made before review 2 makes it full without
// classifying, and review 2 consumes the request.
func TestRebaseAwareReview(t *testing.T) {
	base1, base2 := strings.Repeat("b", 40), strings.Repeat("c", 40)
	resolution := []byte("diff --git a/n.txt b/n.txt\n")
	f1 := workflow.Finding{ID: "f1", Rule: "S2", Summary: "No red test shown."}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
	o2 := bound(t, workflow.VerdictApproved, head2, nil, workflow.Disposition{Finding: "f1", State: workflow.Resolved})
	for _, tc := range []struct {
		name     string
		base     func(n *int) func(string, string) (string, error)
		classify git.Classification
		scope    git.Scope
		calls    int
	}{
		{"rebase", bases(base1, base2), git.Classification{Scope: git.ScopeRebase, Diff: resolution}, git.ScopeRebase, 1},
		{"changed standards", bases(base1, base2), git.Classification{Scope: git.ScopeRebase, Diff: resolution}, git.ScopeFull, 0},
		{"requested full", bases(base1, base2), git.Classification{Scope: git.ScopeRebase, Diff: resolution}, git.ScopeFull, 0},
		{"full", bases(base1, base2), git.Classification{Scope: git.ScopeFull, Reason: "range-diff shows a changed commit"}, git.ScopeFull, 1},
		{"no base", nil, git.Classification{Scope: git.ScopeRebase, Diff: resolution}, git.ScopeFull, 0},
		{"base error", func(*int) func(string, string) (string, error) {
			return func(string, string) (string, error) { return "", errors.New("no remote") }
		}, git.Classification{Scope: git.ScopeRebase, Diff: resolution}, git.ScopeFull, 0},
	} {
		for _, b := range backends {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				nextOutcome := o2
				if tc.name == "changed standards" {
					nextOutcome.Revisions.StandardsRevision = "standards-2"
				}
				review2 := reviewExp(t, "review", "reviewer", 2, head2, &o1, nextOutcome)
				if tc.scope == git.ScopeRebase {
					review2.Inputs[interp.ResolutionPort] = "sha256:" + sum(resolution)
				}
				e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
					buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
					buildExp(t, 2, head2, outcomeDigest(o1)), review2)
				repo := t.TempDir()
				e.eng.Config.Repositories[0].Path = repo
				script := &gittest.Script{}
				script.Add(repo, git.Range{Base: base1, Head: head}, git.Range{Base: base2, Head: head2}, tc.classify)
				e.eng.Rebases = script
				calls := 0
				if tc.base != nil {
					e.eng.Base = tc.base(&calls)
				}
				e.accepted(t)
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
				if tc.name == "changed standards" {
					e.eng.StandardsRevision = "standards-2"
				}
				if tc.name == "requested full" {
					if _, err := e.h.RequestFullReview("full:app:t7:1", "app", "t7"); err != nil {
						t.Fatal(err)
					}
				}
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}),
					interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				if len(script.Calls) != tc.calls {
					t.Errorf("classified %d times, want %d: %v", len(script.Calls), tc.calls, script.Calls)
				}

				var b2 struct {
					Range *git.Range `json:"range"`
					Scope git.Scope  `json:"scope"`
				}
				read(t, e, "review-2-inputs", &b2)
				if b2.Scope != tc.scope {
					t.Errorf("review 2 scope = %q, want %q", b2.Scope, tc.scope)
				}
				if tc.base != nil && tc.name != "base error" && (b2.Range == nil || *b2.Range != (git.Range{Base: base2, Head: head2})) {
					t.Errorf("review 2 range = %+v, want base 2 and head 2", b2.Range)
				}
				for round, base := range []string{base1, base2} {
					if tc.base == nil || tc.name == "base error" {
						base = "unavailable"
					}
					data, err := os.ReadFile(filepath.Join(e.stageDir("review", round+1), "prompt", "prompt.md"))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(data), "Review base: "+base+"\n") {
						t.Errorf("review %d prompt does not name recorded base %s", round+1, base)
					}
				}
				if tc.name == "requested full" {
					fr, err := e.h.Records.FullReviews("app", "t7")
					if err != nil || len(fr) != 1 || fr[0].ConsumedBy != "review-2" {
						t.Errorf("FullReviews = %+v, %v; want request 1 consumed by review-2", fr, err)
					}
				}
				var got workflow.ReviewOutcome
				read(t, e, "review-2-outcome", &got)
				if got.Revisions != nextOutcome.Revisions {
					t.Errorf("outcome revisions = %+v, want %+v", got.Revisions, nextOutcome.Revisions)
				}
			})
		}
	}
}

// bases answers base 1 to the first review and base 2 to later ones, counting calls in n.
func bases(base1, base2 string) func(n *int) func(string, string) (string, error) {
	return func(n *int) func(string, string) (string, error) {
		return func(string, string) (string, error) {
			*n++
			if *n == 1 {
				return base1, nil
			}
			return base2, nil
		}
	}
}

// read decodes the recorded document name.
func read(t *testing.T, e *env, name string, v any) {
	t.Helper()
	d, ok := e.documents(t)[name]
	if !ok {
		t.Fatalf("no document %s", name)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}
