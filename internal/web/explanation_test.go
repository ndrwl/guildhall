package web

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// explainedPage renders the fixture task with edit applied to its asks and returns the page at path.
func explainedPage(t *testing.T, path string, edit func(asks []Ask)) string {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	edit(task.Asks)
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, path)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	return w.Body.String()
}

// askCard is the ask id's card on page, up to its signature line.
func askCard(t *testing.T, page, id string) string {
	t.Helper()
	i := strings.Index(page, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("the page has no ask %s", id)
	}
	ask, _, _ := strings.Cut(page[i:], `<p class="who">`)
	return ask
}

func withAsk(id string, f func(a *Ask)) func([]Ask) {
	return func(asks []Ask) {
		for i := range asks {
			if asks[i].ID == id {
				f(&asks[i])
			}
		}
	}
}

// TestExplanationIsSafeMarkdown: the Director's explanation reaches the page only through RenderMarkdown, so raw
// HTML and script are dropped and an unsafe link is blanked, while a safe link and Markdown are kept.
func TestExplanationIsSafeMarkdown(t *testing.T) {
	const hostile = "**Bold** and <script>alert(1)</script> <img src=x onerror=alert(2)> <b onmouseover=x>hi</b>\n\n" +
		"[bad](javascript:alert(3)) [data](data:text/html,<script>4</script>) [good](https://example.com/spec)\n\n<div onclick=\"x\">raw</div>\n"
	page := explainedPage(t, "/guildhall/d122", withAsk("review--s2-x2", func(a *Ask) { a.Explanation.Text = hostile }))
	ask := askCard(t, page, "review--s2-x2")
	for _, bad := range []string{"<script", "onerror", "onmouseover", "onclick", "javascript:", "data:text/html", "<img", "<div onclick"} {
		if strings.Contains(strings.ToLower(ask), bad) {
			t.Errorf("the explanation shows %q:\n%s", bad, ask)
		}
	}
	for _, want := range []string{`<strong>Bold</strong>`, `<a href="https://example.com/spec">good</a>`, `<a href="">bad</a>`} {
		if !strings.Contains(ask, want) {
			t.Errorf("the explanation lacks %s:\n%s", want, ask)
		}
	}
}

// TestExplanationNotes: an ask with no explanation, and one whose explanation was written for an earlier version,
// show the grey note instead, and the stale text and its recommendation never show.
func TestExplanationNotes(t *testing.T) {
	page := explainedPage(t, "/guildhall/d122", withAsk("review--s2-x2", func(a *Ask) {
		a.Explanation = &Explanation{Text: "STALE WORDS", Recommend: "accept", At: time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC), Stale: true}
	}))
	ask := askCard(t, page, "review--s2-x2")
	if strings.Contains(page, "STALE WORDS") || strings.Contains(ask, "From the Director") {
		t.Errorf("the stale explanation shows:\n%s", ask)
	}
	if !strings.Contains(ask, `<p class="dxn">The Director&#39;s explanation was written for an earlier version of this ask, so it is not shown.`) {
		t.Errorf("the stale ask lacks its note:\n%s", ask)
	}
	if strings.Contains(ask, " rec\"") {
		t.Errorf("a stale explanation's recommendation marks a tile:\n%s", ask)
	}
	page = explainedPage(t, "/guildhall/d122", withAsk("review--s2-x2", func(a *Ask) { a.Explanation = nil }))
	ask = askCard(t, page, "review--s2-x2")
	if !strings.Contains(ask, `<p class="q">The slice 2 review asks whether to go on.</p><div class="dx"><p class="dxn">The Director has not explained this ask yet. `+
		`The full request is under the rest of the question; ask the Director in chat if you want its view first.</p></div>`) {
		t.Errorf("the unexplained ask lacks its note:\n%s", ask)
	}
	if strings.Contains(ask, " rec\"") || strings.Contains(ask, "Recommended") {
		t.Errorf("an unexplained loop exit recommends an answer:\n%s", ask)
	}
	// An ask with no rest of the question points at none.
	if merge := askCard(t, page, "deliver-1"); !strings.Contains(merge, `<p class="dxn">The Director has not explained this ask yet. Ask the Director in chat if you want its view first.</p>`) {
		t.Errorf("the merge ask's note:\n%s", merge)
	}
}

// TestDirectorRecommendation: the Director's recommended option gets d153's recommended mark and comes first, on an
// ask d153 marks nothing on; on a plan approval whose questions carry recommendations, d153's Approved keeps the mark
// and the Director's is ignored; an option the ask does not offer marks nothing.
func TestDirectorRecommendation(t *testing.T) {
	page := explainedPage(t, "/guildhall/d122", withAsk("grant-1", func(a *Ask) { a.Explanation.Stale, a.Explanation.Recommend = false, "reject" }))
	grant := askCard(t, page, "grant-1")
	if !strings.Contains(grant, `<div class="opts"><button class="opt neg rec" type="button" disabled><b>Reject <i class="badge">Recommended</i></b>`) ||
		strings.Count(grant, `class="badge">Recommended`) != 1 {
		t.Errorf("the grant does not mark the Director's reject first:\n%s", grant)
	}
	page = explainedPage(t, "/guildhall/d122", withAsk("accept--s2-1", func(a *Ask) {
		a.Explanation = &Explanation{Text: "Send it back.", Recommend: "changes_requested"}
	}))
	plan := askCard(t, page, "accept--s2-1")
	if !strings.Contains(plan, `<button class="opt pos rec" type="button" disabled><b>Approved <i class="badge">Recommended</i></b>`) ||
		strings.Contains(plan, `carry rec`) {
		t.Errorf("the plan approval's own mark does not win:\n%s", plan)
	}
	page = explainedPage(t, "/guildhall/d122", withAsk("grant-1", func(a *Ask) { a.Explanation.Stale, a.Explanation.Recommend = false, "maybe" }))
	if grant := askCard(t, page, "grant-1"); strings.Contains(grant, `class="badge">Recommended`) {
		t.Errorf("an option the ask does not offer is marked:\n%s", grant)
	}
}

// TestHeadline: the readable headline takes the question's first line's place, and that line leads the rest of the
// question, so the raw text is still on the page, folded; an ask with no headline keeps its first line.
func TestHeadline(t *testing.T) {
	page := explainedPage(t, "/guildhall/d122", func([]Ask) {})
	plan := askCard(t, page, "accept--s2-1")
	if !strings.Contains(plan, `<p class="q">The plan for slice 2 is ready for your approval.</p>`) ||
		!strings.Contains(plan, `<summary>The rest of the question</summary><div><pre>Approval needed for guildhall/d122, step accept--s2 (guildmaster-approval version 1), attempt 1.`) {
		t.Errorf("the plan approval's headline:\n%s", plan)
	}
	if merge := askCard(t, page, "deliver-1"); !strings.Contains(merge, `<p class="q">Merge d122?</p>`) {
		t.Errorf("an ask with no headline:\n%s", merge)
	}
}

// TestReaderPageExplains: the reader page draws its asks through the same ask block, explanation and all.
func TestReaderPageExplains(t *testing.T) {
	page := explainedPage(t, "/guildhall/d122/doc/plan-1-spec", func([]Ask) {})
	ask := askCard(t, page, "review--s2-x2")
	if !strings.Contains(ask, `<p class="dxh">From the Director`) || !strings.Contains(ask, `rec" type="button" disabled><b>Accept`) {
		t.Errorf("the reader page's ask:\n%s", ask)
	}
}

// TestOneAskBlock: an ask's headline, explanation, question cards, recommended mark and answer row each have one home
// in page.tmpl, every ask is drawn through the one ask block, and the page has no ask markup outside it. The option
// row is shared by the answers block and d153's Confirm tile under the ask's talk, the Director's talk
// recommendation, which is not a second answer row.
func TestOneAskBlock(t *testing.T) {
	src, err := os.ReadFile("page.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	// Each block runs from its define to the next one.
	blocks := map[string]string{}
	for _, part := range strings.Split(string(src), `{{define "`)[1:] {
		name, body, _ := strings.Cut(part, `"}}`)
		blocks[name] = body
	}
	for marker, home := range map[string]string{
		`class="dx"`: "explanation", `class="oq"`: "questions", `class="ol"`: "questions",
		`class="opts"`: "answers confirm", `class="badge">Recommended`: "answers questions", `template "answers"`: "ask", `template "explanation"`: "ask",
	} {
		var in []string
		for name, body := range blocks {
			if strings.Contains(body, marker) {
				in = append(in, name)
			}
		}
		slices.Sort(in)
		// The Recommended badge is drawn once on a question's row and once on an answer tile; the option row once
		// for the answer tiles and once for the Confirm tile.
		if want := strings.Fields(home); !slices.Equal(in, want) {
			t.Errorf("%s is in blocks %v, want only %v", marker, in, want)
		}
	}
	page := explainedPage(t, "/guildhall/d122", func([]Ask) {})
	asks := regexp.MustCompile(`<div class="ask[ "]`).FindAllString(page, -1)
	if n := len(asks); n != 6 {
		t.Errorf("%d ask cards, want one per ask of every kind (6)", n)
	}
	for _, m := range []string{`<p class="q">`, `<div class="dx">`, `<p class="pick">Answer</p>` + "\n" + `<div class="opts">`} {
		if n := strings.Count(page, m); n != 6 {
			t.Errorf("%d of %q, want one per ask (6)", n, m)
		}
	}
	// The only other option row is a Confirm tile, one per ask the Director recommends an answer to in talk.
	if opts, confirms := strings.Count(page, `<div class="opts">`), strings.Count(page, `<div class="recd">`); confirms == 0 || opts != 6+confirms {
		t.Errorf("%d option rows with %d Confirm tiles, want one answer row per ask (6) plus one per Confirm tile", opts, confirms)
	}
}
