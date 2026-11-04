package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/spec"
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
	ask, _, _ := strings.Cut(body[i:], `<p class="who">`)
	return ask
}

// TestDesignQuestions: a design round's ask shows its design's open questions as cards in a box of their own, once,
// with no Summary, the recommended option first and Approved as the recommended answer, and the rest of the question
// keeps what each answer does but not the questions, in today's shape or an older ask's; a design whose section says
// None says so; an older design with no section, or one whose file does not verify, shows no box and keeps its
// question whole; hostile questions are made safe.
func TestDesignQuestions(t *testing.T) {
	const qs = "**Q1. Which control sorts the list**\nThe list needs one control.\nOptions: (a) a drop-down; (b) a table.\nRecommended: (b). It fits a phone."
	design := "# Design\n\nA drop-down.\n\n## Open questions\n\n" + qs + "\n"
	part, ok := spec.SpecPart([]byte(design))
	if !ok {
		t.Fatal("the design's questions do not read")
	}
	t.Run("cards", func(t *testing.T) {
		for name, q := range map[string]string{"question with a copy": designQuestion(part), "question with an old copy": designQuestion("Open questions in full:\n" + qs), "older question": designQuestion("")} {
			page := designPage(t, design, q, false)
			once(t, page, `<div class="plan md"><p class="ph oqh">Open questions <span class="n">1</span></p><div class="oqs">
<section class="oq"><p class="qt"><span class="qn">Q1</span>Which control sorts the list</p><div class="qb"><p>The list needs one control.</p>
</div><ul class="ol"><li class="rec"><span class="lt">(b)</span><span class="ot">a table<span class="why">It fits a phone.</span></span><i class="badge">Recommended</i></li><li><span class="lt">(a)</span><span class="ot">a drop-down</span></li></ul></section></div></div>`)
			box, rest, _ := strings.Cut(page, `class="ctx"`)
			if strings.Contains(box, `<p class="ph">Summary</p>`) || strings.Contains(box, "No summary in this spec") {
				t.Errorf("%s: a design ask shows a plan summary", name)
			}
			if strings.Contains(rest, "Which control") || strings.Contains(rest, "Open questions") {
				t.Errorf("%s: the rest of the question repeats the questions", name)
			}
			once(t, rest, `<b>Approved <i class="badge">Recommended</i></b><span>take every recommendation above; the task goes on</span>`)
			if !strings.Contains(rest, "<pre>approved makes the design an input to plan") || !strings.Contains(rest, "Inputs:\n- design: ") {
				t.Errorf("%s: the rest of the question lost its parts:\n%s", name, rest)
			}
			if q, p := strings.Index(page, `<p class="q">`), strings.Index(page, `<div class="plan md"><p class="ph oqh">Open questions`); q < 0 || p < q || strings.Index(page, `class="ctx"`) < p {
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
		if strings.Contains(rest, "As stored") {
			t.Errorf("the rest of the question repeats the stored questions:\n%s", rest)
		}
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
			if strings.Contains(page, "take every recommendation") {
				t.Error("an ask with no questions offers a recommended answer")
			}
		})
	}
	t.Run("hostile", func(t *testing.T) {
		for name, bad := range map[string]string{
			"word for word": "# D\n\n## Open questions\n\n**Q1. <script>alert(1)</script>**\nOptions: [x](javascript:alert(1)); <img src=x onerror=alert(1)>.\nRecommended: x.\n",
			"options":       "# D\n\n## Open questions\n\n**Q1. <script>alert(1)</script>**\n<script>b</script>\nOptions: (a) [x](javascript:alert(1)); (b) <img src=x onerror=alert(1)>.\nRecommended: (a). <script>c</script>\n",
		} {
			page := designPage(t, bad, designQuestion(""), false)
			once(t, page, `<p class="ph oqh">Open questions <span class="n">1</span></p>`)
			for _, b := range []string{"<script", "javascript:", "<img"} {
				if strings.Contains(withoutOwnScripts(page), b) {
					t.Errorf("%s: the questions hold %q", name, b)
				}
			}
		}
	})
}
