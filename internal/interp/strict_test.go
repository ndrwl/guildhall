package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestBugPlanPromptRefusesOnlySlices: the shipped bug workflow's plan, which no Guildmaster approval shows, is told
// that intake refuses its `## Slices` alone, not a Summary or Open questions, and intake refuses the Invalid Slices
// example the prompt shows with the error the prompt quotes.
func TestBugPlanPromptRefusesOnlySlices(t *testing.T) {
	def, err := workflow.Bug()
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte(spec.SlicesInvalid + "\n")
	e := newEnvDefinition(t, backends[1], def,
		interptest.Expectation{Step: "research", Attempt: 1, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("researched", interptest.File{Path: "notes.md", Data: researchNotes})},
		interptest.Expectation{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{"context": "sha256:" + sum(researchNotes)}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: bad})})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	data, err := os.ReadFile(filepath.Join(e.stageDir("plan", 1), "prompt", render.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(data)
	if !strings.Contains(prompt, "\nStrict sections: Guildhall parses these sections of `spec.md` itself, and `guildhall result submit` and intake refuse a result that breaks one") {
		t.Error("the bug plan prompt does not say intake refuses a result that breaks a strict section")
	}
	if !strings.Contains(prompt, "**`"+spec.SlicesHeading+"`.** "+spec.SlicesRule) || !strings.Contains(prompt, "Invalid:\n\n```text\n"+spec.SlicesInvalid+"\n```") {
		t.Error("the bug plan prompt does not show the Slices section")
	}
	for _, h := range []string{spec.SummaryHeading, spec.OpenQuestionsHeading} {
		if strings.Contains(prompt, "**`"+h+"`.**") {
			t.Errorf("the bug plan prompt names %s as refused, which intake does not refuse", h)
		}
	}
	said := checkBefore(t, e, "plan", 1)
	if said == nil || !strings.HasPrefix(said.reason, spec.SlicesError) {
		t.Errorf("CheckResult of the prompt's Invalid Slices = %+v, want a rejection starting %q", said, spec.SlicesError)
	}
}
