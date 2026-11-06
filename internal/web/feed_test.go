package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListFeed: GET /list.json goes through ServeHTTP's headers, the page's CSP, nosniff, no-store and the
// referrer policy, and answers JSON: the band's lines, Needs you, the stage groups in order with the merge queue in
// queue order and its banner, Recently finished and the stats, every time an RFC 3339 instant with its exact-time
// title and no relative text, no HTML, unfiltered by any query; testdata/list.golden.json.
func TestListFeed(t *testing.T) {
	s, _ := listServer(fixtureList())
	w := get(t, s, "/list.json?q=nothing-matches&sort=pr")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	h := w.Header()
	for k, want := range map[string]string{"Content-Type": "application/json", "Content-Security-Policy": CSP, "X-Content-Type-Options": "nosniff",
		"Cache-Control": "no-store", "Referrer-Policy": "same-origin"} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	body := w.Body.String()
	for _, bad := range []string{"<", ">", "min ago", "h ago"} {
		if strings.Contains(body, bad) {
			t.Errorf("the feed holds %q", bad)
		}
	}
	var f feedReply
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Count != "3 tasks wait for you" || !f.You || f.Meta != "12 open · 3 in the merge queue" || f.ReadAt.At != "2026-11-05T14:52:10Z" || f.ReadAt.Title != "Thu 5 Nov 2026, 14:52:10 UTC" {
		t.Errorf("band = %q %v %q %+v", f.Count, f.You, f.Meta, f.ReadAt)
	}
	if len(f.Needs) != 3 || f.Needs[0].ID != "f31" || f.Needs[0].Ask == "" || f.Needs[0].Since == nil || f.Needs[0].Since.At != "2026-11-04T12:40:00Z" {
		t.Errorf("needs = %+v", f.Needs)
	}
	var ids []string
	for _, g := range f.Groups {
		ids = append(ids, g.ID)
	}
	if strings.Join(ids, " ") != "g-plan g-build g-ci g-merge-queue g-held" {
		t.Errorf("groups = %v", ids)
	}
	mq := f.Groups[3]
	if len(mq.Rows) != 3 || mq.Rows[0].ID != "d208" || !mq.Rows[0].Front || mq.Rows[0].Position != 1 || !mq.Rows[2].Held || !mq.Moving ||
		len(mq.Banners) != 1 || mq.Banners[0].Since == nil || mq.Rows[0].PR == nil || mq.Rows[0].PR.Number != 371 || mq.Rows[0].PR.CI != "run" {
		t.Errorf("merge queue = %+v", mq)
	}
	if f.Groups[4].Moving {
		t.Error("Held is moving")
	}
	if b := f.Groups[1].Rows; len(b) != 2 || b[0].ID != "d215" || b[0].Priority != 1 || b[0].Attempt != 2 || b[0].Slice != "2/3" || b[1].Name != "firstmate/f29" {
		t.Errorf("build rows = %+v", b)
	}
	if f.Finished == nil || len(f.Finished.Rows) != 2 || f.Finished.Rows[0].ID != "d223" {
		t.Errorf("finished = %+v", f.Finished)
	}
	st := f.Stats
	if st.Running != 3 || st.RunningLimit != 4 || st.MergedToday != 4 || st.Day != "2026-11-05" || st.Machine.Load == nil || *st.Machine.Load != 1.52 ||
		len(st.Fronts) != 1 || st.Fronts[0].Href != "/guildhall/d208" || st.Text.Summary != "3/4 running · 3 to merge · 4 merged · $38.20 today" {
		t.Errorf("stats = %+v", st)
	}
	checkGolden(t, "list.golden.json", body)

	head := httptest.NewRecorder()
	s.ServeHTTP(head, request(http.MethodHead, "/list.json", nil))
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "application/json" {
		t.Errorf("HEAD: status %d, type %q", head.Code, head.Header().Get("Content-Type"))
	}
}

// TestListFeedUnavailable: a vital the OS does not give is null in the feed, with "unavailable" in its text.
func TestListFeedUnavailable(t *testing.T) {
	s, src := listServer(TaskList{ReadAt: *at("2026-11-05 14:52")})
	src.stats = Stats{Machine: Vitals{Cores: 4}}
	var f struct {
		Stats struct {
			Machine map[string]any `json:"machine"`
			Text    stripView      `json:"text"`
		} `json:"stats"`
		Needs  []feedRow   `json:"needs"`
		Groups []feedGroup `json:"groups"`
		Fin    *feedGroup  `json:"finished"`
	}
	w := get(t, s, "/list.json")
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"load", "mem_used", "mem_total", "disk_free", "disk_total"} {
		if v, ok := f.Stats.Machine[k]; !ok || v != nil {
			t.Errorf("machine %s = %v, %v; want null", k, v, ok)
		}
	}
	if f.Stats.Text.Load != "load unavailable" || f.Needs == nil || f.Groups == nil || f.Fin != nil {
		t.Errorf("feed = %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"fronts":[]`) {
		t.Errorf("no empty fronts: %s", w.Body)
	}
}

// TestListFeedRefusals: a wrong Host is 421 before the Source is read; a busy store, or busy stats alone, is 503;
// any other failure 500 with no detail, logged; and a POST is 404.
func TestListFeedRefusals(t *testing.T) {
	s, src := listServer(fixtureList())
	r := request(http.MethodGet, "/list.json", nil)
	r.Host = "evil.example:80"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest || src.calls != 0 || w.Header().Get("Content-Security-Policy") != CSP {
		t.Errorf("wrong host: status %d, %d Source calls", w.Code, src.calls)
	}
	src.err = ErrBusy
	if w := get(t, s, "/list.json"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("busy: status %d", w.Code)
	}
	src.err, src.statsErr = nil, ErrBusy
	if w := get(t, s, "/list.json"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("busy stats: status %d", w.Code)
	}
	if w := get(t, s, "/"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("busy stats on the page: status %d", w.Code)
	}
	var logged []string
	s.o.Log = func(l string) { logged = append(logged, l) }
	src.statsErr = errors.New("statfs /secret/state: permission denied")
	w = get(t, s, "/list.json")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "/secret") {
		t.Errorf("failure: status %d\n%s", w.Code, w.Body)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "permission denied") {
		t.Errorf("logged %q", logged)
	}
	src.statsErr = nil
	post := httptest.NewRecorder()
	s.ServeHTTP(post, request(http.MethodPost, "/list.json", nil))
	if post.Code != http.StatusNotFound {
		t.Errorf("POST: status %d", post.Code)
	}
}

// TestListFeedEscapes: hostile ids, titles, steps, asks and sentences reach the feed as inert JSON strings, decoded
// back whole, with no raw markup in the body.
func TestListFeedEscapes(t *testing.T) {
	hostile := `<script>alert(1)</script><img src=x onerror=alert(1)>`
	s, src := listServer(TaskList{Tasks: []Listed{
		{Repository: "app", ID: "a0", Title: hostile, Group: GroupYou, Step: hostile, Ask: hostile, PR: &PullRequest{Number: 1, URL: `javascript:alert(1)`}, Where: Where{Lead: hostile, You: true}},
		{Repository: "app", ID: "a1", Title: hostile, Group: GroupMergeQueue, PR: &PullRequest{Number: 2, URL: "https://x.invalid/2"}, Queue: &QueuePlace{Position: 1, Front: true, WaitingOn: hostile}},
		{Repository: "app", ID: "a2", Title: hostile, Group: GroupHeld, Stands: hostile, Where: Where{Lead: hostile, Rest: hostile}},
	}})
	src.stats.Fronts = []TaskRef{{Repository: "app", ID: `"><script>x</script>`}}
	w := get(t, s, "/list.json")
	for _, bad := range []string{"<script", "<img", "javascript:"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Errorf("the feed holds %q raw", bad)
		}
	}
	var f feedReply
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Needs) != 1 || f.Needs[0].Title != hostile || f.Needs[0].Ask != hostile || f.Needs[0].PR == nil || f.Needs[0].PR.Href != "" {
		t.Errorf("needs = %+v", f.Needs)
	}
	if len(f.Groups) != 2 || f.Groups[1].Rows[0].Rest != hostile+" "+hostile {
		t.Errorf("held = %+v", f.Groups)
	}
	if fr := f.Stats.Fronts; len(fr) != 1 || fr[0].Href != "/app/%22%3E%3Cscript%3Ex%3C%2Fscript%3E" {
		t.Errorf("fronts = %+v", fr)
	}
}
