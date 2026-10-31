package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/web/webtest"
)

// The fixture's tailnet nodes: the Guildmaster's and another login's.
const (
	gmAddr    = "100.101.102.103:51000"
	otherAddr = "100.101.102.104:51000"
	gmLogin   = "gm@example.com"
)

// fakeAnswerer records what the page gives it, refusing with err when set.
type fakeAnswerer struct {
	answers, sent []web.Answer
	err           error
}

func (f *fakeAnswerer) Answer(a web.Answer) error {
	if f.err != nil {
		return f.err
	}
	f.answers = append(f.answers, a)
	return nil
}

func (f *fakeAnswerer) Send(a web.Answer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, a)
	return web.NoteID(a.Repository, a.TaskID, a.Decision, a.Text), nil
}

// answering is the fixture's server answering for gmLogin through f.
func answering(t *testing.T, f *fakeAnswerer) *web.Server {
	t.Helper()
	return web.AnsweringFixture(t, webtest.Logins{"100.101.102.103": gmLogin, "100.101.102.104": "other@example.com"}, gmLogin, f)
}

// request is one request from remote, with the page's form and its same-origin headers unless headers replaces them.
func request(h http.Handler, method, target, remote string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	r.Host = "andrew-lap-wsl.tail5da40a.ts.net:4388"
	r.RemoteAddr = remote
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if headers == nil {
		headers = map[string]string{"Origin": "http://andrew-lap-wsl.tail5da40a.ts.net:4388", "Sec-Fetch-Site": "same-origin"}
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func form(decision, subject, option, text string) url.Values {
	v := url.Values{"decision": {decision}, "subject": {subject}, "token": {web.FixtureToken}, "text": {text}}
	if option != "" {
		v.Set("option", option)
	}
	return v
}

const loopSubject = "3f9c2ab0c0ffee0000000000000000000000beef"

// TestAnsweringPage: the Guildmaster's login gets a form on every ask, with the words box, enabled tiles, Send
// words only and "Answering as"; any other login, and a request with no identity, gets the read-only page.
func TestAnsweringPage(t *testing.T) {
	h := answering(t, &fakeAnswerer{})
	w := request(h, http.MethodGet, "/guildhall/d122", gmAddr, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<form method="post" action="/guildhall/d122/answer"><input type="hidden" name="decision" value="review--s2-x2"><input type="hidden" name="subject" value="` + loopSubject + `"><input type="hidden" name="token" value="` + web.FixtureToken + `">`,
		`<label class="yw" for="w-review--s2-x2">Your words <span>optional with an answer</span></label><textarea id="w-review--s2-x2" name="text" rows="3"></textarea>`,
		`<button class="opt carry" type="submit" name="option" value="continue"><b>Continue</b><span>another round of the loop</span></button>`,
		`<button class="opt pos" type="submit" name="option" value="accept">`, `<button class="opt neg" type="submit" name="option" value="reject">`,
		`<button class="opt" type="submit" name="send" value="words"><b>Send words only</b><span>answers nothing; your words reach the Director as a message</span></button>`,
		`<p class="who">Answering as <b>gm@example.com</b> · your Tailscale login</p>`,
		`<p class="who">Deferred: still yours, for later. Answering as <b>gm@example.com</b>`,
		`<meta name="referrer" content="same-origin">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Guildmaster's page lacks %s", want)
		}
	}
	if n := strings.Count(body, "<form "); n != 3 {
		t.Errorf("%d forms, want one per ask (3)", n)
	}
	for _, bad := range []string{"<script", " disabled>"} {
		if strings.Contains(body, bad) {
			t.Errorf("the Guildmaster's page holds %q", bad)
		}
	}
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin so a form's POST carries its Origin", got)
	}
	golden := filepath.Join("testdata", "task.answering.golden.html")
	if web.UpdateGolden() {
		if err := os.WriteFile(golden, w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/web -run TestAnsweringPage -update", err)
	}
	if got := strings.ReplaceAll(body, "\r\n", "\n"); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("the answering page differs from %s; review it and run with -update", golden)
	}

	for name, remote := range map[string]string{"another login": otherAddr, "no identity": "192.0.2.1:1234", "unknown node": "100.64.0.9:1"} {
		body := request(h, http.MethodGet, "/guildhall/d122", remote, nil, nil).Body.String()
		for _, bad := range []string{"<form", "<textarea", "Send words only", "Answering as"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: the page holds %q", name, bad)
			}
		}
		if !strings.Contains(body, "Only the Guildmaster's Tailscale login can answer here") || !strings.Contains(body, `type="button" disabled`) {
			t.Errorf("%s: the page is not read-only", name)
		}
	}
	if body := request(h, http.MethodGet, "/guildhall/d122", otherAddr, nil, nil).Body.String(); !strings.Contains(body, "can answer here; you are other@example.com.") {
		t.Error("another login is not told who it is")
	}
	// The reader page carries the same forms for the Guildmaster.
	if body := request(h, http.MethodGet, "/guildhall/d122/doc/plan-1-spec", gmAddr, nil, nil).Body.String(); !strings.Contains(body, `<form method="post" action="/guildhall/d122/answer">`) {
		t.Error("the reader page has no form for the Guildmaster")
	}
}

// TestAnswerRecordsAndRedirects: an option with words reaches the Answerer with the login and redirects to the ask;
// words only reach Send and the redirect shows the ask's words-only-sent state; an ask with no options is answered
// by its words.
func TestAnswerRecordsAndRedirects(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, form("review--s2-x2", loopSubject, "accept", "Good enough.\r\nShip it."), nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/guildhall/d122#review--s2-x2" {
		t.Fatalf("answer: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}
	want := web.Answer{Repository: "guildhall", TaskID: "d122", Decision: "review--s2-x2", Subject: loopSubject, Option: "accept", Text: "Good enough.\r\nShip it.", Login: gmLogin}
	if len(f.answers) != 1 || f.answers[0] != want {
		t.Fatalf("answers = %+v, want %+v", f.answers, want)
	}

	words := form("grant-1", "", "", "Wait for the rebase first.")
	words.Set("send", "words")
	w = request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, words, nil)
	id := web.NoteID("guildhall", "d122", "grant-1", "Wait for the rebase first.")
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/guildhall/d122?ask=grant-1&sent="+id+"#grant-1" {
		t.Fatalf("words: %d %q", w.Code, loc)
	}
	if len(f.sent) != 1 || f.sent[0].Option != "" || f.sent[0].Text != "Wait for the rebase first." || len(f.answers) != 1 {
		t.Fatalf("sent = %+v, answers = %+v", f.sent, f.answers)
	}
	body := request(h, http.MethodGet, w.Header().Get("Location"), gmAddr, nil, nil).Body.String()
	if !strings.Contains(body, `<div class="ask sent" id="grant-1">`) || !strings.Contains(body, "<b>Words only sent:</b> your words reached the Director as a message; nothing is answered.") {
		t.Errorf("the redirect does not show the words-only-sent state:\n%s", body)
	}

	byWords := form("deliver-1", "", "", "Merge after d7.")
	byWords.Set("send", "answer")
	byWords.Set("option", "merge")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, byWords, nil); w.Code != http.StatusSeeOther || len(f.answers) != 2 || f.answers[1].Option != "" {
		t.Errorf("an answer by words alone: %d %+v", w.Code, f.answers)
	}
}

// TestAnswerRefusals: each refusal writes nothing and shows the page again with its status, the reason on the ask
// and the words kept in the box.
func TestAnswerRefusals(t *testing.T) {
	sameOrigin := map[string]string{"Origin": "http://andrew-lap-wsl.tail5da40a.ts.net:4388"}
	for name, c := range map[string]struct {
		remote  string
		form    url.Values
		headers map[string]string
		err     error
		status  int
		reason  string
	}{
		"another login":      {remote: otherAddr, status: 403, reason: "only the Guildmaster&#39;s Tailscale login can answer"},
		"no identity":        {remote: "192.0.2.1:1234", status: 403, reason: "only the Guildmaster&#39;s Tailscale login can answer"},
		"bad token":          {form: url.Values{"token": {"stale"}}, status: 403, reason: "the page is out of date"},
		"cross-origin":       {headers: map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"}, status: 403, reason: "the answer did not come from this page"},
		"other port":         {headers: map[string]string{"Origin": "http://andrew-lap-wsl.tail5da40a.ts.net:4387"}, status: 403, reason: "the answer did not come from this page"},
		"no origin headers":  {headers: map[string]string{}, status: 403, reason: "the answer did not come from this page"},
		"null origin":        {headers: map[string]string{"Origin": "null"}, status: 403, reason: "the answer did not come from this page"},
		"cross-site fetch":   {headers: map[string]string{"Sec-Fetch-Site": "same-site"}, status: 403, reason: "the answer did not come from this page"},
		"no option":          {form: url.Values{"option": {""}}, status: 400, reason: "choose an answer, or send your words only"},
		"changed subject":    {err: web.Refuse(409, "changed since you loaded the page"), status: 409, reason: "changed since you loaded the page"},
		"already answered":   {err: web.Refuse(409, "already answered: approved"), status: 409, reason: "already answered: approved"},
		"busy":               {err: web.ErrBusy, status: 503, reason: "the store is busy"},
		"failure":            {err: errors.New("open /secret: denied"), status: 500, reason: "Guildhall could not record it"},
		"origin, no fetch":   {headers: sameOrigin, err: web.Refuse(409, "the task is cancelled"), status: 409, reason: "the task is cancelled"},
		"null origin, fetch": {headers: map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, err: web.Refuse(409, "fetch passes"), status: 409, reason: "fetch passes"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAnswerer{err: c.err}
			h := answering(t, f)
			v := form("review--s2-x2", loopSubject, "continue", "Keep <going> & \"fix\" F4.")
			for k, x := range c.form {
				v[k] = x
			}
			remote := c.remote
			if remote == "" {
				remote = gmAddr
			}
			w := request(h, http.MethodPost, "/guildhall/d122/answer", remote, v, c.headers)
			body := w.Body.String()
			if w.Code != c.status {
				t.Fatalf("status %d, want %d\n%s", w.Code, c.status, body)
			}
			if c.err == nil && (len(f.answers) != 0 || len(f.sent) != 0) {
				t.Fatalf("a refused answer reached the Answerer: %+v %+v", f.answers, f.sent)
			}
			if !strings.Contains(body, `<div class="ask refused" id="review--s2-x2">`) || !strings.Contains(body, "<b>Refused · nothing written:</b> "+c.reason) {
				t.Errorf("the reason is not on the ask:\n%s", body)
			}
			if !strings.Contains(body, "Keep &lt;going&gt; &amp; &#34;fix&#34; F4.</textarea>") {
				t.Errorf("the words are not kept, escaped, in the box")
			}
			if strings.Contains(body, "/secret") {
				t.Error("the page shows the internal error")
			}
		})
	}
}

// TestAnswerRefusedForAGoneAsk: a refusal about a decision that is no longer an ask shows at the top of Now with
// the words kept; a server with no Guildmaster login refuses every answer.
func TestAnswerRefusedForAGoneAsk(t *testing.T) {
	f := &fakeAnswerer{err: web.Refuse(409, "already answered: approve")}
	h := answering(t, f)
	w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, form("accept-1", "", "approved", "yes"), nil)
	body := w.Body.String()
	if w.Code != 409 || !strings.Contains(body, `<div class="ask refused" id="accept-1">`) || !strings.Contains(body, "#accept-1 is not waiting on you") ||
		!strings.Contains(body, "already answered: approve") || !strings.Contains(body, `<textarea rows="3" readonly>yes</textarea>`) {
		t.Errorf("gone ask: %d\n%s", w.Code, body)
	}
	ro := web.AnsweringFixture(t, webtest.Logins{"100.101.102.103": gmLogin}, "", f)
	w = request(ro, http.MethodPost, "/guildhall/d122/answer", gmAddr, form("grant-1", "", "approve", ""), nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "names no guildmaster login") {
		t.Errorf("no guildmaster: %d", w.Code)
	}
}

// TestForeignHostRefused: a request whose Host is not the configured [web] address, as a DNS-rebinding page's
// is, is refused on every route before anything is read, so the page and its form token are never shown and an
// answer never reaches the Answerer, whichever Origin it carries; the configured host in another case is served.
func TestForeignHostRefused(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	at := func(method, target, host string, v url.Values, origin string) *httptest.ResponseRecorder {
		var body *strings.Reader
		if v != nil {
			body = strings.NewReader(v.Encode())
		} else {
			body = strings.NewReader("")
		}
		r := httptest.NewRequest(method, target, body)
		r.Host, r.RemoteAddr = host, gmAddr
		if v != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const evil = "evil.example:4388"
	for name, c := range map[string]struct{ host, origin string }{
		"foreign host, foreign origin": {evil, "http://" + evil},
		"foreign host, our origin":     {evil, "http://" + web.FixtureAddress},
		"our name, other port":         {"andrew-lap-wsl.tail5da40a.ts.net:4387", "http://andrew-lap-wsl.tail5da40a.ts.net:4387"},
		"bare tailnet ip":              {"100.101.102.103:4388", "http://100.101.102.103:4388"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, target := range []string{"/guildhall/d122", "/guildhall/d122/doc/spec", "/nothing"} {
				w := at(http.MethodGet, target, c.host, nil, c.origin)
				if w.Code != http.StatusMisdirectedRequest || strings.Contains(w.Body.String(), web.FixtureToken) {
					t.Errorf("GET %s at %s: %d, want 421 without the token", target, c.host, w.Code)
				}
			}
			w := at(http.MethodPost, "/guildhall/d122/answer", c.host, form("review--s2-x2", loopSubject, "continue", ""), c.origin)
			if w.Code != http.StatusMisdirectedRequest || !strings.Contains(w.Body.String(), "configured address") {
				t.Errorf("POST at %s: %d, want 421\n%s", c.host, w.Code, w.Body)
			}
			if len(f.answers) != 0 || len(f.sent) != 0 {
				t.Fatalf("an answer at a foreign host reached the Answerer: %+v %+v", f.answers, f.sent)
			}
		})
	}
	upper := strings.ToUpper(web.FixtureAddress)
	if w := at(http.MethodGet, "/guildhall/d122", upper, nil, ""); w.Code != http.StatusOK {
		t.Errorf("GET at %s: %d, want the page", upper, w.Code)
	}
	if w := at(http.MethodPost, "/guildhall/d122/answer", upper, form("review--s2-x2", loopSubject, "continue", ""), "http://"+upper); w.Code != http.StatusSeeOther ||
		len(f.answers) != 1 {
		t.Errorf("POST at %s: %d %+v, want it recorded", upper, w.Code, f.answers)
	}
}

// TestIdentifierContract runs the Identifier contract against the fake and against the real Whois over a fake
// tailscale command, which this test binary plays.
func TestIdentifierContract(t *testing.T) {
	known := map[string]string{gmAddr: gmLogin, "[fd7a:115c:a1e0::5]:443": "other@example.com"}
	t.Run("fake", func(t *testing.T) {
		webtest.RunIdentifier(t, webtest.Logins{"100.101.102.103": gmLogin, "fd7a:115c:a1e0::5": "other@example.com"}, known, "100.64.0.9:1")
	})
	t.Run("whois", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(fakeTailscaleEnv, "1")
		webtest.RunIdentifier(t, web.Whois{Command: []string{exe, "-test.run=^TestFakeTailscale$", "--"}}, known, "100.64.0.9:1")
	})
}

// fakeTailscaleEnv makes this test binary play tailscale whois in TestFakeTailscale.
const fakeTailscaleEnv = "GUILDHALL_WEB_FAKE_TAILSCALE"

// TestFakeTailscale plays `tailscale whois --json <addr>` for the contract's two nodes when the test binary runs as
// the fake command; otherwise it does nothing.
func TestFakeTailscale(t *testing.T) {
	if os.Getenv(fakeTailscaleEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) != 3 || args[0] != "whois" || args[1] != "--json" {
		os.Exit(2)
	}
	switch args[2] {
	case gmAddr:
		_, _ = os.Stdout.WriteString(`{"Node":{"Name":"gm-phone."},"UserProfile":{"ID":1,"LoginName":"gm@example.com","DisplayName":"GM"}}`)
	case "[fd7a:115c:a1e0::5]:443":
		_, _ = os.Stdout.WriteString(`{"UserProfile":{"LoginName":"other@example.com"}}`)
	default:
		_, _ = os.Stderr.WriteString("peer not found\n")
		os.Exit(1)
	}
	os.Exit(0)
}

// TestWhoisReal runs the contract against the machine's own tailscale, when it has one that is up: this node's
// address gives the login tailscale status names for it.
func TestWhoisReal(t *testing.T) {
	self, login, ok := web.TailscaleSelf(t)
	if !ok {
		t.Skip("no tailscale, or it is not up")
	}
	webtest.RunIdentifier(t, web.Whois{}, map[string]string{self: login}, "100.127.255.254:1")
}
