package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const designPath = "tasks/guildhall/d122/designer-5/design.md"

// designQuestion is a design round's question as render.DesignQuestion writes it, with part after its first line.
func designQuestion(part string) string {
	q := "Design decision needed for guildhall/d122, design round 5, decision design-5.\n"
	if part != "" {
		q += "\n" + part + "\n"
	}
	return q + "\napproved makes the design an input to plan, which runs again if it already ran; changes_requested reopens the grill with your answer.\n" +
		"\nInputs:\n- design: " + designPath + " (sha256:abc)\n\nAnswer with one of: approved, changes_requested.\n"
}

// designPage renders the fixture task with design-5's ask carrying question, its design file holding design and its
// record that file's digest unless mismatch, and returns the ask up to its options.
func designPage(t *testing.T, design, question string, mismatch bool) string {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(designPath)), []byte(design), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mismatch {
		s := sha256.Sum256([]byte(design))
		sums[designPath] = hex.EncodeToString(s[:])
	}
	task := fixtureTask(sums)
	for i := range task.Asks {
		if task.Asks[i].ID == "design-5" {
			task.Asks[i].Question, task.Asks[i].Subject = question, "sha256:"+sums[designPath]
		}
	}
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="design-5"`)
	if i < 0 {
		t.Fatal("the page has no design ask")
	}
	ask, _, _ := strings.Cut(body[i:], `class="opts"`)
	return ask
}

// TestDesignQuestions: a design round's ask shows its design's open questions in full in a box of their own, each
// line as written, once, with no plan summary, and the rest of the question keeps what each answer does but not the
// questions; a design whose section says None says so; an older design with no section, or one whose file does not
// verify, shows no box and keeps its question whole; hostile questions are made safe.
func TestDesignQuestions(t *testing.T) {
	const qs = "**Q1. Which control sorts the list**\nThe list needs one control.\nOptions: (a) a drop-down; (b) a table.\nRecommended: (a). It fits a phone."
	design := "# Design\n\nA drop-down.\n\n## Open questions\n\n" + qs + "\n"
	part := "Open questions in full:\n" + qs
	t.Run("in full", func(t *testing.T) {
		for name, q := range map[string]string{"question with a copy": designQuestion(part), "older question": designQuestion("")} {
			page := designPage(t, design, q, false)
			once(t, page, `<div class="plan md"><p class="ph">Open questions in full</p><div class="qs"><p><strong>Q1. Which control sorts the list</strong>
The list needs one control.
Options: (a) a drop-down; (b) a table.
Recommended: (a). It fits a phone.</p>
</div></div>`)
			box, rest, _ := strings.Cut(page, `class="ctx"`)
			if strings.Contains(box, "Plan summary") || strings.Contains(box, "No summary in this spec") {
				t.Errorf("%s: a design ask shows a plan summary", name)
			}
			if strings.Contains(rest, "Which control") || strings.Contains(page, "Open questions in full:") {
				t.Errorf("%s: the rest of the question repeats the questions", name)
			}
			if !strings.Contains(rest, "<pre>approved makes the design an input to plan") || !strings.Contains(rest, "Inputs:\n- design: ") {
				t.Errorf("%s: the rest of the question lost its parts:\n%s", name, rest)
			}
			if q, p := strings.Index(page, `<p class="q">`), strings.Index(page, `<div class="plan md"><p class="ph">Open questions`); q < 0 || p < q || strings.Index(page, `class="ctx"`) < p {
				t.Errorf("%s: the questions are not under the question's first line and before the fold", name)
			}
			once(t, page, `<a href="/guildhall/d122/doc/designer-5-design">read designer-5-design</a>`)
			once(t, page, `<p class="ph">Mock-up</p>`)
		}
	})
	t.Run("a stored copy that differs", func(t *testing.T) {
		page := designPage(t, design, designQuestion(part+" As stored."), false)
		_, rest, _ := strings.Cut(page, `class="ctx"`)
		once(t, page, "Which control")
		if !strings.Contains(rest, "<pre>approved makes the design ") {
			t.Errorf("the rest of the question lost what each answer does:\n%s", rest)
		}
	})
	t.Run("none", func(t *testing.T) {
		page := designPage(t, "# Design\n\n## Open questions\n\nNone.\n", designQuestion("Open questions: none."), false)
		once(t, page, `<div class="plan md"><p class="ph">Open questions: none.</p></div>`)
		once(t, page, "Open questions: none.")
	})
	for name, c := range map[string]struct {
		design   string
		mismatch bool
	}{"older design": {"# task design's output\n", false}, "digest mismatch": {design, true}} {
		t.Run(name, func(t *testing.T) {
			page := designPage(t, c.design, designQuestion(""), c.mismatch)
			if strings.Contains(page, "Open questions") || strings.Contains(page, "Which control") {
				t.Errorf("the ask shows open questions:\n%s", page)
			}
			if !strings.Contains(page, "<pre>approved makes the design ") {
				t.Error("the question is not kept whole")
			}
		})
	}
	t.Run("hostile", func(t *testing.T) {
		bad := "# D\n\n## Open questions\n\n**Q1. <script>alert(1)</script>**\nOptions: [x](javascript:alert(1)); <img src=x onerror=alert(1)>.\nRecommended: x.\n"
		page := designPage(t, bad, designQuestion(""), false)
		once(t, page, `<p class="ph">Open questions in full</p>`)
		for _, b := range []string{"<script", "javascript:", "<img"} {
			if strings.Contains(page, b) {
				t.Errorf("the questions hold %q", b)
			}
		}
	})
}
