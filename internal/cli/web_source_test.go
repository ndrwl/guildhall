package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestWebDocumentPlaces pins the page's reading of record names (H30): each document's group, step and round, for
// every name kind the engine writes, with input bindings left out and a name of no record form kept whole.
func TestWebDocumentPlaces(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{
		{ID: "build", Type: "build"}, {ID: "review", Type: "review"}, {ID: "publish", Type: "publish"},
		{ID: "deliver", Type: "deliver"},
	}}
	head := strings.Repeat("ab", 20)
	want := []struct {
		name, kind, step string
		round            int
	}{
		{"send-back-build-2", "Feedback", "", 0},
		{"send-back-build--s2-3", "Feedback", "", 0},
		{"build-2-head", "Build", "build", 2},
		{"build-2-head-3", "Build", "build", 2},
		{"build--s2-3-head", "Build", "build", 3},
		{"review-x2-answer", "Review", "review", 2},
		{"review--s2-x1-outcome", "Review", "review", 1},
		{"publish-1-pr", "Delivery", "publish", 1},
		{"deliver-1-merge", "Delivery", "deliver", 1},
		{"deliver--s2-1-merge", "Delivery", "deliver", 1},
		{"deliver-1-rebase-1", "Delivery", "deliver", 1},
		{"deliver-1-rebase-2", "Delivery", "deliver", 1},
		{"deliver-1-rebase-1-resolved", "Delivery", "deliver", 1},
		{"deliver-1-escalated-1", "Delivery", "deliver", 1},
		{"deliver-1-checks-" + head, "Delivery", "deliver", 1},
		{"design-1-mockup", "Design", "design", 1},
		{"hallmark-1-report", "Other", "hallmark", 1},
		{"follow-ups", "Other", "follow-ups", 0},
	}
	var fl taskFiles
	for _, name := range []string{"build-2-inputs", "build--s2-3-inputs"} {
		fl.Documents = append(fl.Documents, timedDocument{DocumentRef: store.DocumentRef{Name: name}})
	}
	for _, w := range want {
		fl.Documents = append(fl.Documents, timedDocument{DocumentRef: store.DocumentRef{Name: w.name}})
	}
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1"}}}
	got := webBuilder{ct: ct, fl: fl, def: def}.documents()
	if len(got) != len(want) {
		t.Fatalf("documents = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || g.Kind != w.kind || g.Step != w.step || g.Attempt != w.round {
			t.Errorf("document %d = %s %s %q %d, want %s %s %q %d", i, g.Name, g.Kind, g.Step, g.Attempt, w.name, w.kind, w.step, w.round)
		}
	}
}

// TestWebStepsAndFindingsByRecordPlace pins the stepper's reading of decision ids and a loop exit's findings by
// step and slice.
func TestWebStepsAndFindingsByRecordPlace(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{{ID: "review", Type: "review"}}}
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1"}}}
	tl := taskTimeline{
		Decisions: []timedDecision{{Decision: store.Decision{ID: "follow-ups"}}, {Decision: store.Decision{ID: "review-x1", Kind: store.DecisionReview, Choice: "approved", Answered: true}}},
		Findings: []timedFindings{
			{Document: "review-1-outcome", Findings: []workflow.Finding{{ID: "f1", Summary: "one"}}},
			{Document: "review--s2-1-outcome", Findings: []workflow.Finding{{ID: "f2", Summary: "other slice"}}},
		},
	}
	b := webBuilder{ct: ct, tl: tl, def: def}
	if st := b.steps(); len(st) != 1 || st[0].State != web.StateDone || st[0].Note != "review-x1 approved" {
		t.Errorf("steps = %+v", st)
	}
	if f := b.openFindings("review-x1"); len(f) != 1 || f[0].ID != "f1" {
		t.Errorf("open findings = %+v", f)
	}
}
