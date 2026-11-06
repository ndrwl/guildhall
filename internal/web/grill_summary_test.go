package web

import (
	"net/http"
	"strings"
	"testing"
)

// summaryText is a grill's closing summary in short Markdown.
const summaryText = "**Agreed**\n\n- Export CSV only.\n- Behind a flag."

// summaryTask is talkTask with grill-2 running and its closing summary open, after a summary answered with Request
// changes.
func summaryTask() Task {
	t := talkTask()
	t.DirectorStage, t.TagRunning = "grill", true
	t.Conversation = append(t.Conversation,
		Message{Number: 11, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindSummary, Text: "- Export CSV.", At: *at("2026-11-03 20:50")},
		Message{Number: 12, Author: AuthorGuildmasterPage, Tag: "grill-2", Stage: "grill", Kind: KindSummaryAnswer, Round: 11, Option: SummaryChangesRequested,
			Text: SummaryAnswerText(SummaryChangesRequested, "JSON *too*."), At: *at("2026-11-03 21:06")},
		Message{Number: 13, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindSummary, Text: summaryText, At: *at("2026-11-03 21:41")},
	)
	return t
}

// TestSummaryOnThePage: the open summary is one card after the asks, anchored at its attempt, with the summary in
// full, a Your words box, Confirm and Request changes; Needs you counts it as "Grill summary", the stage line points
// to it, and Now's time is from when it was put up if that is earlier than the asks. An answered summary folds to one
// Director row with its outcome and words, and the answer is no row of its own.
func TestSummaryOnThePage(t *testing.T) {
	w := get(t, answeringTalkServer(summaryTask()), "/guildhall/d145")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<div class="ask summary" id="grill-2"><form method="post" action="/guildhall/d145/summary"><input type="hidden" name="tag" value="grill-2"><input type="hidden" name="summary" value="13"><input type="hidden" name="token" value="t">`,
		`<span class="kind">Grill summary</span><a class="aid" href="#grill-2">#grill-2</a><a class="tg" href="#grill-2-talk">grill · grill-2</a><span class="since">since 21:41</span>`,
		`<div class="plan md"><p><strong>Agreed</strong></p>
<ul>
<li>Export CSV only.</li>
<li>Behind a flag.</li>
</ul>`,
		`<textarea id="grill-2-w" name="text" rows="3"></textarea>`,
		`<button class="opt pos rec" type="submit" name="act" value="confirm"><b>Confirm</b>`,
		`<button class="opt carry" type="submit" name="act" value="changes"><b>Request changes</b>`,
		`<li><a href="#grill-2"><b>Grill summary</b><span>#grill-2</span></a></li>`,
		`<span class="n you">2</span>`,
		`waiting on you · 2 asks`,
		`its grill summary is below, to confirm here.`,
		`<div class="msg dir summary" id="grill-2-summary-1"><div class="mh"><b>Director</b><time>20:50</time></div><div class="mb"><details class="fold"><summary>Grill summary · changes requested 21:06</summary><div class="md"><ul>
<li>Export CSV.</li>`,
		`<p class="ra">✓ Changes requested</p><div class="md"><p>JSON <em>too</em>.</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(body, "Changes requested\n\nJSON") || strings.Count(body, "JSON <em>too</em>") != 1 {
		t.Error("the summary's answer shows as a row of its own")
	}
	if strings.Index(body, `id="q1"`) > strings.Index(body, `<div class="ask summary"`) || strings.Index(body, `<div class="ask summary"`) > strings.Index(body, `id="grill-2-talk"`) {
		t.Error("the summary is not after the asks and before the stage's talk")
	}
	// Anyone else reads the summary with no form.
	ro := get(t, talkServer(summaryTask()), "/guildhall/d145").Body.String()
	if strings.Contains(ro, `action="/guildhall/d145/summary"`) || strings.Contains(ro, `value="confirm"`) || !strings.Contains(ro, `<div class="ask summary" id="grill-2">`) ||
		!strings.Contains(ro, "Only the Guildmaster's Tailscale login can answer here") {
		t.Error("the read-only page offers the summary's form")
	}
	// Put up before every ask, the summary gives Now its time.
	early := summaryTask()
	early.Conversation[len(early.Conversation)-1].At = *at("2026-11-03 21:20")
	if page := render(t, answeringTalkServer(), early, gmState); !strings.Contains(page, `<time>since 3 Nov 21:20</time>`) {
		t.Error("Now's time is not the summary's")
	}
}

// TestSummaryStates: a refused press shows its reason and words on the card, or at the top of Now when the summary
// is gone; a summary of a finished attempt, an earlier attempt or a closed task folds as not answered; a confirmed
// summary folds with its words, or none.
func TestSummaryStates(t *testing.T) {
	s := answeringTalkServer()
	st := gmState
	st.SummaryRefused, st.SummaryWords = "Request changes takes your words: write what to change in Your words", "kept <words>"
	refused := render(t, s, summaryTask(), st)
	if !strings.Contains(refused, `<div class="ask summary refused" id="grill-2">`) ||
		!strings.Contains(refused, "<b>Refused · nothing sent:</b> Request changes takes your words: write what to change in Your words.") ||
		!strings.Contains(refused, `name="text" rows="3">kept &lt;words&gt;</textarea>`) {
		t.Error("the refused summary does not show its reason and words")
	}

	for name, tk := range map[string]Task{
		"finished attempt": func() Task { t := summaryTask(); t.TagRunning = false; return t }(),
		"earlier attempt":  func() Task { t := summaryTask(); t.Tag = "research-1"; return t }(),
		"closed task": func() Task {
			t := summaryTask()
			t.State, t.Tag, t.Asks, t.DirectorStage, t.TagRunning = "done", "", nil, "", false
			return t
		}(),
	} {
		page := render(t, s, tk, gmState)
		if strings.Contains(page, `class="ask summary`) || strings.Contains(page, "<b>Grill summary</b>") || !strings.Contains(page, `<summary>⊘ Grill summary · not answered</summary>`) {
			t.Errorf("%s: the summary does not fold as not answered", name)
		}
		if name == "finished attempt" {
			gone := render(t, s, tk, st)
			if !strings.Contains(gone, "Your answer to the grill summary was not sent") || !strings.Contains(gone, "kept &lt;words&gt;</textarea>") {
				t.Error("a refusal with no open summary is lost")
			}
		}
	}

	confirmed := summaryTask()
	confirmed.Conversation = append(confirmed.Conversation, Message{Number: 14, Author: AuthorGuildmasterPage, Tag: "grill-2", Stage: "grill", Kind: KindSummaryAnswer,
		Round: 13, Option: SummaryConfirmed, Text: SummaryAnswerText(SummaryConfirmed, ""), At: *at("2026-11-03 21:43")})
	page := render(t, s, confirmed, gmState)
	if strings.Contains(page, `class="ask summary`) || !strings.Contains(page, `<summary>Grill summary · confirmed 21:43</summary>`) ||
		!strings.Contains(page, `<p class="ra">✓ Confirmed</p></details>`) || !strings.Contains(page, `<span class="n you">1</span>`) {
		t.Error("a confirmed summary does not fold with no words")
	}
}

// TestSummaryHostile: the summary and the words are rendered by goldmark's safe defaults or escaped.
func TestSummaryHostile(t *testing.T) {
	tk := talkTask()
	tk.TagRunning = true
	tk.Conversation = append(tk.Conversation,
		Message{Number: 11, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindSummary,
			Text: "<script>alert(1)</script>\n<img src=x onerror=alert(2)> [x](javascript:alert(3))", At: *at("2026-11-03 20:41")},
		Message{Number: 12, Author: AuthorGuildmasterPage, Tag: "grill-2", Stage: "grill", Kind: KindSummaryAnswer, Round: 11, Option: SummaryChangesRequested,
			Text: "Changes requested\n\n<script>alert(5)</script>", At: *at("2026-11-03 20:42")},
		Message{Number: 13, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindSummary, Text: "<b onclick=x>yes</b>", At: *at("2026-11-03 21:41")},
	)
	st := gmState
	st.SummaryRefused, st.SummaryWords = "<i>no</i>", "</textarea><script>alert(4)</script>"
	body := render(t, answeringTalkServer(), tk, st)
	for _, bad := range []string{"<script>alert", "<img", "javascript:", "<b onclick", "onerror=alert(2)>", "<i>no</i>"} {
		if strings.Contains(body, bad) {
			t.Errorf("the summary carries %s", bad)
		}
	}
}
