package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// otherIdentity is a Tailscale login that is not the Guildmaster's.
type otherIdentity struct{}

func (otherIdentity) Login(context.Context, string) (string, error) { return "friend@example.com", nil }

// noIdentity is a request whose node tailscale does not know.
type noIdentity struct{}

func (noIdentity) Login(context.Context, string) (string, error) { return "", ErrNoIdentity }

func poll(t *testing.T, s *Server, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := request(http.MethodGet, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	s.ServeHTTP(w, r)
	return w
}

// TestSinceReply: the since route answers the Guildmaster's poll with the server's start id, the conversation's
// last number and how many of the messages after the page's are the Director's, with the newest one's tag and time,
// read through Source.Tail alone, never the page model; it writes nothing and carries the page's headers.
func TestSinceReply(t *testing.T) {
	s := answeringTalkServer(talkTask())
	src := s.o.Source.(*fakeSource)
	for _, tc := range []struct {
		target string
		want   sinceReply
	}{
		{"/guildhall/d145/since?after=10", sinceReply{Start: "s", Last: 10}},
		{"/guildhall/d145/since", sinceReply{Start: "s", Last: 10, New: 6, Tag: "plan-9", At: "21:40"}},
		// After 7, the Director wrote 8 and 10; the Guildmaster's 9 is not counted.
		{"/guildhall/d145/since?after=7", sinceReply{Start: "s", Last: 10, New: 2, Tag: "plan-9", At: "21:40"}},
		{"/guildhall/d145/since?after=8", sinceReply{Start: "s", Last: 10, New: 1, Tag: "plan-9", At: "21:40"}},
		{"/guildhall/d145/since?after=99", sinceReply{Start: "s", Last: 10}},
	} {
		src.calls, src.tails = 0, 0
		w := poll(t, s, tc.target, map[string]string{"Sec-Fetch-Site": "same-origin"})
		var got sinceReply
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != tc.want {
			t.Errorf("%s: %d %s, want %+v", tc.target, w.Code, w.Body, tc.want)
		}
		if src.calls != 0 || src.tails != 1 {
			t.Errorf("%s: %d page reads and %d tail reads, want only one tail read", tc.target, src.calls, src.tails)
		}
		h := w.Header()
		if h.Get("Content-Type") != "application/json" || h.Get("Content-Security-Policy") != CSP || h.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", tc.target, h)
		}
	}
	if w := poll(t, s, "/guildhall/d145/since?after=7", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tag":"plan-9"`) {
		t.Errorf("a poll with no Sec-Fetch-Site: %d %s", w.Code, w.Body)
	}
	src.tasks["guildhall/d145"] = Task{Repository: "guildhall", ID: "d145", State: "open"}
	if w := poll(t, s, "/guildhall/d145/since", nil); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"start":"s","last":0,"new":0}` {
		t.Errorf("a task with no talk: %d %s", w.Code, w.Body)
	}
}

// TestSinceRefusals: the since route is 421 for another Host before anything is read; it is refused for any login but
// the Guildmaster's and on a read-only server, for a request another site sent, for an after that is not a message
// number, and for an unknown task or a busy store, each with no reply; a POST to it is 404.
func TestSinceRefusals(t *testing.T) {
	gm := answeringTalkServer(talkTask())
	other := answeringTalkServer(talkTask())
	other.o.Identity = otherIdentity{}
	unknown := answeringTalkServer(talkTask())
	unknown.o.Identity = noIdentity{}
	readOnly := talkServer(talkTask())
	busy := answeringTalkServer(talkTask())
	busy.o.Source.(*fakeSource).err = ErrBusy
	for _, tc := range []struct {
		name   string
		s      *Server
		method string
		target string
		header map[string]string
		host   string
		status int
	}{
		{"wrong host", gm, http.MethodGet, "/guildhall/d145/since", nil, "evil.example:4388", http.StatusMisdirectedRequest},
		{"another login", other, http.MethodGet, "/guildhall/d145/since", nil, "", http.StatusForbidden},
		{"no identity", unknown, http.MethodGet, "/guildhall/d145/since", nil, "", http.StatusForbidden},
		{"read-only server", readOnly, http.MethodGet, "/guildhall/d145/since", nil, "", http.StatusForbidden},
		{"cross-site", gm, http.MethodGet, "/guildhall/d145/since", map[string]string{"Sec-Fetch-Site": "cross-site"}, "", http.StatusForbidden},
		{"same-site", gm, http.MethodGet, "/guildhall/d145/since", map[string]string{"Sec-Fetch-Site": "same-site"}, "", http.StatusForbidden},
		{"negative after", gm, http.MethodGet, "/guildhall/d145/since?after=-1", nil, "", http.StatusBadRequest},
		{"padded after", gm, http.MethodGet, "/guildhall/d145/since?after=07", nil, "", http.StatusBadRequest},
		{"word after", gm, http.MethodGet, "/guildhall/d145/since?after=x", nil, "", http.StatusBadRequest},
		{"unknown task", gm, http.MethodGet, "/guildhall/d999/since", nil, "", http.StatusNotFound},
		{"busy store", busy, http.MethodGet, "/guildhall/d145/since", nil, "", http.StatusServiceUnavailable},
		{"post", gm, http.MethodPost, "/guildhall/d145/since", nil, "", http.StatusNotFound},
	} {
		src := tc.s.o.Source.(*fakeSource)
		src.calls, src.tails = 0, 0
		w := httptest.NewRecorder()
		r := request(tc.method, tc.target, nil)
		if tc.host != "" {
			r.Host = tc.host
		}
		for k, v := range tc.header {
			r.Header.Set(k, v)
		}
		tc.s.ServeHTTP(w, r)
		if w.Code != tc.status || strings.Contains(w.Body.String(), `"start"`) {
			t.Errorf("%s: %d %s, want %d and no reply", tc.name, w.Code, w.Body, tc.status)
		}
		if src.calls != 0 {
			t.Errorf("%s: the page model was read", tc.name)
		}
		readTail := tc.status == http.StatusNotFound && tc.method == http.MethodGet || tc.status == http.StatusServiceUnavailable
		if !readTail && src.tails != 0 {
			t.Errorf("%s: the tail was read before the refusal", tc.name)
		}
	}
}

// TestNewReplyBar: the Guildmaster's page of an open task carries the bar at the top of Now, hidden, with the since
// route, the page's path, its last message number and the server's start id for poll.js, and Reload as a plain
// link; no one else's page, and no closed task's, has it, so with scripts off it never shows.
func TestNewReplyBar(t *testing.T) {
	body := get(t, answeringTalkServer(talkTask()), "/guildhall/d145").Body.String()
	want := `<div class="newbar" id="newbar" hidden data-since="/guildhall/d145/since" data-base="/guildhall/d145" data-last="10" data-start="s">`
	if !strings.Contains(body, want) {
		t.Fatalf("the Guildmaster's page lacks the bar %s", want)
	}
	now := body[strings.Index(body, `<section class="c2-now">`):]
	if i, j := strings.Index(now, `id="newbar"`), strings.Index(now, `<div class="ask`); i < 0 || j < 0 || i > j {
		t.Errorf("the bar is not at the top of Now, above the asks")
	}
	for _, line := range []string{`<p class="nbm" hidden>New from the Director on <b class="nbt"></b> · <time class="nbw"></time> · <a class="nbr" href="/guildhall/d145">Reload</a></p>`,
		`<p class="nbs" hidden><b>This page is out of date:</b>`} {
		if !strings.Contains(body, line) {
			t.Errorf("the bar lacks %s", line)
		}
	}
	if strings.Count(body, `id="newbar"`) != 1 {
		t.Error("the page has more than one bar")
	}

	other := answeringTalkServer(talkTask())
	other.o.Identity = otherIdentity{}
	closed := talkTask()
	closed.State = "done"
	for name, s := range map[string]*Server{"another login": other, "read-only": talkServer(talkTask()), "closed task": answeringTalkServer(closed)} {
		if body := get(t, s, "/guildhall/d145").Body.String(); strings.Contains(body, `id="newbar"`) {
			t.Errorf("%s: the page has the bar", name)
		}
	}
	reader := get(t, answeringTalkServer(talkTask()), "/guildhall/d145")
	checkOwnScripts(t, "the Guildmaster's page", reader)
}
