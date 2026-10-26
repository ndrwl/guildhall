package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Copy the actual generated approval example into the mock reviewer's output,
// then exercise persisted intake, human approval, replay and the delivery gate.
func TestUnconfiguredStandardsReviewExample(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			placeholder := bound(t, workflow.VerdictApproved, head, nil)
			e := newEnv(t, b,
				planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, head, ""),
				reviewExp(t, "review", "reviewer", 1, head, nil, placeholder),
			)
			e.eng.StandardsRevision = ""
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			stage := e.stages[len(e.stages)-1]
			prompt, err := os.ReadFile(filepath.Join(filepath.Dir(stage.Dir), "prompt", "prompt.md"))
			if err != nil {
				t.Fatal(err)
			}
			_, section, ok := strings.Cut(string(prompt), "### Output port `outcome`")
			if !ok {
				t.Fatal("no outcome instructions")
			}
			_, section, ok = strings.Cut(section, "```json\n")
			if !ok {
				t.Fatal("no outcome example")
			}
			example, _, ok := strings.Cut(section, "\n```")
			if !ok {
				t.Fatal("unterminated example")
			}
			launched := e.script.Launched()
			work := launched[len(launched)-1].WorkDir
			if err := os.WriteFile(filepath.Join(work, interptest.ReviewOutcomeFile), []byte(example), 0600); err != nil {
				t.Fatal(err)
			}
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			if next.Decision != "sign-off-1" {
				t.Fatalf("next = %+v", next)
			}
			e.answer(t, "sign-off-1", "approved")
			next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"})
			if next.Decision != "deliver-1" {
				t.Fatalf("delivery gate = %+v", next)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), next)
		})
	}
}
