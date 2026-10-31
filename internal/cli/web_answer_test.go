package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/web/webtest"
)

// The page fixture's Guildmaster: their login, the node it answers from, and the host the page is served at.
const (
	pageLogin = "gm@example.com"
	pageNode  = "100.101.102.103:51000"
	pageHost  = "andrew-lap-wsl.tail5da40a.ts.net:4388"
)

// planSubject is the clerk fixture's accept-plan subject digest.
var planSubject = "sha256:" + strings.Repeat("a", 64)

// pageFixture is the clerk fixture served as task pages answering for pageLogin, with a running Director session
// whose toasts and wakes go to log; it returns the handler and the store's path.
func pageFixture(t *testing.T) (http.Handler, string, *surfaceLog) {
	t.Helper()
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	log := &surfaceLog{}
	app, _, _ := newTestApp(nil, nil)
	app.Now = func() time.Time { return clerkNow }
	app.DirectorPane = log
	app.Notify = log.notify
	app.WebIdentity = webtest.Logins{"100.101.102.103": pageLogin, "100.101.102.104": "other@example.com"}
	c, err := app.clerkCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	return app.webHandler(c, &config.Web{Address: pageHost, Guildmaster: pageLogin}, &bytes.Buffer{}), path, log
}

// pageToken is the form token of h's pages, read from the Guildmaster's a-approve page while it has an ask.
func pageToken(t *testing.T, h http.Handler) string {
	t.Helper()
	get := httptest.NewRequest(http.MethodGet, "/app/a-approve", nil)
	get.Host, get.RemoteAddr = pageHost, pageNode
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	_, rest, ok := strings.Cut(gw.Body.String(), `name="token" value="`)
	if !ok {
		t.Fatalf("the page has no form:\n%s", gw.Body)
	}
	return rest[:strings.Index(rest, `"`)]
}

// postPage posts an answer to repo/task's page from the Guildmaster's node, same-origin, with the page's token
// unless form has one.
func postPage(t *testing.T, h http.Handler, task string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form.Get("token") == "" {
		form.Set("token", pageToken(t, h))
	}
	r := httptest.NewRequest(http.MethodPost, "/app/"+task+"/answer", strings.NewReader(form.Encode()))
	r.Host, r.RemoteAddr = pageHost, pageNode
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+pageHost)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func pageDecision(t *testing.T, path, task, id string) store.Decision {
	t.Helper()
	d, err := withStore(path, true, func(s *store.Store) (store.Decision, error) { return findDecision(s, "app", task, id) })
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// pageMessages are the Director's unacknowledged messages the page sent: those from the Guildmaster.
func pageMessages(t *testing.T, path string) []store.Message {
	t.Helper()
	msgs, err := withStore(path, true, func(s *store.Store) ([]store.Message, error) {
		m, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, store.MaxInboxLimit)
		return m, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Message
	for _, m := range msgs {
		if m.Sender == store.SenderGuildmaster {
			out = append(out, m)
		}
	}
	return out
}

// TestWebAnswerRecordsOnThePage: an option with words, sent with CRLF, records the choice and the words as the note,
// attributed to the page and the login, sends decision-answered from the Guildmaster without their words, wakes the
// Director and redirects to the ask; a double submit replays it; the story, decide show and clerk inspect say where
// it was answered.
func TestWebAnswerRecordsOnThePage(t *testing.T) {
	h, path, log := pageFixture(t)
	form := url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "option": {"approve"}, "text": {"Go ahead.\r\nKeep it small."}}
	w := postPage(t, h, "a-approve", form)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/a-approve#accept-plan" {
		t.Fatalf("answer: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}
	d := pageDecision(t, path, "a-approve", "accept-plan")
	if !d.Answered || d.Choice != "approve" || d.Note != "Go ahead.\nKeep it small." || d.Answer != "approve\n\nGo ahead.\nKeep it small." ||
		d.AnsweredVia != store.AnsweredViaPage || d.AnsweredBy != pageLogin {
		t.Fatalf("decision = %+v", d)
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != web.AnsweredID("app", "a-approve", "accept-plan") || msgs[0].Type != store.MessageDecisionAnswered ||
		!strings.Contains(msgs[0].Text, "accept-plan") || !strings.Contains(msgs[0].Text, "answered on the task page") || strings.Contains(msgs[0].Text, "Go ahead") {
		t.Fatalf("messages = %+v", msgs)
	}
	// The fixture holds an unread checks-passed message too.
	if want := []string{"wake: guildhall: 2 unread (checks-passed 1, decision-answered 1)"}; !slices.Equal(log.events, want) {
		t.Errorf("events %q, want %q: decision-answered wakes and is not surfaced", log.events, want)
	}
	wakes := readWakes(t, path)
	if len(wakes) != 1 || wakes[0].SenderType != "page" || wakes[0].MessageID != msgs[0].ID {
		t.Errorf("wakes = %+v", wakes)
	}

	// A double submit replays the receipt: the first answer stands and nothing new is sent.
	if w := postPage(t, h, "a-approve", form); w.Code != http.StatusSeeOther {
		t.Fatalf("double submit: %d\n%s", w.Code, w.Body)
	}
	if again := pageDecision(t, path, "a-approve", "accept-plan"); !reflect.DeepEqual(again, d) || len(pageMessages(t, path)) != 1 {
		t.Fatalf("double submit changed the record: %+v", again)
	}

	get := httptest.NewRequest(http.MethodGet, "/app/a-approve", nil)
	get.Host, get.RemoteAddr = pageHost, pageNode
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	body := gw.Body.String()
	if strings.Contains(body, `id="accept-plan"`) && strings.Contains(body, `<div class="ask`) {
		t.Error("the answered ask is still on Now")
	}
	if !strings.Contains(body, "“Go ahead.\nKeep it small.”<small>answered on the task page by gm@example.com</small>") {
		t.Errorf("the story does not say where it was answered:\n%s", body)
	}

	app, stdout, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"decide", "show", "--store", path, "--repo", "app", "--task", "a-approve", "--decision", "accept-plan"}); code != 0 ||
		!strings.Contains(stdout.String(), `"answered_via":"page","answered_by":"gm@example.com"`) {
		t.Errorf("decide show: %d %s", code, stdout)
	}
	if code, out, stderr := runClerk(t, filepath.Dir(path), "inspect", "app/a-approve"); code != 0 || !strings.Contains(out, "answered on the page by gm@example.com") {
		t.Errorf("clerk inspect: %d %s %s", code, out, stderr)
	}
}

// TestWebWordsOnlySendsASurfacedNote: words only answer nothing; they reach the Director as a note from the
// Guildmaster naming the task and decision, shown in a toast as messages send shows a note, then woken.
func TestWebWordsOnlySendsASurfacedNote(t *testing.T) {
	h, path, log := pageFixture(t)
	w := postPage(t, h, "a-approve", url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "send": {"words"}, "text": {"Hold on: does it cover Windows?"}})
	id := web.NoteID("app", "a-approve", "accept-plan", "Hold on: does it cover Windows?")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/a-approve?ask=accept-plan&sent="+id+"#accept-plan" {
		t.Fatalf("words: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if d := pageDecision(t, path, "a-approve", "accept-plan"); !d.Open() {
		t.Fatalf("words only answered the decision: %+v", d)
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != id || msgs[0].Type != store.MessageNote || msgs[0].Text != "a-approve #accept-plan, words only, nothing answered: Hold on: does it cover Windows?" {
		t.Fatalf("messages = %+v", msgs)
	}
	want := []string{
		"toast lab: guildhall: note from guildmaster about a-approve | a-approve #accept-plan, words only, nothing answered: Hold on: does it cover Windows? · guildhall messages read " + id,
		"wake: guildhall: 2 unread (checks-passed 1, note 1 from guildmaster)",
	}
	if !slices.Equal(log.events, want) {
		t.Errorf("events %q, want %q", log.events, want)
	}
}

// TestWebAnswerRefusalsWriteNothing: each refusal leaves the decision open, sends nothing and shows the page again
// with its reason and the words kept.
func TestWebAnswerRefusalsWriteNothing(t *testing.T) {
	long := strings.Repeat("x", store.MaxMessageTextBytes)
	huge := strings.Repeat("y", store.MaxTextBytes)
	for name, c := range map[string]struct {
		form   url.Values
		before func(t *testing.T, path string)
		status int
		reason string
	}{
		"changed subject": {form: url.Values{"subject": {"sha256:" + strings.Repeat("b", 64)}}, status: 409, reason: "changed since you loaded the page"},
		"not an option":   {form: url.Values{"option": {"maybe"}}, status: 400, reason: "is not one of #accept-plan"},
		"no decision":     {form: url.Values{"decision": {"accept-9"}}, status: 404, reason: "this task has no decision #accept-9"},
		"words too long":  {form: url.Values{"send": {"words"}, "text": {long}}, status: 413, reason: "your words are over the limit"},
		"answer too long": {form: url.Values{"text": {huge}}, status: 413, reason: "your answer is over the limit"},
		"blank words":     {form: url.Values{"send": {"words"}, "text": {" \r\n "}}, status: 400, reason: "write your words in the box first"},
		"invalid UTF-8":   {form: url.Values{"text": {"bad \xff"}}, status: 400, reason: "not valid UTF-8"},
		"already answered": {before: func(t *testing.T, path string) {
			answerClerkDecision(t, filepath.Dir(path), "app", "a-approve", "accept-plan", "reject\n\nNo.")
		}, status: 409, reason: "already answered: reject (in chat or with decide answer)"},
		"void": {before: func(t *testing.T, path string) {
			if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
				return s.VoidDecision("void", "app", "a-approve", "accept-plan", "superseded: the spec changed")
			}); err != nil {
				t.Fatal(err)
			}
		}, status: 409, reason: "is void: superseded: the spec changed"},
		// A cancel now voids or refuses an open decision, so a cancelled task keeps one open only as a record an
		// earlier release left (docs/decisions/closed-tasks-have-no-open-decisions.md): void, cancel, then write
		// the decision back open beneath the store's guards.
		"cancelled task": {before: func(t *testing.T, path string) {
			open := pageDecision(t, path, "a-approve", "accept-plan")
			if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
				if _, err := s.VoidDecision("void", "app", "a-approve", "accept-plan", "the task is cancelled"); err != nil {
					return store.Receipt{}, err
				}
				return s.CancelTask("cancel", "app", "a-approve", "not needed")
			}); err != nil {
				t.Fatal(err)
			}
			writeLegacyDecision(t, path, open)
		}, status: 409, reason: "cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			h, path, log := pageFixture(t)
			token := pageToken(t, h)
			if c.before != nil {
				c.before(t, path)
			}
			before := pageDecision(t, path, "a-approve", "accept-plan")
			form := url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "option": {"approve"}, "text": {"My <words>."}, "token": {token}}
			for k, v := range c.form {
				form[k] = v
			}
			w := postPage(t, h, "a-approve", form)
			body := w.Body.String()
			if w.Code != c.status || !strings.Contains(body, "<b>Refused · nothing written:</b> ") || !strings.Contains(body, c.reason) {
				t.Fatalf("status %d, want %d with %q:\n%s", w.Code, c.status, c.reason, body)
			}
			if len(form["text"][0]) < 100 && !strings.Contains(body, strings.ReplaceAll(strings.ReplaceAll(form["text"][0], "<", "&lt;"), ">", "&gt;")+"</textarea>") {
				t.Errorf("the words are not kept in the box")
			}
			if after := pageDecision(t, path, "a-approve", "accept-plan"); !reflect.DeepEqual(after, before) || len(pageMessages(t, path)) != 0 || len(log.events) != 0 {
				t.Errorf("a refusal wrote: %+v, messages %+v, events %q", after, pageMessages(t, path), log.events)
			}
		})
	}
}

// TestWebAnswerBusyStore: a writer holding the store's lock past the page's wait refuses the answer with 503,
// writing nothing.
func TestWebAnswerBusyStore(t *testing.T) {
	h, path, _ := pageFixture(t)
	old := webLockWait
	webLockWait = 50 * time.Millisecond
	t.Cleanup(func() { webLockWait = old })
	token := pageToken(t, h)
	s, err := store.Open(path, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	w := postPage(t, h, "a-approve", url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "option": {"approve"}, "text": {"Go."}, "token": {token}})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503\n%s", w.Code, w.Body)
	}
	if d := pageDecision(t, path, "a-approve", "accept-plan"); !d.Open() {
		t.Fatalf("a busy store recorded the answer: %+v", d)
	}
}
