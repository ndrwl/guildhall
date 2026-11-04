package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// postRoute posts form to the task page route of task with the page's token, as the Guildmaster's browser would.
func postRoute(t *testing.T, h http.Handler, task, route string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form.Get("token") == "" {
		form.Set("token", pageToken(t, h))
	}
	r := httptest.NewRequest(http.MethodPost, "/app/"+task+"/"+route, strings.NewReader(form.Encode()))
	r.Host, r.RemoteAddr = pageHost, pageNode
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+pageHost)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// gmPage is the task page of task as the Guildmaster's login sees it.
func gmPage(h http.Handler, task string) string {
	r := httptest.NewRequest(http.MethodGet, "/app/"+task, nil)
	r.Host, r.RemoteAddr = pageHost, pageNode
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Body.String()
}

// TestTaskSayRecommend: --recommend makes the Director's words a recommendation of one of an open decision's
// options, with --words-file the words the page pre-fills; a rerun replays; a tag that is no open decision, an
// option it does not offer and flags that do not go together are refused.
func TestTaskSayRecommend(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	args := []string{"--store", path, "--repo", "app", "--task", "a-approve", "--tag", "accept-plan", "--text-file", "-", "--recommend", "approve"}
	code, out, errOut := runTaskSay(t, "Windows is covered, so **approve**.", args...)
	if code != 0 {
		t.Fatalf("task say --recommend = %d %s", code, errOut)
	}
	if again, rerun, _ := runTaskSay(t, "Windows is covered, so **approve**.", args...); again != 0 || rerun != out {
		t.Fatalf("rerun = %d %s, want %s", again, rerun, out)
	}
	words := filepath.Join(t.TempDir(), "words.md")
	writeFile(t, words, "approve, Windows is covered\n")
	if code, _, errOut := runTaskSay(t, "Still approve.", append(args, "--words-file", words)...); code != 0 {
		t.Fatalf("task say --recommend --words-file = %d %s", code, errOut)
	}
	got := conversation(t, path, "app", "a-approve")
	want := []store.Saying{
		{Author: store.ConversationDirector, Tag: "accept-plan", Kind: store.ConversationRecommendation, Text: "Windows is covered, so **approve**.", Option: "approve"},
		{Author: store.ConversationDirector, Tag: "accept-plan", Kind: store.ConversationRecommendation, Text: "Still approve.", Option: "approve", Words: "approve, Windows is covered\n"},
	}
	if len(got) != 2 || got[0].Saying != want[0] || got[1].Saying != want[1] {
		t.Fatalf("conversation = %+v", got)
	}

	for name, c := range map[string]struct {
		args []string
		code int
		says string
	}{
		"not an option":       {[]string{"--tag", "accept-plan", "--recommend", "merge"}, 1, `"merge" is not one of decision accept-plan's options`},
		"an attempt":          {[]string{"--tag", "plan-1", "--recommend", "approve"}, 1, `has no decision "plan-1"`},
		"chat author":         {[]string{"--tag", "accept-plan", "--recommend", "approve", "--author", "guildmaster-chat"}, 2, "--recommend is the Director's"},
		"words alone":         {[]string{"--tag", "accept-plan", "--words-file", words}, 2, "--words-file goes with --recommend"},
		"blank recommend":     {[]string{"--tag", "accept-plan", "--recommend", " "}, 2, "--recommend needs the option"},
		"both standard input": {[]string{"--tag", "accept-plan", "--recommend", "approve", "--words-file", "-"}, 2, "cannot both read standard input"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errOut := runTaskSay(t, "Words.", append([]string{"--store", path, "--repo", "app", "--task", "a-approve", "--text-file", "-"}, c.args...)...)
			if code != c.code || !strings.Contains(errOut, c.says) {
				t.Errorf("= %d %s; want %d saying %q", code, errOut, c.code, c.says)
			}
		})
	}
	if n := len(conversation(t, path, "app", "a-approve")); n != 2 {
		t.Errorf("refusals wrote: %d entries", n)
	}
}

// TestConfirmOnThePage: Confirm answers the decision with the recommendation's option and the box's words through
// the one answer path, and records the confirmation, naming the recommendation, with the answer; a double submit
// answers once; confirming a recommendation the Director has since replaced is refused with nothing written.
func TestConfirmOnThePage(t *testing.T) {
	h, path, _ := pageFixture(t)
	say := func(text string) {
		t.Helper()
		if code, _, errOut := runTaskSay(t, text, "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "accept-plan", "--text-file", "-", "--recommend", "approve"); code != 0 {
			t.Fatalf("task say --recommend = %d %s", code, errOut)
		}
	}
	say("I recommend **approve**.")
	body := gmPage(h, "a-approve")
	if !strings.Contains(body, `<button class="opt rec pos" type="submit" name="confirm" value="1"><b>Confirm: Approve</b>`) {
		t.Fatalf("the page offers no Confirm:\n%s", body)
	}
	say("On reflection, still **approve**, and here is why.")

	confirm := url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "confirm": {"1"}, "text": {"Ship it."}}
	if w := postRoute(t, h, "a-approve", "answer", confirm); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "the Director has recommended again") {
		t.Fatalf("confirming a replaced recommendation: %d\n%s", w.Code, w.Body)
	}
	if d := pageDecision(t, path, "a-approve", "accept-plan"); !d.Open() || len(conversation(t, path, "app", "a-approve")) != 2 {
		t.Fatalf("a refused confirmation wrote: %+v", d)
	}
	if w := postRoute(t, h, "a-approve", "answer", url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "confirm": {"9"}}); w.Code != http.StatusBadRequest {
		t.Errorf("confirming no recommendation: %d", w.Code)
	}

	confirm.Set("confirm", "2")
	for i := range 2 {
		if w := postRoute(t, h, "a-approve", "answer", confirm); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/a-approve#accept-plan" {
			t.Fatalf("confirm %d: %d %q\n%s", i+1, w.Code, w.Header().Get("Location"), w.Body)
		}
	}
	d := pageDecision(t, path, "a-approve", "accept-plan")
	if !d.Answered || d.Choice != "approve" || d.Answer != "approve\n\nShip it." || d.AnsweredVia != store.AnsweredViaPage || d.AnsweredBy != pageLogin {
		t.Fatalf("decision = %+v", d)
	}
	got := conversation(t, path, "app", "a-approve")
	want := store.Saying{Author: store.ConversationGuildmasterPage, Tag: "accept-plan", Kind: store.ConversationConfirmed, Text: "approve\n\nShip it.", Option: "approve", Confirms: 2}
	if len(got) != 3 || got[2].Saying != want {
		t.Fatalf("conversation = %+v, want the confirmation once", got)
	}
	if msgs := pageMessages(t, path); len(msgs) != 1 || msgs[0].Type != store.MessageDecisionAnswered {
		t.Errorf("messages = %+v, want one decision-answered", msgs)
	}
	if body := gmPage(h, "a-approve"); !strings.Contains(body, "<p>↩ Confirmed: <b>Approve</b></p>") {
		t.Errorf("the page does not show the confirmation")
	}
}

// TestSayOnThePage: the Say box records the Guildmaster's words under the attempt, written with a note to the
// Director, once for a double submit; an attempt the task does not have and a closed task are refused.
func TestSayOnThePage(t *testing.T) {
	h, path, _ := pageFixture(t)
	body := gmPage(h, "a-approve")
	if !strings.Contains(body, `<form class="say" id="say" method="post" action="/app/a-approve/say"><input type="hidden" name="tag" value="plan-1">`) {
		t.Fatalf("the page has no Say box for plan-1:\n%s", body)
	}
	form := url.Values{"tag": {"plan-1"}, "text": {"Does the plan cover **Windows**?"}}
	id := web.NoteID("app", "a-approve", "say:plan-1", "Does the plan cover **Windows**?")
	for i := range 2 {
		if w := postRoute(t, h, "a-approve", "say", form); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/a-approve?said="+id+"#plan-1-talk" {
			t.Fatalf("say %d: %d %q\n%s", i+1, w.Code, w.Header().Get("Location"), w.Body)
		}
	}
	got := conversation(t, path, "app", "a-approve")
	want := store.Saying{Author: store.ConversationGuildmasterPage, Tag: "plan-1", Kind: store.ConversationSay, Text: "Does the plan cover **Windows**?"}
	if len(got) != 1 || got[0].Saying != want {
		t.Fatalf("conversation = %+v", got)
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != id || msgs[0].Type != store.MessageNote || msgs[0].Text != "a-approve plan-1, said on the page, nothing answered: Does the plan cover **Windows**?" {
		t.Fatalf("messages = %+v", msgs)
	}
	if d := pageDecision(t, path, "a-approve", "accept-plan"); !d.Open() {
		t.Errorf("the Say box answered a decision: %+v", d)
	}

	if w := postRoute(t, h, "a-approve", "say", url.Values{"tag": {"build-9"}, "text": {"Hi."}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "this task has no attempt build-9") {
		t.Errorf("an unknown attempt: %d", w.Code)
	}
	if w := postRoute(t, h, "a-approve", "say", url.Values{"tag": {"plan-1"}, "text": {" \r\n"}}); w.Code != http.StatusBadRequest {
		t.Errorf("blank words: %d", w.Code)
	}

	// The page loaded before the task closed still carries its token.
	token := pageToken(t, h)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.VoidDecision("void", "app", "a-approve", "accept-plan", "task cancelled"); err == nil {
		_, err = s.CancelTask("cancel", "app", "a-approve", "not needed")
	}
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gmPage(h, "a-approve"), "<form") {
		t.Error("a closed task's page renders a form")
	}
	w := postRoute(t, h, "a-approve", "say", url.Values{"tag": {"plan-1"}, "text": {"Too late?"}, "token": {token}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "this task is closed") || !strings.Contains(w.Body.String(), "Too late?") {
		t.Errorf("a closed task: %d\n%s", w.Code, w.Body)
	}
	if n := len(conversation(t, path, "app", "a-approve")); n != 1 || len(pageMessages(t, path)) != 1 {
		t.Errorf("a refused say wrote: %d entries", n)
	}
}

// TestDecideOpenRecommend: decide open --recommend stores the recommended option on the question, which the page
// marks; an option not offered is a usage error.
func TestDecideOpenRecommend(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Back off or stop?", "--option", "back-off", "--option", "stop", "--recommend", "stop")
	if code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	d := decodeJSON[store.Decision](t, out)
	if d.ID != "q1" || d.Recommended != "stop" {
		t.Fatalf("decision = %+v", d)
	}
	if code, again, _ := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Back off or stop?", "--option", "back-off", "--option", "stop", "--recommend", "stop"); code != 0 ||
		decodeJSON[store.Decision](t, again).ID != "q1" {
		t.Errorf("reopen = %d %s, want q1 again", code, again)
	}
	if code, _, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which?", "--option", "a", "--recommend", "b"); code != 2 ||
		!strings.Contains(errOut, "must be one of the --option values") {
		t.Errorf("a recommendation not offered = %d %s", code, errOut)
	}
}

// TestWebSourceRecommended: the Source gives a question's ask the option the Director recommended.
func TestWebSourceRecommended(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	if code, _, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "a-approve", "--question", "Keep the flag?", "--option", "keep", "--option", "drop", "--recommend", "drop"); code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	task, err := webSourceFor(t, dir).Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range task.Asks {
		if a.ID == "q1" {
			if a.Recommended != "drop" {
				t.Errorf("q1's recommended = %q, want drop", a.Recommended)
			}
			return
		}
	}
	t.Errorf("no ask q1 in %+v", task.Asks)
}
