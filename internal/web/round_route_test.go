package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/web/webtest"
)

// roundForm is a press of act on round 13 of grill-2 with the page's token and the boxes' words.
func roundForm(act string, boxes map[string]string) url.Values {
	v := url.Values{"tag": {"grill-2"}, "round": {"13"}, "act": {act}, "token": {web.FixtureToken}}
	for k, w := range boxes {
		v.Set(k, w)
	}
	return v
}

// TestRoundPosts: each button reaches the Answerer's Round with the round, the button, its question and every box's
// words, and redirects to the round, the question or, after Send round or Accept all and send, the attempt's talk.
func TestRoundPosts(t *testing.T) {
	for act, want := range map[string]struct {
		act      string
		question int
		location string
	}{
		"save":        {web.RoundSave, 0, "/guildhall/d122#grill-2"},
		"send":        {web.RoundSend, 0, "/guildhall/d122#grill-2-talk"},
		"accept-all":  {web.RoundAcceptAll, 0, "/guildhall/d122#grill-2"},
		"accept-send": {web.RoundAcceptSend, 0, "/guildhall/d122#grill-2-talk"},
		"accept-3":    {web.RoundAccept, 3, "/guildhall/d122#grill-2-q3"},
		"words-1":     {web.RoundWords, 1, "/guildhall/d122#grill-2-q1"},
		"change-30":   {web.RoundChange, 30, "/guildhall/d122#grill-2-q30"},
	} {
		f := &fakeAnswerer{}
		w := request(answering(t, f), http.MethodPost, "/guildhall/d122/round", gmAddr, roundForm(act, map[string]string{"text-1": "CSV only.", "text-3": "", "text-31": "never read"}), nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want.location {
			t.Fatalf("%s: %d %q\n%s", act, w.Code, w.Header().Get("Location"), w.Body)
		}
		p := f.rounds
		if len(p) != 1 || p[0].Repository != "guildhall" || p[0].TaskID != "d122" || p[0].Tag != "grill-2" || p[0].Round != 13 || p[0].Act != want.act ||
			p[0].Question != want.question || p[0].Login != gmLogin || len(p[0].Boxes) != 2 || p[0].Boxes[1] != "CSV only." || p[0].Boxes[3] != "" {
			t.Errorf("%s: rounds = %+v", act, p)
		}
	}
}

// TestRoundPostsDrafts: the route passes the posted drafts number to Round, a form with none as 0.
func TestRoundPostsDrafts(t *testing.T) {
	for drafts, want := range map[string]int{"": 0, "0": 0, "42": 42} {
		f := &fakeAnswerer{}
		v := roundForm("save", nil)
		if drafts != "" {
			v.Set("drafts", drafts)
		}
		w := request(answering(t, f), http.MethodPost, "/guildhall/d122/round", gmAddr, v, nil)
		if w.Code != http.StatusSeeOther || len(f.rounds) != 1 || f.rounds[0].Drafts != want {
			t.Errorf("drafts %q: %d, rounds = %+v; want Drafts %d", drafts, w.Code, f.rounds, want)
		}
	}
}

// TestRoundRefusals: the round route passes the same gate as every POST, and each refusal shows the page again with
// its status, its reason and the boxes' words kept.
func TestRoundRefusals(t *testing.T) {
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
		"cross-origin":      {headers: map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"}, status: 403, reason: "the answer did not come from this page"},
		"no origin headers": {headers: map[string]string{}, status: 403, reason: "the answer did not come from this page"},
		"no tag":            {form: url.Values{"tag": {""}}, status: 400, reason: "the round names no attempt"},
		"no round":          {form: url.Values{"round": {"x"}}, status: 400, reason: "the form names no round"},
		"unknown button":    {form: url.Values{"act": {"accept-0"}}, status: 400, reason: "the form names no button of the round"},
		"bad drafts":        {form: url.Values{"drafts": {"x"}}, status: 400, reason: "the form&#39;s drafts number does not read"},
		"empty drafts":      {form: url.Values{"drafts": {""}}, status: 400, reason: "the form&#39;s drafts number does not read"},
		"negative drafts":   {form: url.Values{"drafts": {"-1"}}, status: 400, reason: "the form&#39;s drafts number does not read"},
		"unanswered":        {err: web.Refuse(409, "Q9 and Q10 have no answer yet; your words are saved"), status: 409, reason: "Q9 and Q10 have no answer yet; your words are saved"},
		"busy":              {err: web.ErrBusy, status: 503, reason: "the store is busy"},
		"failure":           {err: errors.New("open /secret: denied"), status: 500, reason: "Guildhall could not record it"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAnswerer{err: c.err}
			v := roundForm("save", map[string]string{"text-2": "Keep <CSV> & \"JSON\""})
			for k, x := range c.form {
				v[k] = x
			}
			remote := c.remote
			if remote == "" {
				remote = gmAddr
			}
			w := request(answering(t, f), http.MethodPost, "/guildhall/d122/round", remote, v, c.headers)
			body := w.Body.String()
			if w.Code != c.status {
				t.Fatalf("status %d, want %d\n%s", w.Code, c.status, body)
			}
			if c.err == nil && len(f.rounds) != 0 {
				t.Fatalf("a refused press reached the Answerer: %+v", f.rounds)
			}
			if !strings.Contains(body, "<b>Refused · nothing written:</b> "+c.reason) {
				t.Errorf("the reason is not shown:\n%s", body)
			}
			if !strings.Contains(body, "Q2: Keep &lt;CSV&gt; &amp; &#34;JSON&#34;</textarea>") {
				t.Errorf("the words are not kept, escaped")
			}
			if strings.Contains(body, "/secret") {
				t.Error("the page shows the internal error")
			}
		})
	}
	ro := web.AnsweringFixture(t, webtest.Logins{"100.101.102.103": gmLogin}, "", &fakeAnswerer{})
	if w := request(ro, http.MethodPost, "/guildhall/d122/round", gmAddr, roundForm("save", nil), nil); w.Code != 403 || !strings.Contains(w.Body.String(), "names no guildmaster login") {
		t.Errorf("no guildmaster: %d", w.Code)
	}
	if w := request(answering(t, &fakeAnswerer{}), http.MethodGet, "/guildhall/d122/round", gmAddr, nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("GET round: %d, want 404", w.Code)
	}
}

// TestRoundWrongHost: the round route is refused 421 for any Host but the configured one, before the gate, and an
// Origin naming another host is refused by the gate.
func TestRoundWrongHost(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	for _, host := range []string{"evil.example:4388", "andrew-lap-wsl.tail5da40a.ts.net:4389", ""} {
		r := httptest.NewRequest(http.MethodPost, "/guildhall/d122/round", strings.NewReader(roundForm("save", nil).Encode()))
		r.Host, r.RemoteAddr = host, gmAddr
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://andrew-lap-wsl.tail5da40a.ts.net:4388")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: %d, want 421", host, w.Code)
		}
	}
	w := request(h, http.MethodPost, "/guildhall/d122/round", gmAddr, roundForm("save", nil), map[string]string{"Origin": "http://evil.example:4388", "Sec-Fetch-Site": "same-origin"})
	if w.Code != http.StatusForbidden || len(f.rounds) != 0 {
		t.Errorf("an Origin of another host: %d, %+v", w.Code, f.rounds)
	}
}
