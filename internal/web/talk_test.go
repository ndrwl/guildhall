package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// talkTask is an open task at grill-2 with one Director question, q1, and talk on every kind of tag: the question
// (inside its ask), the current stage (under the asks, two folded), an earlier stage (folded under its story node)
// and a tag with no ask or node (on Now, so nothing is lost).
func talkTask() Task {
	m := func(n int, author, tag, stage, text, when string) Message {
		return Message{Number: n, Author: author, Tag: tag, Stage: stage, Kind: "say", Text: text, At: *at(when)}
	}
	return Task{
		Repository: "guildhall", ID: "d145", Title: "Talk with the Director on the task page", State: "open", Stands: "waiting for your answer",
		Asks: []Ask{{ID: "q1", Kind: "question", Question: "Back off or stop while the tab is hidden?", Options: []string{"back-off", "stop"}, OpenedAt: at("2026-11-03 21:25")}},
		Story: []Node{
			{Title: "Grill · round 1", State: StateDone, Word: "finished", Start: at("2026-10-31 17:05"), End: at("2026-10-31 18:20"), Tag: "grill-1"},
			{Title: "Grill · round 2", State: StateRunning, Word: "running", Start: at("2026-11-03 21:00"), Tag: "grill-2"},
			{Title: "Question · q1", State: StateYou, Word: "waiting on you", Start: at("2026-11-03 21:25"), Ask: "q1", Tag: "q1"},
		},
		Conversation: []Message{
			// Out of order on purpose: the page orders by number.
			m(2, AuthorGuildmasterChat, "grill-1", "grill", "scope is *one task*", "2026-10-31 17:10"),
			m(1, AuthorDirector, "grill-1", "grill", "Round 1 is four questions on scope.", "2026-10-31 17:05"),
			m(3, AuthorDirector, "grill-2", "grill", "Grill-2 resumes the grill.", "2026-11-03 21:00"),
			m(4, AuthorGuildmasterChat, "grill-2", "grill", "keep lavish", "2026-11-03 21:01"),
			m(5, AuthorDirector, "grill-2", "grill", "Recorded as **R1**.", "2026-11-03 21:02"),
			m(6, AuthorGuildmasterPage, "grill-2", "grill", "can we look at the phone too?", "2026-11-03 21:08"),
			m(7, AuthorDirector, "grill-2", "grill", "Yes, Q6 is the phone order.", "2026-11-03 21:10"),
			m(8, AuthorDirector, "q1", "", "Should the poll back off while hidden?", "2026-11-03 21:25"),
			m(9, AuthorGuildmasterPage, "q1", "", "what does it cost?", "2026-11-03 21:30"),
			m(10, AuthorDirector, "plan-9", "plan", "A plan that is not on the page.", "2026-11-03 21:40"),
		},
		Tag:    "grill-2",
		ReadAt: *at("2026-11-03 21:44"),
	}
}

func talkServer(tasks ...Task) *Server {
	src := &fakeSource{tasks: map[string]Task{}}
	for _, t := range tasks {
		src.tasks[t.Repository+"/"+t.ID] = t
	}
	return New(Options{Source: src, Location: time.UTC, Host: testHost})
}

// TestTalkOnThePage: each message shows once, as a ledger row with its author, source and time over its Markdown
// body: inside its ask under "Talk on #q1" at #q1-talk, the current stage's under the asks with its earlier ones
// folded, an earlier stage's folded under its story node, and talk on a tag with no place of its own on Now. The
// rail counts the talk, the chips link to it, and the story says where on Now a node's talk is.
func TestTalkOnThePage(t *testing.T) {
	w := get(t, talkServer(talkTask()), "/guildhall/d145")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<dt>Talk</dt><dd>10 messages · last 21:40</dd>`,
		`<a class="aid" href="#q1">#q1</a><a class="tg" href="#q1-talk">q1</a>`,
		`<div class="talk" id="q1-talk"><p class="pick">Talk on #q1</p><div class="msg dir"><div class="mh"><b>Director</b><time>21:25</time></div><div class="mb"><p>Should the poll back off while hidden?</p>`,
		`<div class="msg"><div class="mh"><b>Guildmaster</b><span class="src">page</span><time>21:30</time></div><div class="mb"><p>what does it cost?</p>`,
		`<div class="talk" id="grill-2-talk"><p class="th">Talk on <a class="tg" href="#grill-2-talk">grill · grill-2</a> · oldest first</p><details class="fold"><summary>2 earlier messages on grill-2</summary>`,
		`<b>Guildmaster</b><span class="src">chat</span><time>21:01</time></div><div class="mb"><p>keep lavish</p>`,
		`<div class="mb"><p>Recorded as <strong>R1</strong>.</p>`,
		`<div class="talk" id="plan-9-talk"><p class="th">Talk on <a class="tg" href="#plan-9-talk">plan · plan-9</a> · oldest first</p><div class="msg dir">`,
		`<li class="node ok" id="story-grill-1">`,
		`<details class="fold talkf"><summary>Talk · 2 messages</summary><div class="talk"><div class="msg dir"><div class="mh"><b>Director</b><time>31 Oct 17:05</time></div><div class="mb"><p>Round 1 is four questions on scope.</p>`,
		`<b>Guildmaster</b><span class="src">chat</span><time>31 Oct 17:10</time></div><div class="mb"><p>scope is <em>one task</em></p>`,
		`<li class="node run" id="story-grill-2"><div class="nh"><span class="t">Grill · round 2</span><span class="st">running</span><time>3 Nov 21:00</time></div><div class="nb">Its talk is on Now, under the asks.</div>`,
		`<div class="nb">Its talk is on Now, inside the ask: 2 messages.</div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The grill-1 talk folds closed; every message shows once, in number order.
	if strings.Contains(body, `<details class="fold talkf" open>`) {
		t.Error("an earlier stage's talk is open")
	}
	for _, run := range [][]string{
		{"Round 1 is four questions on scope.", "scope is <em>one task</em>"},
		{"Grill-2 resumes the grill.", "keep lavish", "Recorded as <strong>R1</strong>.", "can we look at the phone too?", "Yes, Q6 is the phone order."},
	} {
		last := -1
		for _, text := range run {
			if n := strings.Count(body, text); n != 1 {
				t.Errorf("%q shows %d times, want once", text, n)
			}
			if i := strings.Index(body, text); i < last {
				t.Errorf("%q is out of order", text)
			} else {
				last = i
			}
		}
	}
	// The question's talk sits after its question and before its words box and tiles (L1 A).
	ask := body[strings.Index(body, `id="q1"`):]
	if q, talk, tiles := strings.Index(ask, `<p class="q">`), strings.Index(ask, `id="q1-talk"`), strings.Index(ask, `<p class="pick">Answer</p>`); q >= talk || talk >= tiles {
		t.Errorf("the ask's order is question %d, talk %d, tiles %d", q, talk, tiles)
	}
	if strings.Contains(body, "<script") {
		t.Error("the page holds a script")
	}
}

// TestTalkOnAClosedTask: a done task's talk stays readable, folded under its story nodes, and Now says the task is
// done with its talk read-only; a cancelled one says so too. A task with no talk shows no Talk line and no closed line.
func TestTalkOnAClosedTask(t *testing.T) {
	done := talkTask()
	done.State, done.Stands, done.Asks, done.Tag = "done", "done", nil, ""
	done.Story[1].State, done.Story[2].Ask = StateDone, ""
	cancelled := talkTask()
	cancelled.ID, cancelled.State, cancelled.Asks, cancelled.Tag = "d146", "cancelled", nil, ""
	quiet := talkTask()
	quiet.ID, quiet.Conversation = "d147", nil
	s := talkServer(done, cancelled, quiet)

	body := get(t, s, "/guildhall/d145").Body.String()
	for _, want := range []string{
		`<p class="stage done">This task is done and merged. Its talk stays below, read-only.</p>`,
		`<li class="node ok" id="story-grill-2">`,
		`<details class="fold talkf"><summary>Talk · 5 messages</summary>`,
		`<details class="fold talkf"><summary>Talk · 2 messages</summary>`,
		`<dt>Talk</dt><dd>10 messages · last 21:40</dd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the done page lacks %s", want)
		}
	}
	for _, bad := range []string{"<form", "<textarea", "Its talk is on Now"} {
		if strings.Contains(body, bad) {
			t.Errorf("the done page holds %q", bad)
		}
	}
	if body := get(t, s, "/guildhall/d146").Body.String(); !strings.Contains(body, `<p class="stage done">This task is cancelled. Its talk stays below, read-only.</p>`) {
		t.Error("the cancelled page does not say its talk stays")
	}
	body = get(t, s, "/guildhall/d147").Body.String()
	for _, bad := range []string{"<dt>Talk</dt>", `class="talk"`, "stays below"} {
		if strings.Contains(body, bad) {
			t.Errorf("a task with no talk shows %q", bad)
		}
	}
}

// TestTalkIsSafeMarkdown: a message's body renders through the documents' goldmark, so the hostile seeds of
// markdown_test.go leave no script, no event handler and no dangerous URL on the page.
func TestTalkIsSafeMarkdown(t *testing.T) {
	task := talkTask()
	seeds := []string{"<script>alert(1)</script>", "<img src=x onerror=alert(1)>", "inline <b onclick=x>b</b>",
		"[a](javascript:alert(1)) [b](vbscript:msgbox) [c](file:///etc/passwd) ![e](javascript:alert(3))",
		"| <b onmouseover=x> | b |\n|---|---|\n| c | d |", "<scr<script>ipt>", "</div></div><form action=//evil>"}
	for i, seed := range seeds {
		task.Conversation = append(task.Conversation, Message{Number: 100 + i, Author: AuthorDirector, Tag: "grill-2", Stage: "grill", Kind: "say", Text: seed, At: *at("2026-11-03 21:43")})
	}
	body := get(t, talkServer(task), "/guildhall/d145").Body.String()
	if bad := unsafeMarkup(body); bad != "" {
		t.Errorf("the page holds unsafe markup %s", bad)
	}
	for _, bad := range []string{"javascript:", "vbscript:", "file:", "//evil"} {
		if strings.Contains(body, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
}

// TestTalkSaysYouToTheGuildmaster: the Guildmaster's own page says You on their rows; anyone else's says Guildmaster.
func TestTalkSaysYouToTheGuildmaster(t *testing.T) {
	s := talkServer(talkTask())
	t.Run("guildmaster", func(t *testing.T) {
		v := s.taskView(talkTask(), pageState{identity: identity{Login: "gm@example.com", Answer: true}})
		if got := v.Asks[0].Talk[1].Who; got != "You" {
			t.Errorf("Who = %q, want You", got)
		}
	})
	t.Run("anyone else", func(t *testing.T) {
		v := s.taskView(talkTask(), pageState{})
		if got := v.Asks[0].Talk[1].Who; got != "Guildmaster" {
			t.Errorf("Who = %q, want Guildmaster", got)
		}
	})
}

// TestTalkRecommendationWaits: a Director recommendation on an open ask has the amber bar until the ask is answered.
func TestTalkRecommendationWaits(t *testing.T) {
	task := talkTask()
	task.Conversation = append(task.Conversation, Message{Number: 11, Author: AuthorDirector, Tag: "q1", Kind: KindRecommendation, Text: "Back off.", At: *at("2026-11-03 21:42")})
	s := talkServer()
	if v := s.taskView(task, pageState{}); v.Asks[0].Talk[2].Class != "msg dir wait" {
		t.Errorf("an open ask's recommendation is %q, want msg dir wait", v.Asks[0].Talk[2].Class)
	}
	task.Asks = nil
	v := s.taskView(task, pageState{})
	for _, n := range v.Story {
		for _, m := range n.Talk {
			if strings.Contains(m.Class, "wait") {
				t.Errorf("an answered ask's recommendation is %q", m.Class)
			}
		}
	}
}
