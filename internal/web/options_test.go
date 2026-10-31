package web_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/cli"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// renderKinds maps the step kinds whose questions render.Question opens, and a design round's, to the decision kind
// each is opened with; the handoff wait has no kind of its own.
var renderKinds = map[string]store.DecisionKind{
	"guildmaster-approval": store.DecisionApproval,
	"design":               store.DecisionApproval,
	"review":               store.DecisionReview,
	"document-review":      store.DecisionReview,
	"handoff-wait":         "",
}

// TestOptionMeaningsCoverEveryOption: every option of every option set the engine, the rendered questions, grants
// and the Steward offer has a meaning and a tone in the page's table, so no answer tile ships without its line.
func TestOptionMeaningsCoverEveryOption(t *testing.T) {
	sets := map[store.DecisionKind][][]string{}
	for step, opts := range render.DecisionOptions() {
		kind, ok := renderKinds[step]
		if !ok {
			t.Errorf("render offers options for step kind %q, which this test does not map to a decision kind", step)
			continue
		}
		sets[kind] = append(sets[kind], opts)
	}
	for _, more := range []map[store.DecisionKind][][]string{interp.DecisionOptions(), store.DecisionOptions(), cli.StewardDecisionOptions()} {
		for kind, s := range more {
			sets[kind] = append(sets[kind], s...)
		}
	}
	for _, kind := range []store.DecisionKind{store.DecisionApproval, store.DecisionReview, store.DecisionLoopExit, store.DecisionMerge, store.DecisionGrant, store.DecisionFollowUps} {
		if len(sets[kind]) == 0 {
			t.Errorf("no option set of kind %s was exported", kind)
		}
	}
	n := 0
	for kind, ss := range sets {
		for _, opts := range ss {
			for _, o := range opts {
				m, ok := web.OptionMeaning(string(kind), o)
				if !ok || m.Text == "" || m.Tone == "" && (kind != store.DecisionFollowUps || o != "none") {
					t.Errorf("option %q of kind %q has meaning %+v, %v", o, kind, m, ok)
				}
				n++
			}
		}
	}
	if n < 20 {
		t.Errorf("checked only %d options", n)
	}
}

func TestUnknownOptionIsNeutral(t *testing.T) {
	if m, ok := web.OptionMeaning("loop-exit", "maybe"); ok || m != (web.Meaning{}) {
		t.Errorf("unknown option: %+v, %v", m, ok)
	}
	if m, ok := web.OptionMeaning("question", "yes"); ok || m.Tone != web.ToneNeutral {
		t.Errorf("unknown kind: %+v, %v", m, ok)
	}
	if got := web.OptionWord("changes_requested"); got != "Changes requested" {
		t.Errorf("OptionWord = %q", got)
	}
}
