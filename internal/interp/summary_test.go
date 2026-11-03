package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// summarisedQuestions are summarisedSpec's open questions, as the spec writes them.
const summarisedQuestions = `**Q1. Which parser**
The parser can be strict or lenient.
Options: (a) strict; (b) lenient.
Recommended: (a). A strict parser names the line at fault.

**Q2. Where the views go**
Options: (a) one package; (b) one per view.
Recommended: (a).

**Q3. Engine size**
The engine may need splitting.
Options: (a) one slice; (b) two.
Recommended: (a). About 200 lines fits one review.`

// summarisedSpec is threeSlices with a Summary block that counts three open questions, and the questions in full.
var summarisedSpec = strings.Replace(strings.Replace(threeSlices, "- **Open questions:** None.", "- **Open questions:** 3.", 1),
	"## Open questions\n\nNone.\n", "## Open questions\n\n"+summarisedQuestions+"\n", 1)

// noSummary is a spec with three slices, open questions and no Summary block, like d150 plan-1's.
var noSummary = strings.Replace(summarisedSpec, "## Summary\n\n- **Goal:** deliver the large work.\n- **Open questions:** 3.\n\n", "", 1)

// TestApprovalCarriesPlanSummary: a plan's approval question carries its spec's Summary block after the first line and
// then every open question in full, byte for byte, before the inputs; a spec whose section says None says so.
func TestApprovalCarriesPlanSummary(t *testing.T) {
	for _, c := range []struct {
		name, spec, has string
	}{
		{"three questions", summarisedSpec, "attempt 1.\n\nPlan summary:\n- **Goal:** deliver the large work.\n- **Open questions:** 3.\n\nOpen questions in full:\n" + summarisedQuestions + "\n\nInputs:\n"},
		{"none", threeSlices, "attempt 1.\n\nPlan summary:\n- **Goal:** deliver the large work.\n- **Open questions:** None.\n\nOpen questions: none.\n\nInputs:\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, b := range backends {
				t.Run(b.name, func(t *testing.T) {
					e, _, _ := newSliceEnv(t, b, true, sliceScript(t, c.spec)...)
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}),
						interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
					if q := e.decision(t, "accept-1").Question; !strings.Contains(q, c.has) || strings.Contains(q, "No summary in this spec") {
						t.Errorf("question = %q, want %q", q, c.has)
					}
				})
			}
		})
	}
}

// TestPlanCheckRefusesAnUnanswerableSpec: a plan whose spec an approval will show is refused, the attempt failing with
// the reason and no spec recorded, when the spec has no Summary block or its open questions are malformed.
func TestPlanCheckRefusesAnUnanswerableSpec(t *testing.T) {
	for _, c := range []struct {
		name, spec, says string
	}{
		{"no summary", noSummary, "spec has no summary: the spec has no ## Summary block"},
		{"no open questions", strings.Replace(threeSlices, "## Open questions\n\nNone.\n", "", 1), "no open questions section"},
		{"prose", strings.Replace(threeSlices, "\nNone.\n", "\nOne, with a recommendation (see above).\n", 1), "neither \"None\" nor a **Qn. title** question"},
		{"no recommendation", strings.Replace(summarisedSpec, "Recommended: (a).\n\n**Q3.", "\n**Q3.", 1), `Q2 "Where the views go" has no Recommended: line`},
		{"no options", strings.Replace(summarisedSpec, "Options: (a) one slice; (b) two.\n", "", 1), `Q3 "Engine size" has no Options: line`},
		{"a gap", strings.Replace(summarisedSpec, "**Q3.", "**Q4.", 1), "Q4 follows Q2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, backends[1], true, sliceScript(t, c.spec)...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})
			a := e.attempts(t)[0]
			if a.Outcome != store.AttemptFailed || !strings.Contains(a.IntakeRejection, c.says) {
				t.Errorf("plan attempt = %+v, want it failed saying %q", a, c.says)
			}
			if _, ok := e.documents(t)["plan-1-spec"]; ok {
				t.Error("a refused spec was recorded")
			}
		})
	}
}

// TestBugPlanNeedsNoSummary: a plan no approval shows, as in the bug workflow, is recorded without a Summary block or
// an open-questions section.
func TestBugPlanNeedsNoSummary(t *testing.T) {
	bare := []byte("# Fix\n\nSort by priority.\n")
	e := newEnvDefinition(t, backends[1], loadWorkflow(t, "no-acceptance"),
		interptest.Expectation{Step: "research", Attempt: 1, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("researched", interptest.File{Path: "notes.md", Data: researchNotes})},
		interptest.Expectation{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{"context": "sha256:" + sum(researchNotes)}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: bare})},
		interptest.Expectation{Step: "build", Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": "sha256:" + sum(bare)}, Standards: []string{"standards/go.md"},
			Skills: []string{"go-tdd", "guildhall-contributing"}, Respond: interptest.Hang()})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	if d, ok := e.documents(t)["plan-1-spec"]; !ok || d.SHA256 != sum(bare) {
		t.Errorf("plan-1-spec = %+v, %v; want the bare spec recorded", d, ok)
	}
}

// TestRecordedSpecWithoutSummaryReplays: a plan's spec already recorded, such as d150 plan-1's from before the check,
// is not checked again: the result replays and the approval opens with the no-summary line and no open-questions
// part, since its questions are not in the checked shape.
func TestRecordedSpecWithoutSummaryReplays(t *testing.T) {
	old := strings.ReplaceAll(noSummary, "Options:", "Choices:")
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, b, true, sliceScript(t, old)...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			const p = "tasks/app/t7/plan-1/spec.md"
			file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := e.h.Records.PutDocument("earlier:plan-1-spec", store.DocumentRef{Repository: "app", TaskID: "t7", Name: "plan-1-spec", Path: p, SHA256: sum([]byte(old))}); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}),
				interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			q := e.decision(t, "accept-1").Question
			if !strings.Contains(q, "attempt 1.\n\nNo summary in this spec; read tasks/app/t7/plan-1/spec.md.\n\nInputs:\n") || strings.Contains(q, "Open questions") {
				t.Errorf("question = %q, want the no-summary line and no open-questions part", q)
			}
		})
	}
}
