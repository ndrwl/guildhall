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

// summaryForm is a press of act on summary 13 of grill-2 with the page's token and words.
func summaryForm(act, words string) url.Values {
	return url.Values{"tag": {"grill-2"}, "summary": {"13"}, "act": {act}, "text": {words}, "token": {web.FixtureToken}}
}

// TestSummaryPosts: Confirm and Request changes reach the Answerer's Summary with the summary, the outcome, the words
// and the login, and redirect to the attempt's talk.
func TestSummaryPosts(t *testing.T) {
	for act, outcome := range map[string]string{"confirm": web.SummaryConfirmed, "changes": web.SummaryChangesRequested} {
		f := &fakeAnswerer{}
		w := request(answering(t, f), http.MethodPost, "/guildhall/d122/summary", gmAddr, summaryForm(act, "JSON too."), nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/guildhall/d122#grill-2-talk" {
			t.Fatalf("%s: %d %q\n%s", act, w.Code, w.Header().Get("Location"), w.Body)
		}
		want := web.SummaryPost{Repository: "guildhall", TaskID: "d122", Tag: "grill-2", Summary: 13, Outcome: outcome, Words: "JSON too.", Login: gmLogin}
		if len(f.summaries) != 1 || f.summaries[0] != want {
			t.Errorf("%s: summaries = %+v", act, f.summaries)
		}
	}
}

// TestSummaryRefusals: the summary route passes the same gate as every POST, and each refusal shows the page again
// with its status, its reason and the words kept.
func TestSummaryRefusals(t *testing.T) {
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
		"no tag":            {form: url.Values{"tag": {""}}, status: 400, reason: "the summary names no attempt"},
		"no summary":        {form: url.Values{"summary": {"x"}}, status: 400, reason: "the form names no summary"},
		"unknown button":    {form: url.Values{"act": {"approve"}}, status: 400, reason: "the form names no button of the summary"},
		"no words":          {err: web.Refuse(400, "Request changes takes your words"), status: 400, reason: "Request changes takes your words"},
		"other button":      {err: web.Refuse(409, "already answered"), status: 409, reason: "already answered"},
		"busy":              {err: web.ErrBusy, status: 503, reason: "the store is busy"},
		"failure":           {err: errors.New("open /secret: denied"), status: 500, reason: "Guildhall could not record it"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAnswerer{err: c.err}
			v := summaryForm("changes", "Keep <CSV> & \"JSON\"")
			for k, x := range c.form {
				v[k] = x
			}
			remote := c.remote
			if remote == "" {
				remote = gmAddr
			}
			w := request(answering(t, f), http.MethodPost, "/guildhall/d122/summary", remote, v, c.headers)
			body := w.Body.String()
			if w.Code != c.status {
				t.Fatalf("status %d, want %d\n%s", w.Code, c.status, body)
			}
			if c.err == nil && len(f.summaries) != 0 {
				t.Fatalf("a refused press reached the Answerer: %+v", f.summaries)
			}
			if !strings.Contains(body, "<b>Refused · nothing written:</b> "+c.reason) {
				t.Errorf("the reason is not shown:\n%s", body)
			}
			if !strings.Contains(body, "Keep &lt;CSV&gt; &amp; &#34;JSON&#34;</textarea>") {
				t.Errorf("the words are not kept, escaped")
			}
			if strings.Contains(body, "/secret") {
				t.Error("the page shows the internal error")
			}
		})
	}
	ro := web.AnsweringFixture(t, webtest.Logins{"100.101.102.103": gmLogin}, "", &fakeAnswerer{})
	if w := request(ro, http.MethodPost, "/guildhall/d122/summary", gmAddr, summaryForm("confirm", ""), nil); w.Code != 403 || !strings.Contains(w.Body.String(), "names no guildmaster login") {
		t.Errorf("no guildmaster: %d", w.Code)
	}
	if w := request(answering(t, &fakeAnswerer{}), http.MethodGet, "/guildhall/d122/summary", gmAddr, nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("GET summary: %d, want 404", w.Code)
	}
}

// TestSummaryWrongHost: the summary route is refused 421 for any Host but the configured one, before the gate, and
// an Origin naming another host is refused by the gate.
func TestSummaryWrongHost(t *testing.T) {
	f := &fakeAnswerer{}
	h := answering(t, f)
	for _, host := range []string{"evil.example:4388", "andrew-lap-wsl.tail5da40a.ts.net:4389", ""} {
		r := httptest.NewRequest(http.MethodPost, "/guildhall/d122/summary", strings.NewReader(summaryForm("confirm", "").Encode()))
		r.Host, r.RemoteAddr = host, gmAddr
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://andrew-lap-wsl.tail5da40a.ts.net:4388")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: %d, want 421", host, w.Code)
		}
	}
	w := request(h, http.MethodPost, "/guildhall/d122/summary", gmAddr, summaryForm("confirm", ""), map[string]string{"Origin": "http://evil.example:4388", "Sec-Fetch-Site": "same-origin"})
	if w.Code != http.StatusForbidden || len(f.summaries) != 0 {
		t.Errorf("an Origin of another host: %d, %+v", w.Code, f.summaries)
	}
}
