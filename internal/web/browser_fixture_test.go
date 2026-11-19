package web

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// listFixture is the address TestListBrowserFixture serves the live task list on for the browser check of
// list.js; with none the test checks the fixture itself and serves nothing.
var listFixture = flag.String("list-fixture", "", "serve the live task list fixture for the browser check on this host:port until interrupted")

// liveSource is fixtureList made live for a browser: its times are moved so that the list reads now, each read
// is stamped now so its times count up, and the /fixture/ links make it fail with ErrBusy (a 503), recover, or
// change a row. It adds a Held row at each step of TestRelTime's table, so the server's words for each step can be
// set beside the browser's. It is safe for the server's concurrent reads.
type liveSource struct {
	mu      sync.Mutex
	list    TaskList
	stats   Stats
	fail    bool
	changes int
	now     func() time.Time
}

// relSteps are the ages of the step rows, one inside each step of TestRelTime's table, and a time in the future,
// each far enough from a boundary to keep its words for the minute after the page loads.
var relSteps = []struct {
	id  string
	ago time.Duration
}{
	{"t01", 20 * time.Second}, {"t02", -5 * time.Minute}, {"t03", 90 * time.Second}, {"t04", 59*time.Minute - 20*time.Second},
	{"t05", 70 * time.Minute}, {"t06", 23*time.Hour + 20*time.Minute}, {"t07", 25 * time.Hour}, {"t08", 49 * time.Hour},
	{"t09", 30*24*time.Hour + time.Hour}, {"t10", 32 * 24 * time.Hour}, {"t11", 400 * 24 * time.Hour},
}

func newLiveSource(now func() time.Time) *liveSource {
	l := fixtureList()
	start := now()
	shift := start.Sub(l.ReadAt)
	for i := range l.Tasks {
		if s := l.Tasks[i].Since; s != nil {
			moved := s.Add(shift)
			l.Tasks[i].Since = &moved
		}
	}
	for _, s := range relSteps {
		since := start.Add(-s.ago)
		l.Tasks = append(l.Tasks, Listed{Repository: "guildhall", ID: s.id, Title: "Step row " + s.ago.String() + " ago", Group: GroupHeld, Priority: 4, Since: &since,
			Where: Where{Lead: "Held:", Rest: "a fixture row for the relative-time check."}})
	}
	st := fixtureStats()
	st.Day = start.UTC().Truncate(24 * time.Hour)
	// the charts' days end today, as the strip's
	st.Days = append([]DayTotal(nil), st.Days...)
	for i := range st.Days {
		st.Days[i].Day = st.Day.AddDate(0, 0, i-len(st.Days)+1)
	}
	return &liveSource{list: l, stats: st, now: now}
}

func (f *liveSource) Tasks() (TaskList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return TaskList{}, ErrBusy
	}
	l := f.list
	l.Tasks = append([]Listed(nil), f.list.Tasks...)
	l.ReadAt = f.now()
	return l, nil
}

func (f *liveSource) Stats() (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return Stats{}, ErrBusy
	}
	st := f.stats
	st.ReadAt = f.now()
	return st, nil
}

func (f *liveSource) Task(string, string) (Task, error) { return Task{}, ErrNotFound }

func (f *liveSource) Tail(string, string, int) (Tail, error) { return Tail{}, ErrNotFound }

// change edits d215, a Build row: its attempt goes up, it gains PR #390 with CI running, and its title counts the changes, so its row
// differs from the one on the page and list.js should redraw and tint it, and keep every other row. Today's chart day
// gains a merge, so the merges chart's last bar should grow.
func (f *liveSource) change() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes++
	if n := len(f.stats.Days); n > 0 {
		f.stats.Days = append([]DayTotal(nil), f.stats.Days...)
		f.stats.Days[n-1].Merges++
	}
	for i := range f.list.Tasks {
		if t := &f.list.Tasks[i]; t.ID == "d215" {
			t.Attempt++
			t.PR, t.CI = &PullRequest{Number: 390, URL: "https://github.com/ndrwl/guildhall/pull/390"}, CIPending
			t.Title = fmt.Sprintf("Merge queue rows say why each rebase happened (change %d)", f.changes)
		}
	}
	return f.changes
}

func (f *liveSource) setFail(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = on
}

// fixtureHandler serves the list on host from src, and the fixture's links: /fixture/fail and /fixture/ok turn the
// 503 on and off, and /fixture/change changes d215. Each answers a line of plain text.
func fixtureHandler(src *liveSource, host string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", New(Options{Source: src, Location: time.UTC, Host: host}))
	say := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintln(w, s) // a fixture line; a client that hung up needs no answer
	}
	mux.HandleFunc("/fixture/fail", func(w http.ResponseWriter, _ *http.Request) {
		src.setFail(true)
		say(w, "the list and its feed now answer 503")
	})
	mux.HandleFunc("/fixture/ok", func(w http.ResponseWriter, _ *http.Request) {
		src.setFail(false)
		say(w, "the list and its feed answer again")
	})
	mux.HandleFunc("/fixture/change", func(w http.ResponseWriter, _ *http.Request) {
		say(w, fmt.Sprintf("d215 changed (change %d)", src.change()))
	})
	return mux
}

// TestListBrowserFixture is the fixture server for the browser check of list.js (spec slice 3). Run
//
//	go test ./internal/web -run TestListBrowserFixture -list-fixture 127.0.0.1:8222 -timeout 0
//
// and open http://127.0.0.1:8222/ at 1280 px and 390 px wide, in light and dark themes, then check that:
//   - after http://127.0.0.1:8222/fixture/change, within 20 s, d215's row reads "(change 1)", attempt 3 and PR #390 and is
//     tinted, and no other row is redrawn or loses the keyboard focus;
//   - a folded group, a filter (?p=p1 or the search box) and a sort (Stage, PR) survive that refresh, and the
//     filter and sort a reload too (a reload opens the groups again, as the server draws them);
//   - t01 reads "now" and, a minute later with no reload, "1 min ago", its title unchanged;
//   - each step row t01 to t11 reads in the browser what the server wrote (curl the page, or load it with scripts
//     off): now, now, 1 min ago, 58 min ago, 1 hr ago, 23 hr ago, 1 day ago, 2 days ago, 30 days ago, then dates;
//   - after /fixture/fail the rows stay and the band says "Not refreshed since …"; /fixture/ok clears it;
//   - at 390 px the stats strip is closed and nothing scrolls sideways;
//   - the strip shows two charts of the last 14 days, merges as bars and spend as a line, and hovering over or tapping
//     a day writes its words under them, such as "<today> · 4 merges · $38", with today's when nothing is hovered;
//     after /fixture/change today's bar and words read one merge more; the charts do not zoom or pan; at 390 px they
//     stack and fit once the strip is opened; and with scripts off the charts are absent.
//
// With no -list-fixture it checks the fixture itself: its times read now, its links fail, recover and change a row,
// and the server writes each step row's words as listed above.
func TestListBrowserFixture(t *testing.T) {
	if addr := *listFixture; addr != "" {
		serveListFixture(t, addr)
		return
	}
	src := newLiveSource(time.Now)
	h := fixtureHandler(src, testHost)

	w := get(t, h, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("/: status %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "· updated <time") || !strings.Contains(body, ">now</time>") {
		t.Errorf("the band does not read now:\n%s", body)
	}
	for id, want := range map[string]string{"t01": "now", "t02": "now", "t03": "1 min ago", "t04": "58 min ago", "t05": "1 hr ago", "t06": "23 hr ago",
		"t07": "1 day ago", "t08": "2 days ago", "t09": "30 days ago"} {
		if got := rowTime(t, body, id); got != want {
			t.Errorf("%s reads %q, want %q", id, got, want)
		}
	}
	for _, id := range []string{"t10", "t11"} {
		if got := rowTime(t, body, id); strings.Contains(got, "ago") || got == "now" {
			t.Errorf("%s reads %q, want a date", id, got)
		}
	}

	get(t, h, "/fixture/change")
	var f feedReply
	if err := json.Unmarshal(get(t, h, "/list.json").Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	var d215 *feedRow
	for _, g := range f.Groups {
		for i := range g.Rows {
			if g.Rows[i].ID == "d215" {
				d215 = &g.Rows[i]
			}
		}
	}
	if d215 == nil || d215.Attempt != 3 || !strings.HasSuffix(d215.Title, "(change 1)") || d215.PR == nil || d215.PR.Number != 390 {
		t.Errorf("d215 after a change = %+v", d215)
	}
	days := f.Stats.Text.Days
	if len(days) != ChartDays || days[ChartDays-1].Day != time.Now().UTC().Format("2006-01-02") || days[ChartDays-1].Merges != 5 {
		t.Errorf("the charts' days after a change = %+v", days)
	}

	get(t, h, "/fixture/fail")
	if c := get(t, h, "/list.json").Code; c != http.StatusServiceUnavailable {
		t.Errorf("/list.json while failing: status %d, want 503", c)
	}
	get(t, h, "/fixture/ok")
	if c := get(t, h, "/list.json").Code; c != http.StatusOK {
		t.Errorf("/list.json after ok: status %d, want 200", c)
	}
}

// rowTime is the text of the <time> in body's row for the task id.
func rowTime(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `data-sort-id="`+id+`"`)
	if i < 0 {
		t.Fatalf("no row %s", id)
		return ""
	}
	row := body[i:]
	if j := strings.Index(row, "</li>"); j >= 0 {
		row = row[:j]
	}
	j := strings.Index(row, "<time")
	if j < 0 {
		t.Fatalf("row %s has no time", id)
	}
	row = row[j:]
	k := strings.Index(row, ">")
	e := strings.Index(row, "</time>")
	return row[k+1 : e]
}

// serveListFixture serves the live fixture on addr until the test is interrupted.
func serveListFixture(t *testing.T, addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	host := ln.Addr().String()
	srv := &http.Server{Handler: fixtureHandler(newLiveSource(time.Now), host), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	t.Logf("serving the task list fixture on http://%s/ (links /fixture/change, /fixture/fail, /fixture/ok); interrupt to stop", host)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
