package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// roundText is a round of four questions in the guildhall-grill format.
const roundText = `**Q1. Where do rounds go?**
On the page, or in chat as today.
Recommended: on the page.

**Q2. Do answered questions fold?**
Recommended: yes, to one line with Change.

**Q3. What does Send round do with a question unanswered?**
It can always be pressed.

- refuse and save
- send what there is
Recommended: refuse, saving every box.

**Q4. Does the page advance the task?**
Recommended: no; the Director still finishes the stage.`

func TestParseRound(t *testing.T) {
	qs, err := ParseRound(roundText)
	if err != nil || len(qs) != 4 {
		t.Fatalf("ParseRound = %+v, %v", qs, err)
	}
	want := RoundQuestion{N: 3, Title: "What does Send round do with a question unanswered?", Body: "It can always be pressed.\n\n- refuse and save\n- send what there is",
		Recommended: "refuse, saving every box."}
	if qs[2] != want || qs[1].Body != "" || qs[0].Recommended != "on the page." {
		t.Fatalf("questions = %+v", qs)
	}
	for name, bad := range map[string]string{
		"empty":                 "",
		"preamble":              "Round 1, four questions.\n\n" + roundText,
		"no recommendation":     "**Q1. Where?**\nOn the page.",
		"blank recommendation":  "**Q1. Where?**\nRecommended: ",
		"out of order":          "**Q2. Where?**\nRecommended: here.",
		"skipped number":        "**Q1. A?**\nRecommended: a.\n\n**Q3. C?**\nRecommended: c.",
		"recommendation inside": "**Q1. A?**\nRecommended: a.\nMore body after it.",
	} {
		if _, err := ParseRound(bad); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	var many strings.Builder
	for i := 1; i <= MaxRoundQuestions+1; i++ {
		many.WriteString("**Q" + strconv.Itoa(i) + ". A?**\nRecommended: a.\n\n")
	}
	if _, err := ParseRound(many.String()); err == nil {
		t.Errorf("a round of %d questions parsed", MaxRoundQuestions+1)
	}
	// A heading inside a fence is the body's.
	qs, err = ParseRound("**Q1. Which output?**\n```\n**Q2. not a question**\n```\nRecommended: the first.")
	if err != nil || len(qs) != 1 {
		t.Fatalf("fenced head: %+v, %v", qs, err)
	}
}

func TestRoundAnswers(t *testing.T) {
	qs, _ := ParseRound(roundText)
	answers := []RoundAnswer{{Accepted: true}, {Accepted: true, Words: "and keep the gist short"}, {Words: "Refuse.\n\n**Q4. Does the page advance the task?**\nquoted"}, {Words: "No."}}
	text := RoundAnswersText(qs, answers)
	if !strings.HasPrefix(text, "**Q1. Where do rounds go?**\nAccepted: on the page.\n\n**Q2.") || !strings.Contains(text, "**Q4. Does the page advance the task?**\nYour words:\n\nNo.") {
		t.Fatalf("RoundAnswersText =\n%s", text)
	}
	got := ParseRoundAnswers(qs, text)
	if got[0] != answers[0] || got[1] != answers[1] || got[2] != answers[2] || got[3] != answers[3] {
		t.Fatalf("ParseRoundAnswers = %+v", got)
	}
	if tally := RoundTally(got); tally != "2 accepted, 2 your words" {
		t.Errorf("tally %q", tally)
	}
	// Words that begin with the accepted line stay words.
	quoting := []RoundAnswer{{Words: "Accepted: " + qs[0].Recommended + " but later"}, {Accepted: true}, {Accepted: true}, {Accepted: true}}
	if got := ParseRoundAnswers(qs, RoundAnswersText(qs, quoting)); got[0] != quoting[0] {
		t.Errorf("words quoting the accepted line = %+v", got[0])
	}
	if got := ParseRoundAnswers(qs, "not answers"); len(got) != 4 || got[0].Accepted {
		t.Errorf("answers that do not read = %+v", got)
	}
	if Unanswered([]int{9, 10}) != "Q9 and Q10" || Unanswered([]int{2}) != "Q2" || Unanswered([]int{1, 2, 3}) != "Q1, Q2 and Q3" {
		t.Error("Unanswered names them wrong")
	}
	if RoundID("guildhall", "d145", 11) == RoundID("guildhall", "d145", 12) || !strings.HasPrefix(RoundID("guildhall", "d145", 11), "page-round.") {
		t.Error("RoundID is not one id per round")
	}
}

// TestParseGivenAnswers: answers given in chat read per question, with Accepted or the Guildmaster's words; the text
// RoundAnswersText writes reads back as its answers; a wrong count, an empty answer, another answer than the
// recommendation under Accepted and a preamble are refused.
func TestParseGivenAnswers(t *testing.T) {
	qs, _ := ParseRound(roundText)
	given := "\n**Q1. Where?**\nAccepted\n\n**Q2. Fold?**\nAccepted: yes, to one line with Change.\nand keep the gist short\n\n" +
		"**Q3.**\nRefuse.\n```\n**Q4. not a head**\n```\n\n**Q4. Advance?**\nYour words:\n\nNo."
	got, err := ParseGivenAnswers(qs, given)
	want := []RoundAnswer{{Accepted: true}, {Accepted: true, Words: "and keep the gist short"}, {Words: "Refuse.\n```\n**Q4. not a head**\n```"}, {Words: "No."}}
	if err != nil || len(got) != 4 {
		t.Fatalf("ParseGivenAnswers = %+v, %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Q%d = %+v, want %+v", i+1, got[i], want[i])
		}
	}
	back, err := ParseGivenAnswers(qs, RoundAnswersText(qs, want))
	if err != nil || back[0] != want[0] || back[1] != want[1] || back[3] != want[3] {
		t.Errorf("RoundAnswersText's text reads back as %+v, %v", back, err)
	}
	for name, c := range map[string]struct{ text, says string }{
		"too few":         {"**Q1. A**\nAccepted", "answer 1 question; the round asks 4"},
		"out of order":    {"**Q2. A**\nAccepted", "headed Q2"},
		"preamble":        {"Round 1:\n" + given, "start with the first question"},
		"empty answer":    {strings.Replace(given, "No.", "", 1), "Q4 has no answer"},
		"another answer":  {strings.Replace(given, "Accepted\n", "Accepted: in chat\n", 1), `Q1 is Accepted: "in chat"`},
		"words only line": {strings.Replace(given, "Your words:\n\nNo.", "Your words:", 1), "Q4 has no answer"},
	} {
		if _, err := ParseGivenAnswers(qs, c.text); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.says)
		}
	}
}

// roundTask is talkTask with grill-2's round 1 sent and round 2 open and half filled: Q1 accepted, Q2 answered with
// words, Q3 holding words not yet an answer and Q4 empty.
func roundTask() Task {
	t := talkTask()
	t.DirectorStage = "grill"
	first := "**Q1. Scope?**\nRecommended: one task.\n\n**Q2. Lavish?**\nRecommended: out of scope."
	t.Conversation = append(t.Conversation,
		Message{Number: 11, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindRound, Text: first, At: *at("2026-11-03 20:50")},
		Message{Number: 12, Author: AuthorGuildmasterPage, Tag: "grill-2", Stage: "grill", Kind: KindRoundAnswers, Round: 11, At: *at("2026-11-03 21:06"),
			Text: "**Q1. Scope?**\nAccepted: one task.\n\n**Q2. Lavish?**\nYour words:\n\nkeep lavish"},
		Message{Number: 13, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindRound, Text: roundText, At: *at("2026-11-03 21:41")},
	)
	t.Drafts = []Draft{
		{Tag: "grill-2", Round: 13, Question: 1, Number: 5, Mark: DraftAccept, At: *at("2026-11-03 21:42")},
		{Tag: "grill-2", Round: 13, Question: 2, Number: 6, Mark: DraftWords, Text: "Yes, with the gist\nand Change.", At: *at("2026-11-03 21:42")},
		{Tag: "grill-2", Round: 13, Question: 3, Number: 8, Text: "Refuse <and> save", At: *at("2026-11-03 21:43")},
		// A draft of the sent round is never shown, nor counted in the form's drafts number.
		{Tag: "grill-2", Round: 11, Question: 1, Number: 9, Text: "stale", At: *at("2026-11-03 21:00")},
	}
	return t
}

// TestRoundOnThePage: the open round is one form after the asks, whose answered questions fold to one line with
// their gist and Change, whose box with words not yet an answer says when they were saved, and whose bar's Send
// round says how many are still to answer. Needs you counts it with its progress, and the sent round folds to one
// Director row with its tally.
func TestRoundOnThePage(t *testing.T) {
	w := get(t, answeringTalkServer(roundTask()), "/guildhall/d145")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<div class="ask round" id="grill-2">`,
		`<form method="post" action="/guildhall/d145/round"><input type="hidden" name="tag" value="grill-2"><input type="hidden" name="round" value="13"><input type="hidden" name="drafts" value="8"><input type="hidden" name="token" value="t">`,
		`<span class="kind">Grill round 2</span><a class="aid" href="#grill-2">#grill-2</a><a class="tg" href="#grill-2-talk">grill · grill-2</a><span class="since">2 of 4 answered</span>`,
		`<section class="oq rq done" id="grill-2-q1"><p class="qt"><span class="qn">Q1</span>Where do rounds go?</p><p class="rdone"><b>✓ Accepted</b> <span class="gist">on the page.</span><button class="lnk" type="submit" name="act" value="change-1">Change</button></p>`,
		`<b>✓ Your words, saved</b> <span class="gist">Yes, with the gist</span><button class="lnk" type="submit" name="act" value="change-2">Change</button>`,
		`<textarea id="grill-2-q3-w" name="text-3" rows="2">Refuse &lt;and&gt; save</textarea><p class="saved">saved 21:43 · not an answer yet</p>`,
		`<button class="opt pos rec" type="submit" name="act" value="accept-3"><b>Accept recommendation</b><span>refuse, saving every box.</span></button><button class="opt" type="submit" name="act" value="words-3">`,
		`<textarea id="grill-2-q4-w" name="text-4" rows="2"></textarea>
<div class="opts">`,
		`<button class="opt" type="submit" name="act" value="save"><b>Save, send later</b>`,
		`<button class="opt pos" type="submit" name="act" value="send"><b>Send round</b><span>2 still to answer</span></button>`,
		`<span>saves every box; nothing is sent</span></button><button class="opt rec" type="submit" name="act" value="accept-all"><b>Accept all recommendations</b><span>accepts every question with an empty box; your words are kept</span></button><button class="opt rec" type="submit" name="act" value="accept-send"><b>Accept all and send</b><span>accepts every question still to answer and sends the round</span></button><button class="opt pos" type="submit" name="act" value="send">`,
		`<li><a href="#grill-2"><b>Grill round 2</b> · 2 of 4<span>#grill-2</span></a></li>`,
		`<span class="n you">2</span>`,
		`its grill round 2 is below, to answer here.`,
		`<div class="msg dir round" id="grill-2-round-1"><div class="mh"><b>Director</b><time>20:50</time></div><div class="mb"><details class="fold"><summary>Round 1 · 2 questions · sent 21:06 · 1 accepted, 1 your words</summary>`,
		`<p class="ra">✓ Your words</p><div class="md"><p>keep lavish</p>`,
		`<div class="qb"><p>It can always be pressed.</p>
<ul>
<li>refuse and save</li>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The answered questions carry no box, so the round's form saves only the boxes it shows; the sent round's draft
	// and its answers entry never show as rows of their own.
	for _, not := range []string{`name="text-1"`, `name="text-2"`, "stale", `name="act" value="accept-1"`} {
		if strings.Contains(body, not) {
			t.Errorf("the page shows %s", not)
		}
	}
	if strings.Count(body, "keep lavish") != 2 {
		t.Errorf("the sent round's words show %d times, want once in the fold besides the chat message", strings.Count(body, "keep lavish"))
	}
	if strings.Index(body, `id="q1"`) > strings.Index(body, `id="grill-2"`) || strings.Index(body, `id="grill-2"`) > strings.Index(body, `id="grill-2-talk"`) {
		t.Error("the round is not after the asks and before the stage's talk")
	}

	golden := filepath.Join("testdata", "round.golden.html")
	if *update {
		if err := os.WriteFile(golden, w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/web -run TestRoundOnThePage -update", err)
	}
	if strings.ReplaceAll(body, "\r\n", "\n") != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("the round page differs from %s; review it and run with -update", golden)
	}

	// A round with no drafts posts 0.
	none := roundTask()
	none.Drafts = nil
	if fresh := render(t, answeringTalkServer(), none, gmState); !strings.Contains(fresh, `<input type="hidden" name="drafts" value="0">`) {
		t.Error("a round with no drafts does not post drafts 0")
	}

	// With every question answered or holding words, Accept all recommendations would do nothing, so it is not offered.
	worded := roundTask()
	worded.Drafts = append(worded.Drafts, Draft{Tag: "grill-2", Round: 13, Question: 4, Number: 10, Text: "Not yet.", At: *at("2026-11-03 21:44")})
	if page := render(t, answeringTalkServer(), worded, gmState); strings.Contains(page, `value="accept-all"`) || !strings.Contains(page, `value="send"`) {
		t.Error("Accept all recommendations shows with no empty unmarked question")
	}
	// Accept all and send answers a question holding words too, so it stays while any question is unanswered, and goes
	// once none is, when Send round alone sends.
	if page := render(t, answeringTalkServer(), worded, gmState); !strings.Contains(page, `value="accept-send"`) {
		t.Error("Accept all and send does not show with unanswered questions holding words")
	}
	all := roundTask()
	for _, n := range []int{3, 4} {
		all.Drafts = append(all.Drafts, Draft{Tag: "grill-2", Round: 13, Question: n, Number: 10 + n, Mark: DraftAccept, At: *at("2026-11-03 21:44")})
	}
	if page := render(t, answeringTalkServer(), all, gmState); strings.Contains(page, `value="accept-send"`) || !strings.Contains(page, `every question answered`) {
		t.Error("Accept all and send shows with every question answered")
	}

	// Anyone else reads the round with no form and no boxes.
	ro := get(t, talkServer(roundTask()), "/guildhall/d145").Body.String()
	if strings.Contains(ro, "<textarea") || strings.Contains(ro, `action="/guildhall/d145/round"`) || !strings.Contains(ro, `<div class="ask round" id="grill-2">`) {
		t.Error("the read-only page offers the round's form")
	}
	if strings.Contains(ro, "Accept all recommendations") || strings.Contains(ro, "Accept all and send") {
		t.Error("the read-only page offers Accept all recommendations")
	}
}

// TestRoundStates: every question answered says Send round sends; a refused press shows its reason and the words the
// boxes held; a round of an earlier attempt, or of a closed task, folds as not sent; and a closed task renders no form.
func TestRoundStates(t *testing.T) {
	s := answeringTalkServer()
	tk := roundTask()
	tk.Drafts = append(tk.Drafts[:2], Draft{Tag: "grill-2", Round: 13, Question: 3, Mark: DraftAccept}, Draft{Tag: "grill-2", Round: 13, Question: 4, Mark: DraftWords, Text: "No."})
	v := s.taskView(tk, gmState)
	if v.Round == nil || v.Round.Left != 0 || v.Round.Progress() != "4 of 4" {
		t.Fatalf("round = %+v", v.Round)
	}

	st := gmState
	st.RoundRefused, st.RoundBoxes = "Q3 and Q4 have no answer yet; your words are saved", map[int]string{3: "kept <words>"}
	refused := render(t, s, roundTask(), st)
	if !strings.Contains(refused, `<div class="ask round refused" id="grill-2">`) || !strings.Contains(refused, "<b>Refused · nothing sent:</b> Q3 and Q4 have no answer yet; your words are saved.") ||
		!strings.Contains(refused, `name="text-3" rows="2">kept &lt;words&gt;</textarea>`) {
		t.Errorf("the refused round does not show its reason and words")
	}
	// A refusal when no round is open shows at the top of Now with the words.
	later := roundTask()
	later.Tag = "research-1"
	gone := render(t, s, later, st)
	if !strings.Contains(gone, "Your round was not saved") || !strings.Contains(gone, "Q3: kept &lt;words&gt;") {
		t.Error("a refusal with no open round is lost")
	}
	if !strings.Contains(gone, `<summary>⊘ Round 2 · 4 questions · not sent</summary>`) || strings.Contains(gone, `class="ask round`) {
		t.Error("a round of an earlier attempt is not folded as not sent")
	}

	closed := roundTask()
	closed.State, closed.Tag, closed.Drafts, closed.Asks, closed.DirectorStage = "done", "", nil, nil, ""
	page := render(t, s, closed, gmState)
	if strings.Contains(page, "<form") || strings.Contains(page, "<textarea") {
		t.Error("a closed task renders a form")
	}
	if !strings.Contains(page, `<summary>⊘ Round 2 · 4 questions · not sent; its saved answers were dropped</summary>`) {
		t.Error("a closed task's unsent round does not say its answers were dropped")
	}
	if !strings.Contains(page, `<summary>Round 1 · 2 questions · sent 21:06 · 1 accepted, 1 your words</summary>`) {
		t.Error("a closed task's sent round is not readable")
	}
}

// TestRoundHostile: the round's titles, bodies, recommendations and answers are escaped or rendered by goldmark's
// safe defaults.
func TestRoundHostile(t *testing.T) {
	tk := talkTask()
	tk.Conversation = append(tk.Conversation, Message{Number: 11, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: KindRound,
		Text: "**Q1. <script>alert(1)</script>**\n<img src=x onerror=alert(2)> [x](javascript:alert(3))\nRecommended: <b onclick=x>yes</b>", At: *at("2026-11-03 21:41")})
	tk.Drafts = []Draft{{Tag: "grill-2", Round: 11, Question: 1, Text: "</textarea><script>alert(4)</script>"}}
	body := render(t, answeringTalkServer(), tk, gmState)
	for _, bad := range []string{"<script>alert", "<img", "javascript:", "<b onclick", "onerror=alert(2)>"} {
		if strings.Contains(body, bad) {
			t.Errorf("the round carries %s", bad)
		}
	}
}

// render is the task page of tk for st.
func render(t *testing.T, s *Server, tk Task, st pageState) string {
	t.Helper()
	var b strings.Builder
	if err := pages.ExecuteTemplate(&b, "task", s.taskView(tk, st)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
