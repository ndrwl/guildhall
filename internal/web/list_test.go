package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureList is the task list as design-1's frame draws it: three tasks waiting for the Guildmaster, three moving,
// two waiting on something else, one of another repository, and two finished this week.
func fixtureList() TaskList {
	you := func(id, title, lead, rest, since string) Listed {
		return Listed{Repository: "guildhall", ID: id, Title: title, Group: GroupYou, Where: Where{Lead: lead, Rest: rest, You: true}, Since: at(since)}
	}
	row := func(repo, id, title string, g Group, lead, rest, since string) Listed {
		return Listed{Repository: repo, ID: id, Title: title, Group: g, Where: Where{Lead: lead, Rest: rest}, Since: at(since)}
	}
	return TaskList{ReadAt: *at("2026-11-04 14:52"), Tasks: []Listed{
		you("d150", "Task page shows the human-readable version", "Waiting for you on 2 asks:", "whether to go on after the slice 2 review, and a grant for an attempt.", "2026-11-04 14:20"),
		row("guildhall", "d147", "Design stage renders real-page mock-ups", GroupMoving, "Moving:", "the slice 2 build is running (attempt 1).", "2026-11-04 13:05"),
		you("d153", "One ask card", "Waiting for you in the Director's session:", "the grill runs there with you.", "2026-11-04 11:00"),
		row("guildhall", "d145", "Talk with the Director on the task page", GroupOther, "Waiting on other tasks:", "d150 must finish first.", "2026-11-03 09:30"),
		row("guildhall", "d140", "Merge queue rebases", GroupMoving, "Moving:", "CI is running on PR #260.", "2026-11-04 12:40"),
		you("d139", "Hallmark follow-ups", "Waiting for you, later:", "1 deferred ask, held until other tasks merge.", "2026-11-02 16:00"),
		row("guildhall", "d138", "Paused work", GroupOther, "Paused:", "nothing runs until the Director resumes it.", "2026-11-01 10:00"),
		row("firstmate", "f12", "Captain view", GroupMoving, "Waiting for a free slot:", "the build is queued 2.", "2026-11-04 14:00"),
		{Repository: "guildhall", ID: "d136", Title: "Shipped last week", Group: GroupFinished, Where: Where{Lead: "Done:", Rest: "the work is merged."}, Since: at("2026-11-03 18:00")},
		{Repository: "guildhall", ID: "d135", Title: "Not needed", Group: GroupFinished, Where: Where{Lead: "Cancelled:", Rest: "superseded by d150."}, Since: at("2026-11-02 08:00")},
	}}
}

func listServer(l TaskList) (*Server, *fakeSource) {
	src := &fakeSource{list: l}
	return New(Options{Source: src, Location: time.UTC, Host: testHost}), src
}

// TestTaskList: the list groups every task by who it waits on, each row linked to its task page with its
// where-it-stands sentence and since when, the finished tasks folded last, under the count line; it has no form and
// no script and carries the CSP, and matches testdata/list.golden.html.
func TestTaskList(t *testing.T) {
	s, _ := listServer(fixtureList())
	w := get(t, s, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != CSP {
		t.Fatalf("CSP = %q, want %q", got, CSP)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<meta name="viewport" content="width=device-width, initial-scale=1">`, `<h1>All tasks</h1>`,
		`<p class="ws you"><b>? 3 tasks wait for you · 3 are moving · 2 wait on something else</b></p>`,
		`<section class="tg you"><h2>Waiting for you <span class="n you">3</span></h2>`,
		`<section class="tg moving"><h2>Moving <span class="n">3</span></h2>`,
		`<section class="tg other"><h2>Waiting on something else <span class="n">2</span></h2>`,
		// a task of more than one repository is named with its repository
		`<li class="row you"><a class="tid" href="/guildhall/d150">guildhall/d150</a><div class="tb"><p class="tt">Task page shows the human-readable version</p><p class="tw"><b>Waiting for you on 2 asks:</b> whether to go on after the slice 2 review, and a grant for an attempt.</p></div><time>since 4 Nov 14:20</time></li>`,
		`<a class="tid" href="/firstmate/f12">firstmate/f12</a>`,
		`<summary>Finished in the last 7 days · 2</summary>`, `<b>Done:</b> the work is merged.`, `<time>closed 3 Nov 18:00</time>`,
		`Read from the store at 14:52 · reload for newer`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task list lacks %s", want)
		}
	}
	for _, bad := range []string{"<form", "<script", "<button", "<textarea"} {
		if strings.Contains(body, bad) {
			t.Errorf("task list holds %s", bad)
		}
	}
	if externalURL.MatchString(body) {
		t.Error("task list links outside its origin")
	}
	// each group in order, rows in the Source's order, and the finished fold last
	order := []string{`class="tg you"`, `/guildhall/d150"`, `/guildhall/d153"`, `/guildhall/d139"`, `class="tg moving"`, `/guildhall/d147"`, `/guildhall/d140"`, `/firstmate/f12"`,
		`class="tg other"`, `/guildhall/d145"`, `/guildhall/d138"`, `Finished in the last 7 days`, `/guildhall/d136"`, `/guildhall/d135"`}
	last := -1
	for _, o := range order {
		i := strings.Index(body, o)
		if i < last {
			t.Errorf("%s is out of order", o)
		}
		last = i
	}

	golden := filepath.Join("testdata", "list.golden.html")
	if *update {
		if err := os.WriteFile(golden, w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/web -run TestTaskList -update", err)
	}
	if got := strings.ReplaceAll(body, "\r\n", "\n"); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("the task list differs from %s; review it and run with -update", golden)
	}

	// HEAD reads it too
	r := request(http.MethodHead, "/", nil)
	hw := httptest.NewRecorder()
	s.ServeHTTP(hw, r)
	if hw.Code != http.StatusOK {
		t.Errorf("HEAD /: status %d", hw.Code)
	}
}

// TestTaskListOneRepository: with one repository a task is named by its id alone; with nothing waiting for the
// Guildmaster the count line says so and is not amber; a row with no sentence falls back to Clerk's phrase.
func TestTaskListOneRepository(t *testing.T) {
	s, _ := listServer(TaskList{ReadAt: *at("2026-11-04 14:52"), Tasks: []Listed{
		{Repository: "app", ID: "a1", Title: "One", Group: GroupMoving, Where: Where{Lead: "Moving:", Rest: "CI is running."}},
		{Repository: "app", ID: "a2", Title: "Two", Group: GroupOther, Stands: "waiting (mystery)"},
	}})
	body := get(t, s, "/").Body.String()
	for _, want := range []string{
		`<p class="ws"><b>Nothing waits for you · 1 task is moving · 1 waits on something else</b></p>`,
		`<a class="tid" href="/app/a1">a1</a>`, `<b>Waiting (mystery).</b>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task list lacks %s", want)
		}
	}
	for _, bad := range []string{`class="tg you"`, "Finished in the last 7 days", `<time>`} {
		if strings.Contains(body, bad) {
			t.Errorf("task list holds %s", bad)
		}
	}
}

// TestTaskListEmpty: no task open and none finished says so.
func TestTaskListEmpty(t *testing.T) {
	s, _ := listServer(TaskList{ReadAt: *at("2026-11-04 14:52")})
	w := get(t, s, "/")
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != CSP {
		t.Fatalf("status %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	for _, want := range []string{`<b>No open tasks.</b>`, `No task is open, and none finished in the last 7 days.`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty list lacks %s", want)
		}
	}
	if strings.Contains(body, `class="tg`) {
		t.Error("empty list shows a group")
	}
}

// TestTaskListRefusals: a busy store is 503 and any other failure a 500 with no detail, its error logged.
func TestTaskListRefusals(t *testing.T) {
	s, src := listServer(fixtureList())
	src.err = ErrBusy
	if w := get(t, s, "/"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "The store is busy") {
		t.Errorf("busy: status %d\n%s", w.Code, w.Body)
	}
	var logged []string
	s.o.Log = func(l string) { logged = append(logged, l) }
	src.err = errors.New("open /secret/state/director.store: permission denied")
	w := get(t, s, "/")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "/secret") {
		t.Errorf("failure: status %d\n%s", w.Code, w.Body)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "permission denied") {
		t.Errorf("logged %q", logged)
	}
}

// TestTaskListEscapes: hostile titles, sentences and ids are escaped.
func TestTaskListEscapes(t *testing.T) {
	hostile := `<script>alert(1)</script><img src=x onerror=alert(1)>`
	s, _ := listServer(TaskList{Tasks: []Listed{{Repository: "app", ID: `"><script>x</script>`, Title: hostile, Group: GroupYou, Where: Where{Lead: hostile, Rest: hostile, You: true}}}})
	body := get(t, s, "/").Body.String()
	for _, bad := range []string{"<script", "<img"} {
		if strings.Contains(body, bad) {
			t.Errorf("task list holds %q unescaped", bad)
		}
	}
	if !strings.Contains(body, `href="/app/%22%3E%3Cscript%3Ex%3C%2Fscript%3E"`) {
		t.Error("a hostile id is not path-escaped in its link")
	}
}

// TestCountLine pins the count line's words.
func TestCountLine(t *testing.T) {
	for _, c := range []struct {
		you, moving, other int
		want               string
	}{
		{0, 0, 0, "No open tasks."},
		{1, 0, 0, "1 task waits for you"},
		{3, 5, 3, "3 tasks wait for you · 5 are moving · 3 wait on something else"},
		{0, 1, 0, "Nothing waits for you · 1 task is moving"},
		{0, 0, 2, "Nothing waits for you · 2 tasks wait on something else"},
		{2, 0, 1, "2 tasks wait for you · 1 waits on something else"},
	} {
		if got := countLine(c.you, c.moving, c.other); got != c.want {
			t.Errorf("countLine(%d, %d, %d) = %q, want %q", c.you, c.moving, c.other, got, c.want)
		}
	}
}
