package cli

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// planAnswerer is the page fixture's Answerer, called as the page calls it for the a-approve plan approval.
func planAnswerer(t *testing.T) (webAnswerer, string) {
	t.Helper()
	_, path, log := pageFixture(t)
	app, _, _ := newTestApp(nil, nil)
	app.Now = func() time.Time { return clerkNow }
	app.DirectorPane = log
	app.Notify = log.notify
	c, err := app.clerkCopy(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return webAnswerer{a: app, c: c}, path
}

var planQs = []web.PlanQuestion{{N: 1, Recommended: "(b) one route"}, {N: 2, Recommended: "(a) 5 s"}, {N: 3, Recommended: "(a) list"}}

func planPress(act string, q int, boxes map[int]string) web.RoundPost {
	return web.RoundPost{Repository: "app", TaskID: "a-approve", Tag: "accept-plan", Round: web.PlanRound, Act: act, Question: q, Boxes: boxes,
		Login: pageLogin, Subject: planSubject, Plan: planQs}
}

// planVersion is the highest number of a-approve's plan drafts under accept-plan, 0 with none: the drafts number a
// page loaded now carries on the approval's form.
func planVersion(t *testing.T, path string) int {
	t.Helper()
	got, err := withStore(path, true, func(s *store.Store) ([]store.DraftEntry, error) { return s.Drafts("app", "a-approve") })
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range got {
		if e.Tag == "accept-plan" && e.Round == web.PlanRound {
			n = max(n, e.Number)
		}
	}
	return n
}

// fresh is p posted from a page loaded now, carrying the current drafts number.
func fresh(t *testing.T, path string, p web.RoundPost) web.RoundPost {
	t.Helper()
	p.Drafts = planVersion(t, path)
	return p
}

func refusedWith(t *testing.T, err error, says string) {
	t.Helper()
	ref, ok := errors.AsType[*web.Refusal](err)
	if !ok || !strings.Contains(ref.Reason, says) {
		t.Errorf("err = %v, want a refusal saying %q", err, says)
	}
}

// TestWebPlanQuestions: a plan approval's question presses save every box as drafts under the decision and send
// nothing; Approved writes the answered questions into the one answer, the unanswered taking the recommendation, and
// a double submit replays it; presses on another subject or an answered decision are refused.
func TestWebPlanQuestions(t *testing.T) {
	w, path := planAnswerer(t)
	if err := w.Round(planPress(web.RoundAccept, 1, map[int]string{1: "", 2: "15 s.\r\nFixed.", 3: "  "})); err != nil {
		t.Fatal(err)
	}
	d := pageDrafts(t, path, "a-approve")
	if d[1].Mark != "accept" || d[2].Mark != "" || d[2].Text != "15 s.\nFixed." || d[1].Tag != "accept-plan" || d[1].Round != web.PlanRound {
		t.Fatalf("drafts = %+v", d)
	}
	if err := w.Round(fresh(t, path, planPress(web.RoundWords, 3, map[int]string{2: "15 s.\nFixed.", 3: ""}))); err == nil {
		t.Fatal("Answer with my words on an empty box was not refused")
	} else {
		refusedWith(t, err, "Q3 has no words to answer with")
	}
	if len(pageMessages(t, path)) != 0 {
		t.Fatal("a press sent a message")
	}
	other := planPress(web.RoundSave, 0, nil)
	other.Subject = "sha256:" + strings.Repeat("b", 64)
	refusedWith(t, w.Round(other), "is about another version now")
	refusedWith(t, w.Round(planPress(web.RoundAccept, 9, nil)), "no open question Q9")

	// Approved saves the boxes first: Q2's words are marked by Answer with my words, then sent with the answer.
	if err := w.Round(fresh(t, path, planPress(web.RoundWords, 2, map[int]string{2: "15 s.\nFixed."}))); err != nil {
		t.Fatal(err)
	}
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "accept-plan", Subject: planSubject, Option: "approve", Text: "Go.",
		Login: pageLogin, Plan: planQs, Boxes: map[int]string{3: "maybe a table"}, Drafts: planVersion(t, path)}
	if err := w.Answer(x); err != nil {
		t.Fatal(err)
	}
	dec := pageDecision(t, path, "a-approve", "accept-plan")
	want := "approve\n\nGo.\n\n" + web.PlanAnswersHead + "\n\n- Q1: accepted the recommendation (b) one route\n- Q2: 15 s.\n  Fixed.\n- Q3: no answer; takes the recommendation (a) list"
	if !dec.Answered || dec.Choice != "approve" || dec.Answer != want {
		t.Fatalf("answer = %q, want %q", dec.Answer, want)
	}
	if d := pageDrafts(t, path, "a-approve"); d[3].Text != "maybe a table" || d[3].Mark != "" {
		t.Errorf("Approved did not save the boxes: %+v", d)
	}
	if err := w.Answer(x); err != nil {
		t.Fatalf("double submit: %v", err)
	}
	if again := pageDecision(t, path, "a-approve", "accept-plan"); again.Answer != want || len(pageMessages(t, path)) != 1 {
		t.Fatalf("double submit changed the record: %q", again.Answer)
	}
	refusedWith(t, w.Round(planPress(web.RoundSave, 0, nil)), "already answered")
}

// TestWebPlanQuestionsStaleAnswerRefused: tab B, loaded before tab A answered Q1 with its words, presses Approved
// with Q1's box empty; it is refused with 409 as a stale grill round's press is, Q1 keeps tab A's words and mark,
// and the decision stays open (H100 of d227). A second Approved from the page loaded again is accepted, and saves
// that each post the current number all write, the same words saved again after others included.
func TestWebPlanQuestionsStaleAnswerRefused(t *testing.T) {
	w, path := planAnswerer(t)
	stale := planVersion(t, path)
	if err := w.Round(fresh(t, path, planPress(web.RoundWords, 1, map[int]string{1: "CSV only."}))); err != nil {
		t.Fatal(err)
	}
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "accept-plan", Subject: planSubject, Option: "approve",
		Login: pageLogin, Plan: planQs, Boxes: map[int]string{1: "", 2: "", 3: ""}, Drafts: stale}
	err := w.Answer(x)
	refusedWith(t, err, staleReason)
	if ref, ok := errors.AsType[*web.Refusal](err); !ok || ref.Status != http.StatusConflict {
		t.Errorf("err = %v, want 409", err)
	}
	if d := pageDrafts(t, path, "a-approve"); d[1].Text != "CSV only." || d[1].Mark != store.DraftWords {
		t.Errorf("the stale answer overwrote Q1: %+v", d[1])
	}
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); dec.Answered {
		t.Fatalf("a stale answer answered the decision: %q", dec.Answer)
	}
	if len(pageMessages(t, path)) != 0 {
		t.Fatal("a stale answer sent a message")
	}

	for _, words := range []string{"one", "two", "one"} {
		if err := w.Round(fresh(t, path, planPress(web.RoundSave, 0, map[int]string{2: words}))); err != nil {
			t.Fatalf("save %q: %v", words, err)
		}
		if d := pageDrafts(t, path, "a-approve"); d[2].Text != words {
			t.Fatalf("after saving %q, Q2 = %q", words, d[2].Text)
		}
	}

	x.Drafts, x.Boxes = planVersion(t, path), map[int]string{1: "CSV only.", 2: "one"}
	if err := w.Answer(x); err != nil {
		t.Fatalf("Approved from the page loaded again: %v", err)
	}
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); !dec.Answered || !strings.Contains(dec.Answer, "- Q1: CSV only.") {
		t.Fatalf("answer = %q", dec.Answer)
	}
}

// TestWebPlanQuestionsStalePressRefused: a press of the question controls from a page older than a plan draft saved
// since, Save or a question's button, is refused with 409 and writes nothing.
func TestWebPlanQuestionsStalePressRefused(t *testing.T) {
	w, path := planAnswerer(t)
	stale := planVersion(t, path)
	if err := w.Round(fresh(t, path, planPress(web.RoundWords, 1, map[int]string{1: "CSV only.", 2: "flag it"}))); err != nil {
		t.Fatal(err)
	}
	before := planVersion(t, path)
	for _, p := range []web.RoundPost{
		planPress(web.RoundSave, 0, map[int]string{1: "", 2: "", 3: ""}),
		planPress(web.RoundAccept, 2, map[int]string{1: "", 2: "", 3: ""}),
	} {
		p.Drafts = stale
		err := w.Round(p)
		refusedWith(t, err, staleReason)
		if ref, ok := errors.AsType[*web.Refusal](err); !ok || ref.Status != http.StatusConflict {
			t.Errorf("%s-%d: err = %v, want 409", p.Act, p.Question, err)
		}
	}
	d := pageDrafts(t, path, "a-approve")
	if d[1].Text != "CSV only." || d[1].Mark != store.DraftWords || d[2].Text != "flag it" || d[2].Mark != "" {
		t.Errorf("a stale press wrote drafts: %+v", d)
	}
	if after := planVersion(t, path); after != before {
		t.Errorf("a stale press wrote drafts: %d, then %d", before, after)
	}
}

// TestWebPlanQuestionsRetryReplays: the same press, and the same Approved, retried after it saved, with the number
// its page carried, replays: it is not refused and writes nothing more.
func TestWebPlanQuestionsRetryReplays(t *testing.T) {
	w, path := planAnswerer(t)
	b := planVersion(t, path)
	p := planPress(web.RoundAccept, 2, map[int]string{1: "CSV only.", 3: "maybe"})
	p.Drafts = b
	for i := range 2 {
		if err := w.Round(p); err != nil {
			t.Fatalf("press %d: %v", i+1, err)
		}
	}
	once := planVersion(t, path)
	if once == b {
		t.Fatal("the press saved nothing")
	}
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "accept-plan", Subject: planSubject, Option: "approve",
		Login: pageLogin, Plan: planQs, Boxes: map[int]string{1: "CSV only.", 3: "a table"}, Picks: map[int]string{}, Drafts: once}
	if err := w.Answer(x); err != nil {
		t.Fatal(err)
	}
	if err := w.Answer(x); err != nil {
		t.Fatalf("a retried Approved: %v", err)
	}
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); !dec.Answered || len(pageMessages(t, path)) != 1 {
		t.Fatalf("a retried Approved changed the record: %q", dec.Answer)
	}
}

// TestWebPlanQuestionsNoDraftsField: a post with no drafts field, read as 0, saves while the approval has no plan
// drafts and is refused once it has one, so a tab loaded before the drafts number was posted must reload.
func TestWebPlanQuestionsNoDraftsField(t *testing.T) {
	w, path := planAnswerer(t)
	if err := w.Round(planPress(web.RoundSave, 0, map[int]string{1: "CSV only."})); err != nil {
		t.Fatalf("first save with no drafts: %v", err)
	}
	refusedWith(t, w.Round(planPress(web.RoundSave, 0, map[int]string{1: "CSV"})), staleReason)
	if d := pageDrafts(t, path, "a-approve"); d[1].Text != "CSV only." {
		t.Errorf("Q1 = %q", d[1].Text)
	}
}

// TestWebPlanQuestionsUnanswered: an approval with no question answered is the approval alone; one over the limit
// with its answers is refused, keeping the drafts.
func TestWebPlanQuestionsUnanswered(t *testing.T) {
	w, path := planAnswerer(t)
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "accept-plan", Subject: planSubject, Option: "approve", Login: pageLogin, Plan: planQs,
		Boxes: map[int]string{1: "not an answer yet"}}
	long := strings.Repeat("x", 40<<10)
	if err := w.Round(planPress(web.RoundWords, 2, map[int]string{2: long})); err != nil {
		t.Fatal(err)
	}
	x.Text, x.Drafts = strings.Repeat("y", 30<<10), planVersion(t, path)
	refusedWith(t, w.Answer(x), "over the limit")
	if d := pageDrafts(t, path, "a-approve"); d[2].Text != long || d[1].Text != "not an answer yet" {
		t.Errorf("the refused answer dropped the drafts: %d", len(d))
	}
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); dec.Answered {
		t.Fatal("an answer over the limit was recorded")
	}
	if err := w.Round(fresh(t, path, planPress(web.RoundChange, 2, nil))); err != nil {
		t.Fatal(err)
	}
	x.Text, x.Drafts = "", planVersion(t, path)
	if err := w.Answer(x); err != nil {
		t.Fatal(err)
	}
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); dec.Answer != "approve" {
		t.Errorf("answer = %q, want the approval alone", dec.Answer)
	}
}

// TestWebPlanQuestionsPicks: with two open questions, a pick on Q1 and words on Q2 are each that question's one
// answer: a press on Q2 saves Q1's pick as its answer, a press on a question with a pick posted wins over the pick,
// the control touched last, and the answer writes each once in its list, with no "Q1 (c)" line of its own.
func TestWebPlanQuestionsPicks(t *testing.T) {
	w, path := planAnswerer(t)
	two := []web.PlanQuestion{
		{N: 1, Recommended: "(b) one route", Options: map[string]string{"a": "(a) two routes", "b": "(b) one route", "c": "(c) no route"}},
		{N: 2, Recommended: "(a) 5 s", Options: map[string]string{"a": "(a) 5 s", "b": "(b) 30 s"}},
	}
	press := func(act string, q int, boxes, picks map[int]string) web.RoundPost {
		p := fresh(t, path, planPress(act, q, boxes))
		p.Plan, p.Picks = two, picks
		return p
	}
	// Q2's pick is overtaken by its Accept recommendation, pressed after it.
	if err := w.Round(press(web.RoundAccept, 2, nil, map[int]string{2: "b"})); err != nil {
		t.Fatal(err)
	}
	if d := pageDrafts(t, path, "a-approve"); d[2].Mark != "accept" || d[2].Pick != "" || d[1].Mark != "" {
		t.Fatalf("a press on a picked question: %+v", d)
	}
	// Answer with my words on Q2 saves Q1's changed pick as Q1's answer.
	if err := w.Round(press(web.RoundWords, 2, map[int]string{1: "", 2: "Ten seconds."}, map[int]string{1: "c"})); err != nil {
		t.Fatal(err)
	}
	d := pageDrafts(t, path, "a-approve")
	if d[1].Mark != "pick" || d[1].Pick != "c" || d[2].Mark != "words" || d[2].Pick != "" {
		t.Fatalf("a pick on Q1 and words on Q2: %+v", d)
	}
	// Change on Q1 drops its pick, and Approved with Q1 picked again writes it as Q1's answer.
	if err := w.Round(press(web.RoundChange, 1, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if d := pageDrafts(t, path, "a-approve"); d[1].Mark != "" || d[1].Pick != "" {
		t.Fatalf("Change kept the pick: %+v", d[1])
	}
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "accept-plan", Subject: planSubject, Option: "approve", Text: "Go.",
		Login: pageLogin, Plan: two, Boxes: map[int]string{1: "Behind a flag."}, Picks: map[int]string{1: "c"}, Drafts: planVersion(t, path)}
	if err := w.Answer(x); err != nil {
		t.Fatal(err)
	}
	want := "approve\n\nGo.\n\n" + web.PlanAnswersHead + "\n\n- Q1: picked (c) no route; Behind a flag.\n- Q2: Ten seconds."
	if dec := pageDecision(t, path, "a-approve", "accept-plan"); dec.Answer != want {
		t.Fatalf("answer = %q, want %q", dec.Answer, want)
	}
}
