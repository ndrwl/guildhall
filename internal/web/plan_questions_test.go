package web_test

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/web/webtest"
)

// planWant is the open question of planAsk's spec, recommending (a).
var planWant = []web.PlanQuestion{{N: 1, Recommended: "(a) the tailnet one", Options: map[string]string{"a": "(a) the tailnet one", "b": "(b) localhost only"}}}

func plansServer(t *testing.T, f *fakeAnswerer, drafts []web.Draft) *web.Server {
	t.Helper()
	return web.DraftsFixture(t, webtest.Logins{"100.101.102.103": gmLogin, "100.101.102.104": "other@example.com"}, gmLogin, f, drafts)
}

// askOf is the part of page from ask id's card to the next card.
func askOf(t *testing.T, page, id string) string {
	t.Helper()
	i := strings.Index(page, ` id="`+id+`">`)
	if i < 0 {
		t.Fatalf("the page has no ask %s", id)
	}
	rest := page[i+1:]
	if j := strings.Index(rest, `<div class="ask`); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// TestPlanQuestionsOnTheApproval: a plan approval's open questions get a grill round's controls inside the approval's
// one form, with "n of m answered", and no Send round; another login sees no controls.
func TestPlanQuestionsOnTheApproval(t *testing.T) {
	h := plansServer(t, &fakeAnswerer{}, nil)
	ask := askOf(t, request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil).Body.String(), planAsk)
	for _, want := range []string{
		`<p class="ph oqh">Open questions · 0 of 1 answered</p>`,
		`<textarea id="accept--s2-1-q1-w" name="text-1" rows="2"></textarea>`,
		`name="act" value="accept-1"><b>Accept recommendation</b><span>(a) the tailnet one</span>`,
		`name="act" value="words-1"><b>Answer with my words</b>`,
		`name="act" value="save"><b>Save, answer later</b>`,
		`<b>Approved <i class="badge">Recommended</i></b><span>take every recommendation above; the task goes on</span>`,
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("the approval lacks %q:\n%s", want, ask)
		}
	}
	if strings.Contains(ask, `value="send"`) {
		t.Error("a plan approval's open questions have a Send round")
	}
	other := askOf(t, request(h, http.MethodGet, "/guildhall/d122", otherAddr, nil, nil).Body.String(), planAsk)
	if strings.Contains(other, `name="act"`) || strings.Contains(other, "<textarea") {
		t.Errorf("another login's approval has controls:\n%s", other)
	}
}

// TestPlanQuestionsDrafts: an answered question folds to one line with Change, the heading counts it, and the
// approval's tiles name what they send; unmarked words say they are not an answer yet.
func TestPlanQuestionsDrafts(t *testing.T) {
	at := time.Date(2026, 11, 4, 14, 50, 0, 0, time.UTC)
	h := plansServer(t, &fakeAnswerer{}, []web.Draft{{Tag: planAsk, Round: web.PlanRound, Question: 1, Mark: web.DraftWords, Text: "Both, behind a flag.", At: at}})
	ask := askOf(t, request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil).Body.String(), planAsk)
	for _, want := range []string{
		`<p class="ph oqh">Open questions · 1 of 1 answered</p>`,
		`<section class="oq rq done" id="accept--s2-1-q1">`,
		`<b>✓ Your words, saved</b> <span class="gist">Both, behind a flag.</span><button class="lnk" type="submit" name="act" value="change-1">Change</button>`,
		`<span>with your 1 answer</span>`,
		`<span>send it back with your 1 answer; add your words</span>`,
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("the approval lacks %q:\n%s", want, ask)
		}
	}

	h = plansServer(t, &fakeAnswerer{}, []web.Draft{{Tag: planAsk, Round: web.PlanRound, Question: 1, Text: "Maybe both?", At: at}})
	ask = askOf(t, request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil).Body.String(), planAsk)
	if !strings.Contains(ask, `>Maybe both?</textarea><p class="saved">saved `) || !strings.Contains(ask, "0 of 1 answered") ||
		!strings.Contains(ask, "take every recommendation above") {
		t.Errorf("unmarked words:\n%s", ask)
	}
}

// TestPlanQuestionsPress: a question's button posts with the approval's form and reaches the Answerer's Round as a
// plan press under the decision, with every box and the spec's questions, redirecting to the question; Send round,
// and a press on an ask with no open questions, are refused.
func TestPlanQuestionsPress(t *testing.T) {
	f := &fakeAnswerer{}
	h := plansServer(t, f, nil)
	v := form(planAsk, "sha256:abc", "", "")
	v.Set("text-1", "Both.")
	v.Set("act", "words-1")
	w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/guildhall/d122#accept--s2-1-q1" {
		t.Fatalf("press: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}
	want := web.RoundPost{Repository: "guildhall", TaskID: "d122", Tag: planAsk, Round: web.PlanRound, Act: web.RoundWords, Question: 1,
		Boxes: map[int]string{1: "Both."}, Login: gmLogin, Subject: "sha256:abc", Plan: planWant}
	if len(f.rounds) != 1 || !reflect.DeepEqual(f.rounds[0], want) || len(f.answers) != 0 {
		t.Fatalf("rounds = %+v, want %+v", f.rounds, want)
	}
	v.Set("act", "save")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/guildhall/d122#accept--s2-1" {
		t.Errorf("save: %d %q", w.Code, w.Header().Get("Location"))
	}
	for name, c := range map[string]struct {
		form url.Values
		code int
		says string
	}{
		"send round": {func() url.Values { v := form(planAsk, "", "", ""); v.Set("act", "send"); return v }(), http.StatusBadRequest, "no button of the open questions"},
		// Accept all recommendations is a grill round's button; the approval has its own take-every-recommendation tile.
		"accept all": {func() url.Values { v := form(planAsk, "", "", ""); v.Set("act", "accept-all"); return v }(), http.StatusBadRequest, "no button of the open questions"},
		// Accept all and send sends a round, which an approval's answer does instead.
		"accept all and send": {func() url.Values { v := form(planAsk, "", "", ""); v.Set("act", "accept-send"); return v }(), http.StatusBadRequest, "no button of the open questions"},
		"no questions": {func() url.Values { v := form("grant-1", "", "", ""); v.Set("act", "accept-1"); return v }(), http.StatusConflict,
			"#grant-1 has no open questions to answer here"},
	} {
		w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, c.form, nil)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s: %d\n%s", name, w.Code, w.Body)
		}
	}
	if len(f.rounds) != 2 {
		t.Errorf("refused presses reached the Answerer: %+v", f.rounds)
	}
	// A refused press shows the boxes' words again on the approval.
	f.err = web.Refuse(http.StatusConflict, "this task is closed")
	v.Set("text-1", "Kept words.")
	if body := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil).Body.String(); !strings.Contains(body, ">Kept words.</textarea>") {
		t.Errorf("a refused press loses the boxes' words:\n%s", body)
	}
}

// TestPlanQuestionsAnswer: Approved carries the spec's open questions and every box to the Answerer, which writes
// them into the one answer; an ask that is not a plan approval carries none, and words only carry none.
func TestPlanQuestionsAnswer(t *testing.T) {
	f := &fakeAnswerer{}
	h := plansServer(t, f, nil)
	v := form(planAsk, "sha256:abc", "approved", "Go.")
	v.Set("text-1", "Both.")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("approved: %d\n%s", w.Code, w.Body)
	}
	if len(f.answers) != 1 || !reflect.DeepEqual(f.answers[0].Plan, planWant) || !reflect.DeepEqual(f.answers[0].Boxes, map[int]string{1: "Both."}) {
		t.Fatalf("answers = %+v", f.answers)
	}
	v.Set("send", "words")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil); w.Code != http.StatusSeeOther || len(f.sent) != 1 || f.sent[0].Plan != nil {
		t.Errorf("words only: %d %+v", w.Code, f.sent)
	}
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, form("review--s2-x2", loopSubject, "accept", ""), nil); w.Code != http.StatusSeeOther ||
		len(f.answers) != 2 || f.answers[1].Plan != nil || f.answers[1].Boxes != nil {
		t.Errorf("another ask: %d %+v", w.Code, f.answers)
	}
}

// twoPlanSpec is a plan spec with two lettered open questions, each recommending (a).
const twoPlanSpec = "# The spec\n\n## Summary\n\n- **Goal:** one web page per task.\n\n## Open questions\n\n" +
	"**Q1. Which address**\nThe page needs an address.\nOptions: (a) the tailnet one; (b) localhost only.\nRecommended: (a). Phones.\n\n" +
	"**Q2. Which list**\nThe page lists tasks.\nOptions: (a) open ones; (b) every one.\nRecommended: (a). Short.\n"

// TestPlanQuestionsPicks: a plan approval's open questions keep both answer paths, their picks and their controls,
// and each question has one answer. With two open questions, both cards are picks beside their controls and the
// tiles carry the line shown while a pick is changed; a press on Q2's Answer with my words carries Q1's changed pick
// as Q1's answer; Approved and Changes requested carry a changed pick as its question's answer, not as a "Q2 (b)"
// line, and Approved is not refused for it; a pick naming no option is dropped; words only, which answers nothing,
// keeps the line; a saved pick folds its card as answered.
func TestPlanQuestionsPicks(t *testing.T) {
	f := &fakeAnswerer{}
	h := plansServer(t, f, nil)
	subject := web.RewriteDocument(t, h, "tasks/guildhall/d122/plan-1/spec.md", planAsk, twoPlanSpec)
	two := []web.PlanQuestion{
		{N: 1, Recommended: "(a) the tailnet one", Options: map[string]string{"a": "(a) the tailnet one", "b": "(b) localhost only"}},
		{N: 2, Recommended: "(a) open ones", Options: map[string]string{"a": "(a) open ones", "b": "(b) every one"}},
	}

	ask := askOf(t, request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil).Body.String(), planAsk)
	for _, want := range []string{
		`<div class="oqs ctl">`, "0 of 2 answered",
		`name="pick-1" value="b">`, `name="pick-2" value="b">`, `name="act" value="accept-1"`, `name="act" value="words-2"`,
		`<span>take every recommendation above; the task goes on</span><span class="pkd">` + web.PlanPickedNote + `</span>`,
		`<span class="pkd">send it back ` + web.PlanPickedNote + `; add your words</span>`,
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("the approval lacks %q:\n%s", want, ask)
		}
	}

	press := form(planAsk, subject, "", "")
	press.Set("act", "words-2")
	press.Set("text-2", "Every one, paged.")
	press.Set("pick-1", "b")
	press.Set("pick-2", "")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, press, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("press: %d\n%s", w.Code, w.Body)
	}
	if got := f.rounds[0]; got.Question != 2 || got.Act != web.RoundWords || !reflect.DeepEqual(got.Picks, map[int]string{1: "b"}) || !reflect.DeepEqual(got.Plan, two) {
		t.Errorf("a pick on Q1 and words on Q2: %+v", got)
	}

	post := func(option, send string, picks map[string]string) url.Values {
		v := form(planAsk, subject, option, "My words.")
		v.Set("text-1", "Both.")
		for k, x := range picks {
			v.Set(k, x)
		}
		if send != "" {
			v.Set("send", send)
		}
		return v
	}
	for _, option := range []string{"changes_requested", "approved"} {
		if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, post(option, "", map[string]string{"pick-1": "", "pick-2": "b"}), nil); w.Code != http.StatusSeeOther {
			t.Fatalf("%s with a pick: %d\n%s", option, w.Code, w.Body)
		}
		got := f.answers[len(f.answers)-1]
		if got.Option != option || got.Text != "My words." || !reflect.DeepEqual(got.Plan, two) || !reflect.DeepEqual(got.Boxes, map[int]string{1: "Both."}) ||
			!reflect.DeepEqual(got.Picks, map[int]string{2: "b"}) {
			t.Errorf("%s with a pick: %+v", option, got)
		}
	}
	request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, post("approved", "", map[string]string{"pick-2": "z"}), nil)
	if got := f.answers[len(f.answers)-1]; got.Picks != nil {
		t.Errorf("a pick naming no option was kept: %+v", got.Picks)
	}
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, post("", "words", map[string]string{"pick-2": "b"}), nil); w.Code != http.StatusSeeOther ||
		len(f.sent) != 1 || f.sent[0].Text != "Q2 (b)\n\nMy words." || f.sent[0].Plan != nil || f.sent[0].Picks != nil {
		t.Errorf("words only with a pick: %d %+v", w.Code, f.sent)
	}

	h = plansServer(t, f, []web.Draft{{Tag: planAsk, Round: web.PlanRound, Question: 1, Mark: web.DraftPick, Pick: "b", At: time.Date(2026, 11, 4, 14, 50, 0, 0, time.UTC)}})
	web.RewriteDocument(t, h, "tasks/guildhall/d122/plan-1/spec.md", planAsk, twoPlanSpec)
	ask = askOf(t, request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil).Body.String(), planAsk)
	for _, want := range []string{
		"1 of 2 answered", `<b>✓ Picked</b> <span class="gist">(b) localhost only</span>`, `name="pick-2" value="b">`,
		`<span>with your 1 answer; Q2 takes the recommendation</span>`,
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("a saved pick: the approval lacks %q:\n%s", want, ask)
		}
	}
	if strings.Contains(ask, `name="pick-1"`) {
		t.Errorf("a folded card has picks:\n%s", ask)
	}
}

// TestPlanAnswersText: the list written into a plan approval's answer names each question's answer, the unanswered
// taking the recommendation, and is empty with none answered; the tile line names the ones left.
func TestPlanAnswersText(t *testing.T) {
	qs := []web.PlanQuestion{{N: 1, Recommended: "(b) one route"}, {N: 2, Recommended: "(a) 5 s"}, {N: 3}, {N: 4, Recommended: "(a) list"}}
	got := web.PlanAnswersText(qs, map[int]web.RoundAnswer{1: {Accepted: true}, 2: {Words: "15 s.\n\nFixed."}, 4: {Accepted: true, Words: "and keep it short"}})
	want := web.PlanAnswersHead + `

- Q1: accepted the recommendation (b) one route
- Q2: 15 s.

  Fixed.
- Q3: no answer; takes the spec's recommendation
- Q4: accepted the recommendation (a) list; and keep it short`
	if got != want {
		t.Errorf("PlanAnswersText =\n%s\nwant\n%s", got, want)
	}
	if got := web.PlanAnswersText(qs, nil); got != "" {
		t.Errorf("no answers = %q", got)
	}
	qs[0].Options = map[string]string{"a": "(a) two routes", "b": "(b) one route"}
	got = web.PlanAnswersText(qs[:2], map[int]web.RoundAnswer{1: {Pick: "a"}, 2: {Pick: "c", Words: "a long one"}})
	if want := web.PlanAnswersHead + "\n\n- Q1: picked (a) two routes\n- Q2: picked (c); a long one"; got != want {
		t.Errorf("picks: PlanAnswersText =\n%s\nwant\n%s", got, want)
	}
	for _, c := range []struct {
		answered int
		left     []int
		want     string
	}{
		{2, []int{3, 4}, "with your 2 answers; Q3 and Q4 take the recommendation"},
		{1, []int{2}, "with your 1 answer; Q2 takes the recommendation"},
		{3, nil, "with your 3 answers"},
	} {
		if got := web.PlanTileNote(c.answered, c.left); got != c.want {
			t.Errorf("PlanTileNote(%d, %v) = %q, want %q", c.answered, c.left, got, c.want)
		}
	}
}
