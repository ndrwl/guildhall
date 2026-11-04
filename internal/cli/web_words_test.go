package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestWebWordsCoverEveryStepType: every built-in step type has a noun and a story sentence, so each has a friendly
// name, a story sentence and a where-it-stands sentence, none of which shows a raw step id or a template token.
func TestWebWordsCoverEveryStepType(t *testing.T) {
	for _, k := range workflow.Kinds() {
		w, ok := webStepWords[k.Type]
		if !ok || w.noun == "" || w.done == "" {
			t.Errorf("step type %s has no words: %+v", k.Type, w)
			continue
		}
		id := "s-" + k.Type
		def := &workflow.Definition{Steps: []workflow.Step{{ID: id, Type: k.Type}}}
		next, _ := json.Marshal(clerkNext{Step: id + "--s2", Wait: "attempt", Attempt: 3})
		ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: store.TaskOpen}}}
		ct.status.Next = next
		b := webBuilder{ct: ct, def: def}
		at := timedAttempt{StageAttempt: store.StageAttempt{Step: id + "--s2", Number: 3, Outcome: store.AttemptSucceeded}}
		for what, got := range map[string]string{
			"name":     b.attemptName(id+"--s2", 3),
			"story":    b.attemptSentence(at, "", false),
			"stands":   b.where(nil, "", nil).Rest,
			"document": b.docLabel(id+"--s2-3-notes", nil),
		} {
			if got == "" || strings.Contains(got, id) || strings.ContainsAny(got, "{}") {
				t.Errorf("step type %s: %s = %q, want words with no raw id or token", k.Type, what, got)
			}
		}
		if got := b.attemptName(id+"--s2", 3); got != "Slice 2 "+w.noun+", attempt 3" {
			t.Errorf("step type %s: attempt name = %q", k.Type, got)
		}
	}
}

// TestWebWordsCoverEveryDecisionKind: every decision kind has its open, phrase, answered and void words, and an
// answer with each option of the page's option table says what was chosen.
func TestWebWordsCoverEveryDecisionKind(t *testing.T) {
	kinds := []store.DecisionKind{store.DecisionApproval, store.DecisionReview, store.DecisionLoopExit, store.DecisionMerge,
		store.DecisionQuestion, store.DecisionAudit, store.DecisionGrant, store.DecisionFollowUps, ""}
	def := &workflow.Definition{Steps: []workflow.Step{{ID: "accept", Type: "guildmaster-approval"}, {ID: "review", Type: "review"}}}
	b := webBuilder{def: def}
	ids := map[store.DecisionKind]string{store.DecisionApproval: "accept--s2-1", store.DecisionReview: "review-x1", store.DecisionLoopExit: "review--s2-x2"}
	for _, k := range kinds {
		w, ok := webAskWords[k]
		if !ok || w.open == "" || w.phrase == "" || w.answered == "" || w.void == "" {
			t.Errorf("kind %q has no words: %+v", k, w)
			continue
		}
		id := ids[k]
		if id == "" {
			id = "ask-1"
		}
		for _, dec := range []timedDecision{
			{Decision: store.Decision{ID: id, Kind: k}},
			{Decision: store.Decision{ID: id, Kind: k, Void: true}},
			{Decision: store.Decision{ID: id, Kind: k, Answered: true, Answer: "free words"}},
		} {
			if got := b.decisionSentence(dec); got == "" || strings.ContainsAny(got, "{}") || strings.Contains(got, id) {
				t.Errorf("kind %q: sentence = %q", k, got)
			}
		}
		if got := b.askPhrase(web.Ask{ID: id, Kind: string(k)}); got == "" || strings.ContainsAny(got, "{}") {
			t.Errorf("kind %q: phrase = %q", k, got)
		}
		// The ask card's headline is a whole sentence that names no record id.
		if got := b.askHeadline(web.Ask{ID: id, Kind: string(k)}); !strings.HasSuffix(got, ".") || got != upperFirst(got) || strings.ContainsAny(got, "{}") ||
			strings.Contains(got, id) {
			t.Errorf("kind %q: headline = %q", k, got)
		}
	}
	if got := b.askHeadline(web.Ask{ID: "accept--s2-1", Kind: string(store.DecisionApproval)}); got != "The plan for slice 2 is ready for your approval." {
		t.Errorf("plan approval headline = %q", got)
	}
	for _, c := range []struct {
		kind         store.DecisionKind
		id, choice   string
		want, phrase string
	}{
		{store.DecisionApproval, "accept-2", "approved", "You approved the plan", "approval of the plan"},
		{store.DecisionApproval, "accept--s2-1", "changes_requested", "You asked for changes to the plan for slice 2", "approval of the plan for slice 2"},
		{store.DecisionLoopExit, "review--s2-x2", "continue", "You chose another round of the slice 2 review", "whether to go on after the slice 2 review"},
		{store.DecisionMerge, "deliver-1", "hold", "You held the merge", "whether to merge"},
		{store.DecisionGrant, "grant-1", "reject", "You refused the grant", "a grant for an attempt"},
	} {
		dec := timedDecision{Decision: store.Decision{ID: c.id, Kind: c.kind, Answered: true, Choice: c.choice}}
		if got := b.decisionSentence(dec); got != c.want {
			t.Errorf("%s answered %s = %q, want %q", c.id, c.choice, got, c.want)
		}
		if got := b.askPhrase(web.Ask{ID: c.id, Kind: string(c.kind)}); got != c.phrase {
			t.Errorf("%s phrase = %q, want %q", c.id, got, c.phrase)
		}
	}
}

// TestWebDocumentLabels pins the friendly names of documents: kind, slice and round, the round shown when it is past
// the first or the task holds another of the kind, and a name of no record form kept whole.
func TestWebDocumentLabels(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{
		{ID: "grill", Type: "grill"}, {ID: "plan", Type: "plan"}, {ID: "accept", Type: "guildmaster-approval"}, {ID: "build", Type: "build"},
		{ID: "review", Type: "review"}, {ID: "deliver", Type: "deliver"}, {ID: "designer", Type: "design"},
	}}
	names := []string{"grill-1-notes", "grill-2-notes", "grill-3-notes", "plan-1-spec", "review-1-outcome", "review--s2-2-outcome",
		"designer-4-design", "designer-4-design-2", "build-2-head-3", "send-back-build-1", "deliver--s2-1-merge", "deliver-1-rebase-1", "follow-ups"}
	var fl taskFiles
	for _, n := range names {
		fl.Documents = append(fl.Documents, timedDocument{DocumentRef: store.DocumentRef{Name: n}})
	}
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1"}}}
	got := map[string]string{}
	for _, d := range (webBuilder{ct: ct, fl: fl, def: def}).documents() {
		got[d.Name] = d.Label
	}
	for name, want := range map[string]string{
		"grill-1-notes": "Grill notes, round 1", "grill-3-notes": "Grill notes, round 3", "plan-1-spec": "Spec",
		"review-1-outcome": "Review", "review--s2-2-outcome": "Slice 2 review, round 2", "designer-4-design": "Design, round 4",
		"designer-4-design-2": "Mock-up, round 4", "build-2-head-3": "Build head 3, round 2", "send-back-build-1": "Feedback to the build, round 1",
		"deliver--s2-1-merge": "Slice 2 merge record", "deliver-1-rebase-1": "Delivery rebase 1", "follow-ups": "follow-ups",
	} {
		if got[name] != want {
			t.Errorf("label of %s = %q, want %q", name, got[name], want)
		}
	}
}

// TestWebWhereItStands pins the where-it-stands sentence for the waits a task can be in, from its own words, never
// Clerk's phrase changed.
func TestWebWhereItStands(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{{ID: "build", Type: "build"}, {ID: "review", Type: "review"}, {ID: "grill", Type: "grill"}}}
	task := func(state store.TaskState, wait string) webBuilder {
		ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: state, CancelReason: "not needed"}}}
		if wait != "" {
			ct.status.Next, _ = json.Marshal(clerkNext{Step: "build--s2", Wait: wait, Attempt: 3})
		}
		return webBuilder{d: clerkData{now: time.Now()}, ct: ct, def: def}
	}
	asks := []web.Ask{{ID: "review--s2-x2", Kind: "loop-exit"}, {ID: "grant-1", Kind: "grant"}, {ID: "deliver-1", Kind: "merge", Deferred: true}}
	for _, c := range []struct {
		name  string
		b     webBuilder
		asks  []web.Ask
		stage string
		waits []string
		want  web.Where
	}{
		{"two asks", task(store.TaskOpen, "decision"), asks, "", nil,
			web.Where{Lead: "Waiting for you on 2 asks:", Rest: "whether to go on after the slice 2 review, and a grant for an attempt.", You: true}},
		{"deferred only", task(store.TaskOpen, "decision"), asks[2:], "", nil,
			web.Where{Lead: "Waiting for you, later:", Rest: "1 deferred ask, held until other tasks merge.", You: true}},
		{"stage", task(store.TaskOpen, "director-stage"), nil, "grill", nil,
			web.Where{Lead: "Waiting for you in the Director's session:", Rest: "the grill runs there with you.", You: true}},
		{"starting", task(store.TaskOpen, "attempt"), nil, "", nil, web.Where{Lead: "Moving:", Rest: "the slice 2 build is starting (attempt 3)."}},
		{"director", task(store.TaskOpen, "director"), nil, "", []string{"checks of PR #4 failed"}, web.Where{Lead: "Waiting on the Director:", Rest: "checks of PR #4 failed."}},
		{"checks", task(store.TaskOpen, "checks"), nil, "", nil, web.Where{Lead: "Moving:", Rest: "CI is running."}},
		{"paused", task(store.TaskOpen, "paused"), nil, "", nil, web.Where{Lead: "Paused:", Rest: "nothing runs until the Director resumes it."}},
		{"not started", task(store.TaskOpen, ""), nil, "", nil, web.Where{Lead: "Not started", Rest: "yet."}},
		{"done", task(store.TaskDone, ""), nil, "", nil, web.Where{Lead: "Done."}},
		{"cancelled", task(store.TaskCancelled, ""), nil, "", nil, web.Where{Lead: "Cancelled:", Rest: "not needed."}},
	} {
		if got := c.b.where(c.asks, c.stage, c.waits); got != c.want {
			t.Errorf("%s: where = %+v, want %+v", c.name, got, c.want)
		}
	}
}
