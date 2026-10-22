package interp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Exercise the engine with actual commit ranges and durable records. Only the
// worker responses are scripted: this test neither requires nor invokes a model.
func TestRebaseRealGitStore(t *testing.T) {
	// One history serves every scenario: classification only reads it, and each scenario names its commits by id.
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "empty-gitconfig"))
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=integration", "-c", "user.email=integration@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name, content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		run("add", name)
		run("commit", "-qm", message)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q")
	b1 := commit("base.txt", "base\n", "base")
	h1 := commit("task.txt", "task\n", "task")
	run("checkout", "-q", "--detach", b1)
	b2 := commit("base.txt", "upstream\n", "upstream")
	run("cherry-pick", h1)
	rebased := run("rev-parse", "HEAD")
	edited := commit("task.txt", "changed task\n", "edit task")
	for _, scenario := range []string{"rebase", "requested-full", "requested-full-missing-base", "edited-patch", "changed-standards", "missing-base", "unknown-head"} {
		t.Run(scenario, func(t *testing.T) {
			h2 := rebased
			if scenario == "edited-patch" {
				h2 = edited
			}
			if scenario == "unknown-head" {
				h2 = strings.Repeat("0", 40)
			}
			classification, err := (git.Program{}).Classify(context.Background(), repo, git.Range{Base: b1, Head: h1}, git.Range{Base: b2, Head: h2})
			if err != nil {
				t.Fatal(err)
			}
			scope := git.ScopeFull
			if scenario == "rebase" {
				scope = git.ScopeRebase
			}
			o1 := bound(t, workflow.VerdictChangesRequested, h1, []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "Request another round."}})
			o2 := bound(t, workflow.VerdictApproved, h2, nil, workflow.Disposition{Finding: "f1", State: workflow.Resolved})
			if scenario == "changed-standards" {
				o2.Revisions.StandardsRevision = "standards-2"
			}
			review2 := reviewExp(t, "review", "reviewer", 2, h2, &o1, o2)
			if scope == git.ScopeRebase {
				review2.Inputs[interp.ResolutionPort] = "sha256:" + sum(classification.Diff)
			}
			e := newEnvDefinition(t, backends[1], loadWorkflow(t, "loop"),
				planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, h1, ""), reviewExp(t, "review", "reviewer", 1, h1, nil, o1),
				buildExp(t, 2, h2, outcomeDigest(o1)), review2)
			e.eng.Config.Repositories[0].Path = repo
			e.eng.Rebases = git.Program{}
			calls := 0
			e.eng.Base = bases(b1, b2)(&calls)
			if scenario == "missing-base" || scenario == "requested-full-missing-base" {
				e.eng.Base = nil
			}
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			if scenario == "changed-standards" {
				e.eng.StandardsRevision = "standards-2"
			}
			requested := strings.HasPrefix(scenario, "requested-full")
			if requested {
				if classification.Scope != git.ScopeRebase {
					t.Fatalf("fixture must otherwise qualify for rebase review: %+v", classification)
				}
				if _, err := e.h.RequestFullReview("full:app:t7:1", "app", "t7"); err != nil {
					t.Fatal(err)
				}
			}
			ev := interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}
			next := e.advance(t, ev)
			want(t, next, interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			// Duplicate intake must reuse the recorded binding and not launch again.
			want(t, e.advance(t, ev), next)
			var binding struct {
				Scope     git.Scope          `json:"scope"`
				Range     *git.Range         `json:"range"`
				Revisions workflow.Revisions `json:"revisions"`
			}
			read(t, e, "review-2-inputs", &binding)
			if binding.Scope != scope {
				t.Fatalf("scope %s, want %s", binding.Scope, scope)
			}
			if binding.Revisions != o2.Revisions {
				t.Fatalf("binding revisions %+v, want %+v", binding.Revisions, o2.Revisions)
			}
			if e.eng.Base != nil && (binding.Range == nil || *binding.Range != (git.Range{Base: b2, Head: h2})) {
				t.Fatalf("wrong range: %+v", binding.Range)
			}
			if _, ok := e.documents(t)["review-2-resolution"]; ok != (scope == git.ScopeRebase) {
				t.Fatalf("resolution present: %v", ok)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			var outcome workflow.ReviewOutcome
			read(t, e, "review-2-outcome", &outcome)
			if outcome.Revisions != o2.Revisions {
				t.Fatalf("outcome not bound to new revisions: %+v", outcome)
			}
			requests, err := e.h.Records.FullReviews("app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			if requested && (len(requests) != 1 || requests[0].ConsumedBy != "review-2") {
				t.Fatalf("request not consumed exactly once by review-2: %+v", requests)
			}
			// Optional reviewer artifacts are copies of the product's persisted contracts.
			if root := os.Getenv("GUILDHALL_TEST_EVIDENCE"); root != "" {
				dir := filepath.Join(root, scenario)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				requestData, err := json.MarshalIndent(requests, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "full-review-requests.json"), requestData, 0600); err != nil {
					t.Fatal(err)
				}
				for name, doc := range e.documents(t) {
					if name != "review-2-inputs" && name != "review-2-outcome" && name != "review-2-resolution" {
						continue
					}
					data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(doc.Path)))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				data, err := json.MarshalIndent(e.decision(t, "deliver-1"), "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "deliver-decision.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
