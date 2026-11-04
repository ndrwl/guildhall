package web

import (
	"context"
	"strings"
	"testing"
)

// gmIdentity tells every request is from the Guildmaster's login.
type gmIdentity struct{}

func (gmIdentity) Login(context.Context, string) (string, error) { return "gm@example.com", nil }

// nopAnswerer answers nothing; the view tests only render.
type nopAnswerer struct{}

func (nopAnswerer) Answer(Answer) error         { return nil }
func (nopAnswerer) Send(Answer) (string, error) { return "", nil }
func (nopAnswerer) Say(Answer) (string, error)  { return "", nil }
func (nopAnswerer) Round(RoundPost) error       { return nil }

// answeringTalkServer is talkServer answering for the Guildmaster's login.
func answeringTalkServer(tasks ...Task) *Server {
	s := talkServer(tasks...)
	s.o.Identity, s.o.Guildmaster, s.o.Answerer = gmIdentity{}, "gm@example.com", nopAnswerer{}
	s.token = "t"
	return s
}

var gmState = pageState{identity: identity{Login: "gm@example.com", Answer: true}, Token: "t"}

// recommended is talkTask with the Director recommending stop on q1, after the Guildmaster's question there.
func recommended(words string) Task {
	t := talkTask()
	t.Conversation = append(t.Conversation, Message{Number: 11, Author: AuthorDirector, Tag: "q1", Kind: KindRecommendation,
		Text: "It costs one request a minute; I recommend **stop**.", Option: "stop", Words: words, At: *at("2026-11-03 21:42")})
	return t
}

// TestConfirmTile: the Director's latest recommendation on an ask is one Confirm tile of its form, in an amber box
// under its talk, before Your words and the tiles (L1 A), and the words box is pre-filled with the recommendation's
// words or else the Guildmaster's latest words on the ask; anyone else sees the tile disabled.
func TestConfirmTile(t *testing.T) {
	s := answeringTalkServer()
	v := s.taskView(recommended(""), gmState)
	c := v.Asks[0].Confirm
	if c == nil || c.Number != 11 || c.Option != "stop" || c.Word != "Stop" || !c.Form || c.Decision != "q1" {
		t.Fatalf("Confirm = %+v", c)
	}
	if got := v.Asks[0].Prefill; got != "what does it cost?" {
		t.Errorf("Prefill = %q, want the Guildmaster's latest words on the ask", got)
	}
	if got := s.taskView(recommended("stop, it is cheap enough"), gmState).Asks[0].Prefill; got != "stop, it is cheap enough" {
		t.Errorf("Prefill = %q, want the recommendation's words", got)
	}

	body := get(t, answeringTalkServer(recommended("")), "/guildhall/d145").Body.String()
	for _, want := range []string{
		`<div class="msg dir wait"><div class="mh"><b>Director</b><span class="rk">recommends <b>Stop</b></span><time>21:42</time></div>`,
		`<div class="recd"><p class="ph">Recommended by the Director</p><div class="opts"><button class="opt rec" type="submit" name="confirm" value="11"><b>Confirm: Stop</b><span>answers #q1 with Stop and your words below</span></button></div></div></div>`,
		`<textarea id="w-q1" name="text" rows="3">what does it cost?</textarea>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	confirm, words, tiles := strings.Index(body, `name="confirm"`), strings.Index(body, `id="w-q1"`), strings.Index(body, `name="option" value="back-off"`)
	if confirm < 0 || words < confirm || tiles < words {
		t.Errorf("the order is Confirm %d, Your words %d, tiles %d; want them in that order", confirm, words, tiles)
	}

	ro := talkServer(recommended(""))
	body = get(t, ro, "/guildhall/d145").Body.String()
	if !strings.Contains(body, `<button class="opt rec" type="button" disabled><b>Confirm: Stop</b>`) || strings.Contains(body, `name="confirm"`) {
		t.Error("a read-only page offers Confirm")
	}

	// A recommendation of an answer the ask does not offer, or by anyone but the Director, offers no Confirm; the
	// latest recommendation is the one offered.
	other := recommended("")
	other.Conversation[len(other.Conversation)-1].Option = "pause"
	if v := s.taskView(other, gmState); v.Asks[0].Confirm != nil {
		t.Errorf("a recommendation of no option offers %+v", v.Asks[0].Confirm)
	}
	later := recommended("")
	later.Conversation = append(later.Conversation, Message{Number: 12, Author: AuthorDirector, Tag: "q1", Kind: KindRecommendation, Text: "On reflection, back off.",
		Option: "back-off", At: *at("2026-11-03 21:43")})
	if c := s.taskView(later, gmState).Asks[0].Confirm; c == nil || c.Number != 12 || c.Option != "back-off" {
		t.Errorf("the later recommendation is not offered: %+v", c)
	}
}

// TestConfirmedRow: a confirmation's row says "↩ Confirmed: <answer>" over the words it carried.
func TestConfirmedRow(t *testing.T) {
	task := recommended("")
	task.Asks = nil
	task.Conversation = append(task.Conversation, Message{Number: 12, Author: AuthorGuildmasterPage, Tag: "q1", Kind: KindConfirmed, Text: "stop\n\nit is *cheap*",
		Option: "stop", Confirms: 11, At: *at("2026-11-03 21:44")})
	body := get(t, talkServer(task), "/guildhall/d145").Body.String()
	want := `<div class="msg conf"><div class="mh"><b>Guildmaster</b><span class="src">page</span><time>21:44</time></div><div class="mb"><p>↩ Confirmed: <b>Stop</b></p>
<p>it is <em>cheap</em></p>`
	if !strings.Contains(body, want) {
		t.Errorf("the page lacks the confirmation's row %s", want)
	}
	if strings.Contains(body, "msg dir wait") {
		t.Error("a confirmed recommendation still waits")
	}
	task.Conversation[len(task.Conversation)-1].Text = "stop"
	body = get(t, talkServer(task), "/guildhall/d145").Body.String()
	if !strings.Contains(body, `<p>↩ Confirmed: <b>Stop</b></p>
</div>`) {
		t.Error("a confirmation without words shows more than its answer")
	}
}

// TestRecommendedQuestion: a question the Director opened with a recommended option marks that tile, first.
func TestRecommendedQuestion(t *testing.T) {
	task := talkTask()
	task.Asks[0].Recommended = "stop"
	v := talkServer().taskView(task, pageState{})
	tiles := v.Asks[0].Tiles
	if len(tiles) != 2 || tiles[0].Option != "stop" || !tiles[0].Rec || tiles[1].Rec {
		t.Fatalf("tiles = %+v", tiles)
	}
	body := get(t, talkServer(task), "/guildhall/d145").Body.String()
	if !strings.Contains(body, `<b>Stop <i class="badge">Recommended</i></b>`) {
		t.Error("the recommended tile has no label")
	}
}

// TestSayBox: the Guildmaster's page has the Say box at the end of the current stage's talk, even with no talk on it
// yet, and no one else's does; a closed task has none.
func TestSayBox(t *testing.T) {
	s := answeringTalkServer(talkTask())
	body := get(t, s, "/guildhall/d145").Body.String()
	want := `<form class="say" id="say" method="post" action="/guildhall/d145/say"><input type="hidden" name="tag" value="grill-2"><input type="hidden" name="token" value="t">`
	if !strings.Contains(body, want) {
		t.Fatalf("the page lacks the Say box %s", want)
	}
	if !strings.Contains(body, `<label class="yw" for="say-text">Say to the Director about grill-2 <span>answers nothing</span></label>`) ||
		!strings.Contains(body, `<b>Send to the Director</b>`) {
		t.Error("the Say box is not worded as design-2 draws it")
	}
	talk := strings.Index(body, `<div class="talk" id="grill-2-talk">`)
	if say := strings.Index(body, want); talk < 0 || say < talk {
		t.Errorf("the Say box is not at the end of the stage's talk (talk %d, box %d)", talk, say)
	}

	quiet := talkTask()
	quiet.Conversation = nil
	quiet.TagStage = "grill"
	body = get(t, answeringTalkServer(quiet), "/guildhall/d145").Body.String()
	if !strings.Contains(body, `<div class="talk" id="grill-2-talk"><p class="th">Talk on <a class="tg" href="#grill-2-talk">grill · grill-2</a></p><form class="say"`) {
		t.Errorf("a stage with no talk has no Say box under its heading")
	}

	if body := get(t, talkServer(talkTask()), "/guildhall/d145").Body.String(); strings.Contains(body, `class="say"`) {
		t.Error("a read-only page has the Say box")
	}
	done := talkTask()
	done.State, done.Asks, done.Tag = "done", nil, ""
	if body := get(t, answeringTalkServer(done), "/guildhall/d145").Body.String(); strings.Contains(body, "<form") {
		t.Error("a done task renders a form")
	}

	// A refusal shows in the box with the words kept; on a task with no box it shows at the top of Now.
	st := gmState
	st.SayRefused, st.SayWords = "the store is busy; try again in a moment", "are we <there> yet?"
	v := s.taskView(talkTask(), st)
	if say := v.Talks[0].Say; say == nil || say.Refused != st.SayRefused || say.Words != st.SayWords || v.Gone != nil {
		t.Errorf("the refusal is not in the box: %+v %+v", v.Talks[0].Say, v.Gone)
	}
	v = s.taskView(done, st)
	if v.Gone == nil || v.Gone.Refused != st.SayRefused || v.Gone.Words != st.SayWords {
		t.Errorf("a refusal with no box is not shown: %+v", v.Gone)
	}
}
