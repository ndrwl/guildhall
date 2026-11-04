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

const specPath = "tasks/guildhall/d122/plan-1/spec.md"

// approvalQuestion is a plan approval's question as render.Question writes it, with part after its first line.
func approvalQuestion(part string) string {
	q := "Approval needed for guildhall/d122, step accept (guildmaster-approval version 1), attempt 3.\n"
	if part != "" {
		q += "\n" + part + "\n"
	}
	return q + "\nInputs:\n- spec: " + specPath + " (sha256:abc)\n\nAnswer with one of: approved, changes_requested.\n"
}

// summaryPage renders the fixture task with a plan approval ask about plan-1-spec, whose file holds spec (the
// fixture's when empty) and whose record has its digest unless mismatch, and returns the page.
func summaryPage(t *testing.T, spec, question string, mismatch bool) string {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	if spec != "" {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(specPath)), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mismatch {
			s := sha256.Sum256([]byte(spec))
			sums[specPath] = hex.EncodeToString(s[:])
		}
	}
	task := fixtureTask(sums)
	task.Asks = append(task.Asks, Ask{ID: "accept-3", Kind: "approval", Question: question, Options: []string{"approved", "changes_requested"},
		Subject: "sha256:" + sums[specPath], SubjectDocument: "plan-1-spec", OpenedAt: at("2026-11-04 14:40")})
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="accept-3"`)
	if i < 0 {
		t.Fatal("the page has no approval ask")
	}
	return body[i:]
}

func once(t *testing.T, page, want string) {
	t.Helper()
	if n := strings.Count(page, want); n != 1 {
		t.Errorf("the ask holds %q %d times, want once", want, n)
	}
}

// TestPlanSummary: a plan approval ask shows its spec's Summary block, rendered, under the question's first line and
// once, whether or not the stored question carries a copy; a spec without one, or whose file does not match its
// digest, shows the fallback link and nothing of the file; a hostile summary is made safe by goldmark.
func TestPlanSummary(t *testing.T) {
	_, block, _ := strings.Cut(fixtureFiles[specPath], "## Summary\n\n")
	block, _, _ = strings.Cut(block, "\n\n## Goal")
	t.Run("from the spec", func(t *testing.T) {
		for name, q := range map[string]string{"question with a copy": approvalQuestion("**Summary**\n" + block), "question with an old copy": approvalQuestion("Plan summary:\n" + block), "older question": approvalQuestion("")} {
			page := summaryPage(t, "", q, false)
			once(t, page, `<div class="plan md"><p class="ph">Summary</p>`)
			once(t, page, `<span class="qn">Q1</span>Which address`)
			once(t, page, "<strong>Goal:</strong> one web page per task.")
			once(t, page, "<strong>Changed since the grill:</strong>")
			if strings.Contains(page, "No summary in this spec") || strings.Contains(page, "Plan summary:") || strings.Contains(page, "**Summary**") {
				t.Errorf("%s: the summary shows twice or with the fallback", name)
			}
			if !strings.Contains(page, "<pre>Inputs:\n- spec: ") {
				t.Errorf("%s: the rest of the question lost its inputs", name)
			}
			if q, p := strings.Index(page, `<p class="q">`), strings.Index(page, `<div class="plan md">`); q < 0 || p < q || strings.Index(page, `class="ctx"`) < p {
				t.Errorf("%s: the summary is not under the question's first line and before the fold", name)
			}
		}
	})
	t.Run("no block", func(t *testing.T) {
		page := summaryPage(t, "# The spec\n\n## Goal\n\nNo summary here.\n", approvalQuestion("No summary in this spec; read "+specPath+"."), false)
		once(t, page, `<a href="/guildhall/d122/doc/plan-1-spec">No summary in this spec · read plan-1-spec</a>`)
		once(t, page, "No summary in this spec")
		ask, _, _ := strings.Cut(page, `class="opts"`)
		once(t, ask, `href="/guildhall/d122/doc/plan-1-spec"`)
		if strings.Contains(page, `class="plan md"`) {
			t.Error("a spec with no block shows a summary")
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		page := summaryPage(t, "# Edited\n\n## Summary\n\n- **Goal:** SECRET\n", approvalQuestion(""), true)
		once(t, page, "No summary in this spec · read plan-1-spec")
		if strings.Contains(page, "SECRET") || strings.Contains(page, `class="plan md"`) {
			t.Error("a spec whose file does not match its digest is shown")
		}
	})
	t.Run("hostile", func(t *testing.T) {
		page := summaryPage(t, "# T\n\n## Summary\n\n- **Goal:** <script>alert(1)</script> [bad](javascript:alert(1)) <img src=x onerror=alert(1)>\n", approvalQuestion(""), false)
		once(t, page, `<div class="plan md">`)
		for _, bad := range []string{"<script", "javascript:", "<img"} {
			if strings.Contains(withoutOwnScripts(page), bad) {
				t.Errorf("the summary holds %q", bad)
			}
		}
	})
}

// TestDesignRoundNoSummary: a design round's approval ask, bound to its design document, is not a plan approval and
// shows neither a plan summary nor the no-summary fallback; its subject line still links the design.
func TestDesignRoundNoSummary(t *testing.T) {
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	const p = "tasks/guildhall/d122/design-1/design.html"
	task.Asks = append(task.Asks, Ask{ID: "design-1", Kind: "approval", Question: "Design round 1 for guildhall/d122: approve the design?\n\nThe design is design-1-design.",
		Options: []string{"approved", "changes_requested"}, Subject: "sha256:" + sums[p], SubjectDocument: "design-1-design", OpenedAt: at("2026-11-04 14:40")})
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="design-1"`)
	if i < 0 {
		t.Fatal("the page has no design ask")
	}
	page, _, _ := strings.Cut(body[i:], `class="opts"`)
	for _, bad := range []string{"No summary in this spec", `class="plan md"`, "Plan summary", `<p class="ph">Summary</p>`} {
		if strings.Contains(page, bad) {
			t.Errorf("a design round's ask holds %q", bad)
		}
	}
	once(t, page, `<a href="/guildhall/d122/doc/design-1-design">read design-1-design</a>`)
}

// TestStripSummary: the question's own summary and open-questions part come off the rest of the question, in today's
// shape and in the shape of asks recorded before d153, whose stored copy differs from the page's and is cut up to the
// next part.
func TestStripSummary(t *testing.T) {
	const qs = "Open questions in full:\n**Q1. A**\nOptions: x; y.\nRecommended: x."
	// design is the line render.DesignQuestion writes, whole, as every stored design question holds it.
	const design = "approved makes the design an input to plan, which runs again if it already ran; changes_requested reopens the grill with your answer."
	const part = "**Open questions · 1**\n\n**Q1. A**\n- ★ **(a) x** · Recommended.\n- (b) y\n\n" + spec.ReplyLine
	for _, c := range []struct{ rest, summary, questions, want string }{
		{"**Summary**\n- a\n\n- b\n\nInputs:\n- x", "- a\n\n- b", "", "Inputs:\n- x"},
		{"**Summary**\n- a\n\n" + part + "\n\nInputs:\n- x", "- a", part, "Inputs:\n- x"},
		{"**Summary**\n- a\n\nOpen questions: none.\n\nInputs:\n- x", "- a", "Open questions: none.", "Inputs:\n- x"},
		{"**Summary**\n- a, as stored\n\n" + part + "\n\nInputs:\n- x", "- a", part, "Inputs:\n- x"},
		{"No summary in this spec; read s.md.\n\n" + part + "\n\nInputs:\n- x", "", part, "Inputs:\n- x"},
		{"**Summary**\n- a\n\n" + part + "\n\nInputs:\n- x", "- a", "", part + "\n\nInputs:\n- x"},
		{"Plan summary:\n- a\n\n" + qs + "\n\nInputs:\n- x", "- a", part, "Inputs:\n- x"},
		{"Plan summary:\n- a\n\n" + qs + "\n\nInstructions:\nCheck.\n\nInputs:\n- x", "- a", part, "Instructions:\nCheck.\n\nInputs:\n- x"},
		{qs + "\n\n" + design + "\n\nInputs:\n- x", "", part, design + "\n\nInputs:\n- x"},
		{"Plan summary:\n- a\n\n- b\n\nInputs:\n- x", "- a\n\n- b", "", "Inputs:\n- x"},
		{"Plan summary:\n- a, as stored\n\nAnswer with one of: y.", "- a", "", "Answer with one of: y."},
		{"Plan summary:\n- a\n\nInputs:\n- x", "", "", "Plan summary:\n- a\n\nInputs:\n- x"},
		{"No summary in this spec; read s.md.\n\nInputs:\n- x", "", "", "Inputs:\n- x"},
		{"Inputs:\n- x", "- a", "", "Inputs:\n- x"},
		{"Plan summary:\n- a\n\n" + qs + "\n\nInputs:\n- x", "- a", qs, "Inputs:\n- x"},
		{"Plan summary:\n- a\n\nOpen questions: none.\n\nInputs:\n- x", "- a", "Open questions: none.", "Inputs:\n- x"},
		{"Plan summary:\n- a\n\n" + qs + ", as stored\n\nInputs:\n- x", "- a", qs, "Inputs:\n- x"},
		{"Plan summary:\n- a, as stored\n\n" + qs + "\n\nInputs:\n- x", "- a", qs, "Inputs:\n- x"},
		{"No summary in this spec; read s.md.\n\n" + qs + "\n\nInputs:\n- x", "", qs, "Inputs:\n- x"},
		{"Plan summary:\n- a\n\n" + qs + "\n\nInputs:\n- x", "- a", "", qs + "\n\nInputs:\n- x"},
		{qs + "\n\n" + design + "\n\nInputs:\n- x", "", qs, design + "\n\nInputs:\n- x"},
		{qs + ", as stored\n\n" + design + "\n\nInputs:\n- x", "", qs, design + "\n\nInputs:\n- x"},
		{design + "\n\nInputs:\n- x", "", qs, design + "\n\nInputs:\n- x"},
	} {
		if got := stripSummary(c.rest, c.summary, c.questions); got != c.want {
			t.Errorf("stripSummary(%q, %q, %q) = %q, want %q", c.rest, c.summary, c.questions, got, c.want)
		}
	}
}

// addressCard is the fixture spec's one open question as its card: the recommended option first, tinted and badged,
// with its reason beneath.
const addressCard = `<p class="ph oqh">Open questions <span class="n">1</span></p><div class="oqs">
<section class="oq"><p class="qt"><span class="qn">Q1</span>Which address</p><div class="qb"><p>The page needs an address to be served on.</p>
</div><ul class="ol"><li class="rec"><span class="lt">(a)</span><span class="ot">the tailnet one<span class="why">The Guildmaster reads it from a phone.</span></span><i class="badge">Recommended</i></li><li><span class="lt">(b)</span><span class="ot">localhost only</span></li></ul></section></div></div>`

// TestPlanSummaryQuestions: the summary box shows the spec's open questions as cards after the Summary, once, and the
// rest of the question does not repeat them, whether the stored question has today's copy, the copy of an ask
// recorded before d153, or none; a spec whose section says None says so, and an older spec with no readable section
// shows no part.
func TestPlanSummaryQuestions(t *testing.T) {
	doc := fixtureFiles[specPath]
	_, block, _ := strings.Cut(doc, "## Summary\n\n")
	block, _, _ = strings.Cut(block, "\n\n## Goal")
	_, qs, _ := strings.Cut(doc, "## Open questions\n\n")
	oldPart := "Open questions in full:\n" + strings.TrimSpace(qs)
	part, ok := spec.PlanPart([]byte(doc))
	if !ok || !strings.HasPrefix(part, "**Open questions · 1**\n\n**Q1. Which address**\n") {
		t.Fatalf("PlanPart = %q, %v", part, ok)
	}
	t.Run("cards", func(t *testing.T) {
		for name, q := range map[string]string{
			"question with a copy":      approvalQuestion("**Summary**\n" + block + "\n\n" + part),
			"question with an old copy": approvalQuestion("Plan summary:\n" + block + "\n\n" + oldPart),
			"older question":            approvalQuestion(""),
		} {
			page := summaryPage(t, "", q, false)
			box, rest, _ := strings.Cut(page, `class="ctx"`)
			once(t, box, addressCard)
			if p, o := strings.Index(box, `<p class="ph">Summary</p>`), strings.Index(box, "Open questions"); p < 0 || o < p {
				t.Errorf("%s: the questions are not after the summary", name)
			}
			if strings.Contains(rest, "Which address") || strings.Contains(rest, "Open questions") || strings.Contains(rest, "Reply **approve**") {
				t.Errorf("%s: the rest of the question repeats the questions", name)
			}
			if !strings.Contains(rest, "<pre>Inputs:\n- spec: ") {
				t.Errorf("%s: the rest of the question lost its inputs", name)
			}
		}
	})
	t.Run("none", func(t *testing.T) {
		none := strings.Replace(doc, "## Open questions\n\n"+qs, "## Open questions\n\nNone.\n", 1)
		page := summaryPage(t, none, approvalQuestion("**Summary**\n"+block+"\n\nOpen questions: none."), false)
		once(t, page, `<p class="ph">Open questions: none.</p></div>`)
		once(t, page, "Open questions: none.")
	})
	t.Run("no summary, questions", func(t *testing.T) {
		bare := "# T\n\n## Open questions\n\n" + qs
		page := summaryPage(t, bare, approvalQuestion("No summary in this spec; read "+specPath+".\n\n"+part), false)
		once(t, page, "No summary in this spec · read plan-1-spec")
		once(t, page, `<div class="plan md"><p class="ph oqh">Open questions <span class="n">1</span></p>`)
		once(t, page, "Which address")
	})
	t.Run("older spec", func(t *testing.T) {
		old := strings.Replace(doc, "Options:", "Choices:", 1)
		page := summaryPage(t, old, approvalQuestion(""), false)
		if strings.Contains(page, `class="ph oqh"`) || strings.Contains(page, `class="oq"`) {
			t.Error("a spec with no readable section shows an open-questions part")
		}
		once(t, page, `<div class="plan md"><p class="ph">Summary</p>`)
	})
}

// TestQuestionCards: a question that does not read cleanly, such as one with a free-text Recommended: line, shows
// its lines word for word with no marker, beside cards that did; option text, bodies and reasons are Markdown made
// safe by goldmark; and the answer row offers Approved as the recommended answer only when a card has a marked
// option.
func TestQuestionCards(t *testing.T) {
	const head = "# T\n\n## Summary\n\n- **Goal:** g.\n\n## Open questions\n\n"
	t.Run("fallback", func(t *testing.T) {
		page := summaryPage(t, head+"**Q1. Marked**\nOptions: (a) x; (b) y.\nRecommended: (b). Why.\n\n**Q2. Free**\nOptions: (a) the tailnet one; (b) localhost.\nRecommended: the tailnet one.\n", approvalQuestion(""), false)
		once(t, page, `<li class="rec"><span class="lt">(b)</span><span class="ot">y<span class="why">Why.</span></span><i class="badge">Recommended</i></li><li><span class="lt">(a)</span><span class="ot">x</span></li>`)
		once(t, page, `<section class="oq"><p class="qt"><span class="qn">Q2</span>Free</p><div class="qv"><p>Options: (a) the tailnet one; (b) localhost.
Recommended: the tailnet one.</p>
</div></section>`)
		once(t, page, `class="badge">Recommended</i></li>`)
	})
	t.Run("hostile", func(t *testing.T) {
		page := summaryPage(t, head+"**Q1. <b>Bold</b> title**\nBody with <script>alert(1)</script> and `code`.\nOptions: (a) **strong** <script>x</script>; (b) [bad](javascript:alert(1)) <img src=x onerror=alert(1)>.\nRecommended: (a). Because <script>y</script> `z`.\n", approvalQuestion(""), false)
		for _, bad := range []string{"<script", "javascript:", "<img", "<b>Bold"} {
			if strings.Contains(withoutOwnScripts(page), bad) {
				t.Errorf("the card holds %q", bad)
			}
		}
		once(t, page, `<span class="qn">Q1</span>&lt;b&gt;Bold&lt;/b&gt; title</p>`)
		once(t, page, `<span class="ot"><strong>strong</strong> <!-- raw HTML omitted -->x<!-- raw HTML omitted --><span class="why">Because <!-- raw HTML omitted -->y<!-- raw HTML omitted --> <code>z</code>.</span></span>`)
		once(t, page, `<span class="ot"><a href="">bad</a> <!-- raw HTML omitted --></span>`)
		once(t, page, "and <code>code</code>.")
	})
	t.Run("recommended answer", func(t *testing.T) {
		page := summaryPage(t, "", approvalQuestion(""), false)
		once(t, page, `<button class="opt pos rec" type="button" disabled><b>Approved <i class="badge">Recommended</i></b><span>take every recommendation above; the task goes on</span></button>`)
		once(t, page, `<b>Changes requested</b><span>send it back with your words, such as “Q2 (a)”</span>`)
		free := summaryPage(t, head+"**Q1. Free**\nOptions: (a) x; (b) y.\nRecommended: whichever.\n", approvalQuestion(""), false)
		none := summaryPage(t, head+"None.\n", approvalQuestion(""), false)
		for name, page := range map[string]string{"no marked option": free, "no questions": none} {
			if strings.Contains(page, " rec\"") || strings.Contains(page, "take every recommendation") {
				t.Errorf("%s: the answer row offers a recommended answer", name)
			}
			once(t, page, `<b>Approved</b><span>approve it; the task goes on</span>`)
		}
	})
}
