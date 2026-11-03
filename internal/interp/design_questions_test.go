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

// TestDesignOpenQuestions: a design whose `## Open questions` is missing, or holds a question with no Options: or
// Recommended: line or a numbering gap, is refused before anything is recorded, the error naming what is at fault;
// one that says "None" reaches an ask that says so.
func TestDesignOpenQuestions(t *testing.T) {
	const head = "# Design\n\nOption A: a drop-down. Recommend A.\n"
	for _, c := range []struct{ name, design, reason string }{
		{"no section", head, "no ## Open questions section"},
		{"no options", head + "\n## Open questions\n\n**Q1. Which control**\nRecommended: (a).\n", `Q1 "Which control" has no Options: line`},
		{"no recommendation", head + "\n## Open questions\n\n**Q1. Which control**\nOptions: (a) x; (b) y.\n", `Q1 "Which control" has no Recommended: line`},
		{"numbering gap", head + "\n## Open questions\n\n**Q1. A**\nOptions: x.\nRecommended: x.\n\n**Q3. C**\nOptions: x.\nRecommended: x.\n", "Q3 follows Q1"},
		{"none", head + "\n## Open questions\n\nNone.\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := designedEnv(t, backends[1], designBrief, interptest.File{Path: "design.md", Data: []byte(c.design)})
			ev := interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}
			if c.reason != "" {
				before := len(e.documents(t))
				assertRejected(t, e, ev, c.reason)
				if _, ok := e.documents(t)["designer-1-design"]; ok || len(e.documents(t)) != before {
					t.Error("a refused design was recorded")
				}
				return
			}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			if q := e.decision(t, "design-1").Question; !strings.Contains(q, ", decision design-1.\n\nOpen questions: none.\n\napproved makes ") {
				t.Errorf("question = %q, want the none line after the first line", q)
			}
		})
	}
}

// TestDesignRecordedBeforeTheCheckReplays: a design recorded before the design check required open questions stands
// on replay: the check does not run, and its ask opens without an open-questions part.
func TestDesignRecordedBeforeTheCheckReplays(t *testing.T) {
	old := []byte("# Design\n\nOption A: a drop-down. Recommend A.\n")
	e := designedEnv(t, backends[1], designBrief, interptest.File{Path: "design.md", Data: old})
	doc := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "designer-1-design", Path: "tasks/app/t7/designer-1/design.md", SHA256: sum(old)}
	file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(doc.Path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Records.PutDocument("document:old-design", doc); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
	if q := e.decision(t, "design-1").Question; strings.Contains(q, "Open questions") {
		t.Errorf("question = %q, want no open-questions part", q)
	}
}
