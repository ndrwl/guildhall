package web_test

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/web/webtest"
)

// sayForm is the Say box's form about attempt tag with the page's token.
func sayForm(tag, text string) url.Values {
	return url.Values{"tag": {tag}, "token": {web.FixtureToken}, "text": {text}}
}

// TestConfirmPosts: the Confirm tile posts the recommendation's number, which reaches the Answerer as Confirm with no
// option of its own and the words box's words; a number that does not read is refused, and Send words only with a
// stray confirm still only sends the words.
func TestConfirmPosts(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	v := form("grant-1", "", "", "Why does build-3 need the network?")
	v.Set("confirm", "11")
	w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/guildhall/d122#grant-1" {
		t.Fatalf("confirm: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}
	want := web.Answer{Repository: "guildhall", TaskID: "d122", Decision: "grant-1", Text: "Why does build-3 need the network?", Login: gmLogin, Confirm: 11}
	if len(f.answers) != 1 || !reflect.DeepEqual(f.answers[0], want) {
		t.Fatalf("answers = %+v, want %+v", f.answers, want)
	}

	for _, bad := range []string{"x", "0", "-3"} {
		v.Set("confirm", bad)
		if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil); w.Code != http.StatusBadRequest ||
			!strings.Contains(w.Body.String(), "the confirmation names no recommendation") {
			t.Errorf("confirm=%s: %d", bad, w.Code)
		}
	}
	v.Set("confirm", "11")
	v.Set("send", "words")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", gmAddr, v, nil); w.Code != http.StatusSeeOther || len(f.sent) != 1 || f.sent[0].Confirm != 0 || len(f.answers) != 1 {
		t.Errorf("words only with a confirm: %d %+v %+v", w.Code, f.sent, f.answers)
	}
	// The other login's page has no Confirm to post, and a forged one passes no gate.
	v.Del("send")
	if w := request(h, http.MethodPost, "/guildhall/d122/answer", otherAddr, v, nil); w.Code != http.StatusForbidden || len(f.answers) != 1 {
		t.Errorf("another login's confirm: %d", w.Code)
	}
}

// TestSayPosts: the Say box's words reach the Answerer's Say with the attempt and the login, and the redirect lands
// on the attempt's talk with the box's sent state.
func TestSayPosts(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	w := request(h, http.MethodPost, "/guildhall/d122/say", gmAddr, sayForm("review--s2-2", "Is F4 the only finding?"), nil)
	id := web.NoteID("guildhall", "d122", "say:review--s2-2", "Is F4 the only finding?")
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/guildhall/d122?said="+id+"#review--s2-2-talk" {
		t.Fatalf("say: %d %q\n%s", w.Code, loc, w.Body)
	}
	want := web.Answer{Repository: "guildhall", TaskID: "d122", Tag: "review--s2-2", Text: "Is F4 the only finding?", Login: gmLogin}
	if len(f.said) != 1 || !reflect.DeepEqual(f.said[0], want) || len(f.answers) != 0 || len(f.sent) != 0 {
		t.Fatalf("said = %+v, answers %+v, sent %+v", f.said, f.answers, f.sent)
	}
	body := request(h, http.MethodGet, w.Header().Get("Location"), gmAddr, nil, nil).Body.String()
	if !strings.Contains(body, "<b>Sent:</b> your words reached the Director as a message; its reply shows in this talk.") {
		t.Error("the redirect does not show the Say box's sent state")
	}
	if body := request(h, http.MethodGet, "/guildhall/d122?said=other.x", gmAddr, nil, nil).Body.String(); strings.Contains(body, "<b>Sent:</b>") {
		t.Error("a said that is no page note shows the sent state")
	}
}

// TestSayRefusals: the Say route passes the same gate as the answer route, and each refusal writes nothing and shows
// the page again with its status, the reason in the box and the words kept.
func TestSayRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		remote  string
		form    url.Values
		headers map[string]string
		err     error
		status  int
		reason  string
	}{
		"another login":     {remote: otherAddr, status: 403, reason: "only the Guildmaster&#39;s Tailscale login can answer"},
		"no identity":       {remote: "192.0.2.1:1234", status: 403, reason: "only the Guildmaster&#39;s Tailscale login can answer"},
		"bad token":         {form: url.Values{"token": {"stale"}}, status: 403, reason: "the page is out of date"},
		"no token":          {form: url.Values{"token": {""}}, status: 403, reason: "the page is out of date"},
		"cross-origin":      {headers: map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"}, status: 403, reason: "the answer did not come from this page"},
		"no origin headers": {headers: map[string]string{}, status: 403, reason: "the answer did not come from this page"},
		"no tag":            {form: url.Values{"tag": {""}}, status: 400, reason: "the words name no attempt"},
		"closed task":       {err: web.Refuse(409, "this task is closed"), status: 409, reason: "this task is closed"},
		"busy":              {err: web.ErrBusy, status: 503, reason: "the store is busy"},
		"failure":           {err: errors.New("open /secret: denied"), status: 500, reason: "Guildhall could not record it"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAnswerer{err: c.err}
			h := answering(t, f)
			v := sayForm("review--s2-2", "Is <F4> & \"F5\" fixed?")
			for k, x := range c.form {
				v[k] = x
			}
			remote := c.remote
			if remote == "" {
				remote = gmAddr
			}
			w := request(h, http.MethodPost, "/guildhall/d122/say", remote, v, c.headers)
			body := w.Body.String()
			if w.Code != c.status {
				t.Fatalf("status %d, want %d\n%s", w.Code, c.status, body)
			}
			if c.err == nil && len(f.said) != 0 {
				t.Fatalf("refused words reached the Answerer: %+v", f.said)
			}
			if !strings.Contains(body, "<b>Refused · nothing sent:</b> "+c.reason) && !strings.Contains(body, "<b>Refused · nothing written:</b> "+c.reason) {
				t.Errorf("the reason is not shown:\n%s", body)
			}
			if !strings.Contains(body, "Is &lt;F4&gt; &amp; &#34;F5&#34; fixed?</textarea>") {
				t.Errorf("the words are not kept, escaped")
			}
			if strings.Contains(body, "/secret") {
				t.Error("the page shows the internal error")
			}
		})
	}
	ro := web.AnsweringFixture(t, webtest.Logins{"100.101.102.103": gmLogin}, "", &fakeAnswerer{})
	if w := request(ro, http.MethodPost, "/guildhall/d122/say", gmAddr, sayForm("review--s2-2", "Hi."), nil); w.Code != 403 || !strings.Contains(w.Body.String(), "names no guildmaster login") {
		t.Errorf("no guildmaster: %d", w.Code)
	}
	if w := request(answering(t, &fakeAnswerer{}), http.MethodGet, "/guildhall/d122/say", gmAddr, nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("GET say: %d, want 404", w.Code)
	}
}
