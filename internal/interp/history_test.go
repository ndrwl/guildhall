package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestParseName(t *testing.T) {
	for _, c := range []struct {
		name     string
		document bool
		want     place
		ok       bool
	}{
		{"review-3", false, place{step: "review", round: 3}, true},
		{"review-x2", false, place{step: "review", round: 2, exit: true}, true},
		{"review-2-1", false, place{step: "review-2", round: 1}, true},
		{"review-x2-1", false, place{step: "review-x2", round: 1}, true},
		{"review-x2-x3", false, place{step: "review-x2", round: 3, exit: true}, true},
		{"review", false, place{}, false},
		{"review-0", false, place{}, false},
		{"review-01", false, place{}, false},
		{"review-x", false, place{}, false},
		{"build-2-head", true, place{step: "build", round: 2, part: "head"}, true},
		{"build-2-head-3", true, place{step: "build", round: 2, part: "head", index: 3}, true},
		{"build-2-1-head", true, place{step: "build-2", round: 1, part: "head"}, true},
		{"build-2-inputs", true, place{step: "build", round: 2, part: "inputs"}, true},
		{"review-x2-outcome", true, place{step: "review", round: 2, exit: true, part: "outcome"}, true},
		{"review-x2-1-outcome", true, place{step: "review-x2", round: 1, part: "outcome"}, true},
		{"accept-1-answer", true, place{step: "accept", round: 1, part: "answer"}, true},
		{"build-2", true, place{}, false},
		{"build-head", true, place{}, false},
		{"build-2-3", true, place{}, false},
		{"build-2-head-1", true, place{}, false},
	} {
		parse := parseDecision
		if c.document {
			parse = parseDocument
		}
		got, ok := parse(c.name)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("parse %q = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
		if ok && c.document && documentName(got) != c.name || ok && !c.document && decisionID(got.step, got.round, got.exit) != c.name {
			t.Errorf("%q does not format back from %+v", c.name, got)
		}
	}
}

// files is a document directory in memory for history tests: read checks the recorded digest.
type files map[string][]byte

func (f files) read(d store.DocumentRef) ([]byte, error) {
	data, ok := f[d.Path]
	if !ok {
		return nil, fmt.Errorf("document %s: file not found", d.Path)
	}
	if digest(data) != d.SHA256 {
		return nil, fmt.Errorf("document %s does not match its recorded digest", d.Path)
	}
	return data, nil
}

// doc records data as document name in f.
func (f files) doc(name string, data []byte) store.DocumentRef {
	f[name] = data
	return store.DocumentRef{Name: name, Path: name, SHA256: digest(data)}
}

func outcomeData(t *testing.T, v workflow.Verdict) []byte {
	o := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: v,
		Revisions: workflow.Revisions{Subject: workflow.Subject{CodeHead: strings.Repeat("0", 40)}, SpecDigest: "sha256:" + strings.Repeat("0", 64), StandardsRevision: "standards-1"}}
	if v == workflow.VerdictChangesRequested {
		o.Findings = []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "No red test shown."}}
	}
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var guildmasterReview = workflow.Step{ID: "review", Type: "review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)},
	On: map[string]workflow.Rule{changesRequested: {Goto: "build", Max: 1}}}

var artisanReview = workflow.Step{ID: "review", Type: "review", Version: 1, Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"reviewer"`)},
	On: map[string]workflow.Rule{changesRequested: {Goto: "build", Max: 1}}}

func definition(steps ...workflow.Step) *workflow.Definition {
	return &workflow.Definition{Steps: steps}
}

func renamed(s workflow.Step, id string) workflow.Step {
	s.ID = id
	return s
}

func wantOutput(t *testing.T, h *history, step, port, name, source string) {
	t.Helper()
	doc, src, err := h.output(step, port)
	if err != nil || doc.Name != name || src != source {
		t.Fatalf("output(%s, %s) = %s, %s, %v; want %s, %s", step, port, doc.Name, src, err, name, source)
	}
}

// TestOutputOrdersExitByOriginatingRound moved here unchanged from output_test.go when output joined the history view.
func TestOutputOrdersExitByOriginatingRound(t *testing.T) {
	for _, port := range []string{"spec", "outcome"} {
		t.Run(port, func(t *testing.T) {
			h := newHistory("", "", definition(guildmasterReview), nil, nil,
				[]store.DocumentRef{{Name: "review-x2-" + port}, {Name: "review-10-" + port}},
				[]store.Decision{
					{ID: "review-2", Answered: true, Choice: changesRequested},
					{ID: "review-10", Answered: true, Choice: changesRequested},
				})
			wantOutput(t, h, "review", port, "review-x2-"+port, "review-x2")
			h.addDocument(store.DocumentRef{Name: "review-11-" + port})
			wantOutput(t, h, "review", port, "review-11-"+port, "review-11")
		})
	}
}

// TestOutputInterleavesRoundsAndExits: an Artisan review's exits each order right after the blocking round
// they originate in, and every output and decision is seen as soon as it is added.
func TestOutputInterleavesRoundsAndExits(t *testing.T) {
	f := files{}
	h := newHistory("app", "t7", definition(artisanReview), f.read, nil, nil, nil)
	for n := 1; n <= 4; n++ {
		h.addAttempt(store.StageAttempt{Step: "review", Number: n, Outcome: store.AttemptSucceeded})
	}
	h.addDocument(f.doc("review-1-outcome", outcomeData(t, workflow.VerdictChangesRequested)))
	wantOutput(t, h, "review", "outcome", "review-1-outcome", "review-1")
	h.addDocument(f.doc("review-2-outcome", outcomeData(t, workflow.VerdictChangesRequested)))
	h.addDecision(store.Decision{ID: "review-x2", Answered: true, Choice: "continue"})
	if got := h.continued("review"); got != 1 {
		t.Fatalf("continued = %d, want 1", got)
	}
	wantOutput(t, h, "review", "outcome", "review-2-outcome", "review-2")
	// Round 3 approved without blocking, so exit 3 originates in round 4, the third blocking result.
	h.addDocument(f.doc("review-3-outcome", outcomeData(t, workflow.VerdictApproved)))
	h.addDocument(f.doc("review-4-outcome", outcomeData(t, workflow.VerdictChangesRequested)))
	h.addDocument(f.doc("review-x3-outcome", outcomeData(t, workflow.VerdictApproved)))
	wantOutput(t, h, "review", "outcome", "review-x3-outcome", "review-x3")
	if o, err := h.origin(artisanReview, 3); err != nil || o.round != 4 {
		t.Fatalf("origin of exit 3 = %+v, %v; want round 4", o, err)
	}
	h.addDocument(f.doc("review-5-outcome", outcomeData(t, workflow.VerdictApproved)))
	wantOutput(t, h, "review", "outcome", "review-5-outcome", "review-5")
}

// TestNumericStepSuffixes: steps whose ids extend another's with -<digits> or -x<digits> keep their own history.
func TestNumericStepSuffixes(t *testing.T) {
	def := definition(guildmasterReview, renamed(guildmasterReview, "review-2"), renamed(guildmasterReview, "review-x2"))
	h := newHistory("app", "t7", def, nil, nil,
		[]store.DocumentRef{{Name: "review-1-outcome"}, {Name: "review-2-1-outcome"}, {Name: "review-2-2-outcome"}, {Name: "review-x2-1-outcome"}},
		[]store.Decision{
			{ID: "review-1", Answered: true, Choice: "approved"},
			{ID: "review-2-1", Answered: true, Choice: changesRequested},
			{ID: "review-2-2", Answered: true, Choice: changesRequested},
			{ID: "review-2-x2", Answered: true, Choice: "continue"},
			{ID: "review-x2-1", Answered: true, Choice: changesRequested},
		})
	for _, c := range []struct {
		step, latest            string
		next, continued, rounds int
	}{
		{"review", "review-1", 2, 0, 1},
		{"review-2", "review-2-2", 3, 1, 2},
		{"review-x2", "review-x2-1", 2, 0, 1},
	} {
		if d := h.latestDecision(c.step); d == nil || d.ID != c.latest {
			t.Errorf("latestDecision(%s) = %+v, want %s", c.step, d, c.latest)
		}
		if got := h.nextRound(c.step); got != c.next {
			t.Errorf("nextRound(%s) = %d, want %d", c.step, got, c.next)
		}
		if got := h.continued(c.step); got != c.continued {
			t.Errorf("continued(%s) = %d, want %d", c.step, got, c.continued)
		}
		if got := len(h.rounds(c.step)); got != c.rounds {
			t.Errorf("rounds(%s) = %d, want %d", c.step, got, c.rounds)
		}
	}
	wantOutput(t, h, "review", "outcome", "review-1-outcome", "review-1")
	wantOutput(t, h, "review-2", "outcome", "review-2-2-outcome", "review-2-2")
	wantOutput(t, h, "review-x2", "outcome", "review-x2-1-outcome", "review-x2-1")
	if fs, err := h.firings(def.Steps[0]); err != nil || len(fs) != 0 {
		t.Errorf("firings(review) = %+v, %v; want none", fs, err)
	}
}

// TestMissingOriginIsExplicit: an exit whose originating history is missing or corrupt is an error, never an output.
func TestMissingOriginIsExplicit(t *testing.T) {
	t.Run("guildmaster round", func(t *testing.T) {
		h := newHistory("app", "t7", definition(guildmasterReview), nil, nil,
			[]store.DocumentRef{{Name: "review-1-outcome"}, {Name: "review-x2-outcome"}},
			[]store.Decision{{ID: "review-1", Answered: true, Choice: changesRequested}, {ID: "review-x2", Answered: true, Choice: "accept"}})
		if doc, _, err := h.output("review", "outcome"); err == nil || !strings.Contains(err.Error(), "originating") {
			t.Fatalf("output = %+v, %v; want an error naming the missing origin", doc, err)
		}
	})
	t.Run("artisan outcome", func(t *testing.T) {
		f := files{}
		h := newHistory("app", "t7", definition(artisanReview), f.read,
			[]store.StageAttempt{{Step: "review", Number: 1, Outcome: store.AttemptSucceeded}, {Step: "review", Number: 2, Outcome: store.AttemptSucceeded}},
			[]store.DocumentRef{f.doc("review-1-outcome", outcomeData(t, workflow.VerdictChangesRequested)), f.doc("review-x2-outcome", outcomeData(t, workflow.VerdictApproved))}, nil)
		if doc, _, err := h.output("review", "outcome"); err == nil {
			t.Fatalf("output = %+v with round 2's outcome missing", doc)
		}
		if _, err := h.origin(artisanReview, 2); err == nil {
			t.Fatal("origin of exit 2 found without round 2")
		}
		// Round 2's outcome recorded, but its file holds other content.
		h.addDocument(store.DocumentRef{Name: "review-2-outcome", Path: "review-2-outcome", SHA256: digest([]byte("other"))})
		f["review-2-outcome"] = outcomeData(t, workflow.VerdictChangesRequested)
		if _, _, err := h.output("review", "outcome"); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("output over a corrupt outcome = %v, want a digest error", err)
		}
		if _, err := h.firings(artisanReview); err == nil {
			t.Fatal("firings read a corrupt outcome")
		}
	})
	t.Run("unknown step", func(t *testing.T) {
		h := newHistory("app", "t7", definition(artisanReview), nil, nil, []store.DocumentRef{{Name: "other-x1-outcome"}}, nil)
		if _, _, err := h.output("other", "outcome"); err == nil {
			t.Fatal("an exit output of a step outside the workflow resolved")
		}
	})
}

// TestHistorySeesItsWrites: each read reflects every record added before it, the way an advance writes.
func TestHistorySeesItsWrites(t *testing.T) {
	h := newHistory("app", "t7", definition(guildmasterReview), nil, nil, nil, nil)
	if h.latestDecision("review") != nil || h.nextRound("review") != 1 || h.latestAttempt("review") != nil {
		t.Fatal("empty history has a round")
	}
	if _, _, err := h.output("review", "outcome"); err == nil {
		t.Fatal("empty history has an output")
	}
	d := h.addDecision(store.Decision{ID: "review-1"})
	if got := h.latestDecision("review"); got != d || h.nextRound("review") != 2 {
		t.Fatalf("latestDecision after a write = %+v", got)
	}
	d.Answered, d.Choice = true, changesRequested
	fs, err := h.firings(guildmasterReview)
	if err != nil || len(fs) != 1 || fs[0].round != 1 || fs[0].path != "tasks/app/t7/review-1/answer.md" || fs[0].sha256 != digest(nil) {
		t.Fatalf("firings after answering = %+v, %v", fs, err)
	}
	h.addDocument(store.DocumentRef{Name: "review-1-outcome"})
	wantOutput(t, h, "review", "outcome", "review-1-outcome", "review-1")
	h.addDecision(store.Decision{ID: "review-x1", Answered: true, Choice: "continue"})
	if h.continued("review") != 1 || h.nextRound("review") != 2 {
		t.Fatalf("an exit counted as a round: continued %d, next %d", h.continued("review"), h.nextRound("review"))
	}
	a := h.addAttempt(store.StageAttempt{Step: "build", Number: 1})
	if got := h.latestAttempt("build"); got != a || h.attempt("build", 1) != a {
		t.Fatalf("latestAttempt after a write = %+v", got)
	}
	if h.document("review-1-outcome") == nil || h.decision("review-x1") == nil {
		t.Fatal("a written record is not found by its key")
	}
}

func TestOriginOutOfRange(t *testing.T) {
	h := newHistory("app", "t7", definition(guildmasterReview), nil, nil, nil, []store.Decision{{ID: "review-1", Answered: true, Choice: changesRequested}})
	for _, k := range []int{0, 2} {
		if _, err := h.origin(guildmasterReview, k); !errors.Is(err, errNoOrigin) {
			t.Errorf("origin(%d) = %v, want errNoOrigin", k, err)
		}
	}
}
