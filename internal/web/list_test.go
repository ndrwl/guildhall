package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixtureList is the task list as Layout A of d222's mock-up draws it, in short: three tasks waiting for the
// Guildmaster, one in grill and plan, two in build and review, one in CI, three in the merge queue (the front, one
// behind it and one held), two held, and two finished in the last 24 hours, across two repositories, read at
// 14:52:10 on 5 Nov 2026. The Source's order is not the page's, so the page's own sorting shows.
func fixtureList() TaskList {
	g := func(id, title string, grp Group, pri int, since string) Listed {
		return Listed{Repository: "guildhall", ID: id, Title: title, Group: grp, Priority: pri, Since: at(since)}
	}
	pr := func(n int) *PullRequest {
		return &PullRequest{Number: n, URL: "https://github.com/ndrwl/guildhall/pull/" + strconv.Itoa(n)}
	}
	l := TaskList{ReadAt: at("2026-11-05 14:52").Add(10 * time.Second)}
	add := func(t Listed, edit func(*Listed)) {
		if edit != nil {
			edit(&t)
		}
		l.Tasks = append(l.Tasks, t)
	}
	add(g("d219", "Retry budget per step type", GroupYou, 1, "2026-11-05 12:12"), func(t *Listed) {
		t.Step, t.Attempt, t.Ask = "plan approval", 1, "Approval of the plan."
		t.Where = Where{Lead: "Waiting for you on 1 ask:", Rest: "approval of the plan.", You: true}
	})
	add(g("d222", "Full design overhaul of the Director’s all-tasks web page", GroupYou, 2, "2026-11-05 14:40"), func(t *Listed) {
		t.Step, t.Attempt, t.Ask = "design", 1, "Your pick of the design."
	})
	add(Listed{Repository: "firstmate", ID: "f31", Title: "Captain view keeps its scroll position across refreshes", Group: GroupYou, Priority: 3, Since: at("2026-11-04 12:40"),
		Step: "build", Attempt: 3, Slice: 1, Slices: 2, PR: &PullRequest{Number: 90, URL: "https://github.com/ndrwl/firstmate/pull/90"}, CI: CIFailed, Ask: "A grant for an attempt."}, nil)
	add(g("d215", "Merge queue rows say why each rebase happened", GroupBuild, 1, "2026-11-05 13:40"), func(t *Listed) {
		t.Step, t.Attempt, t.Slice, t.Slices = "build", 2, 2, 3
	})
	add(g("d225", "Spend report groups spend by step type", GroupPlan, 3, "2026-11-05 14:27"), func(t *Listed) { t.Step, t.Attempt = "grill", 1 })
	add(Listed{Repository: "firstmate", ID: "f29", Title: "Offline mode for the logbook", Group: GroupBuild, Priority: 2, Since: at("2026-11-05 14:18"),
		Step: "review", Attempt: 1, Slice: 1, Slices: 2, PR: &PullRequest{Number: 88, URL: "https://github.com/ndrwl/firstmate/pull/88"}, CI: CIPassed}, nil)
	add(g("d211", "Clerk digest drops empty sections", GroupCI, 3, "2026-11-05 14:43"), func(t *Listed) { t.Step, t.PR, t.CI = "validation", pr(378), CIPending })
	// the merge queue, listed by priority as the Source would: the page lists it in queue order
	add(g("d204", "Director status shows parked attempts", GroupMergeQueue, 1, "2026-11-05 12:47"), func(t *Listed) {
		t.PR, t.CI, t.Queue = pr(369), CIPassed, &QueuePlace{Position: 2, WaitingOn: "queue"}
	})
	add(g("d208", "Web pages share one header partial", GroupMergeQueue, 2, "2026-11-05 14:46"), func(t *Listed) {
		t.PR, t.CI, t.Queue = pr(371), CIPending, &QueuePlace{Position: 1, Front: true, WaitingOn: "ci"}
	})
	add(g("d201", "Hallmark checks run in parallel", GroupMergeQueue, 2, "2026-11-05 10:50"), func(t *Listed) {
		t.PR, t.CI, t.Queue = pr(366), CIFailed, &QueuePlace{Position: 3, Held: true, WaitingOn: "queue"}
	})
	add(g("d217", "Merge queue rebases after CI timeouts", GroupHeld, 2, "2026-11-04 10:52"), func(t *Listed) {
		t.Where = Where{Lead: "Waiting on other tasks:", Rest: "d208 must finish first."}
	})
	add(g("d203", "Copy-level pause for the night", GroupHeld, 3, "2026-10-01 13:50"), func(t *Listed) { t.Stands = "paused" })
	add(g("d223", "An open grill round gets an Accept all recommendations button", GroupFinished, 3, "2026-11-05 13:40"), func(t *Listed) {
		t.Finished, t.PR, t.CI = "merged", pr(383), CIPassed
	})
	add(Listed{Repository: "firstmate", ID: "f26", Title: "Old export format", Group: GroupFinished, Priority: 3, Since: at("2026-11-04 18:20"), Finished: "cancelled",
		Where: Where{Lead: "Cancelled:", Rest: "superseded by f29."}}, nil)
	return l
}

func listServer(l TaskList) (*Server, *fakeSource) {
	src := &fakeSource{list: l, stats: fixtureStats()}
	return New(Options{Source: src, Location: time.UTC, Host: testHost}), src
}

// checkGolden compares body with testdata/name, which -update rewrites.
func checkGolden(t *testing.T, name, body string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(golden, []byte(body), 0o644); err != nil {
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
}

// inOrder reports the first of marks that body holds out of order or not at all.
func inOrder(t *testing.T, body string, marks ...string) {
	t.Helper()
	last := -1
	for _, m := range marks {
		i := strings.Index(body, m)
		if i < 0 {
			t.Errorf("no %s", m)
			continue
		}
		if i < last {
			t.Errorf("%s is out of order", m)
		}
		last = i
	}
}

// TestTaskList: the band's two lines; Needs you first, oldest ask first, each row with its ask; the filter form and
// the sort header; the stage groups in order, each a <details> open with its count and status line; the merge queue
// in queue order with its front banner and held mark; Held; Recently finished folded last; the row columns; the CSP,
// a GET form only and no link outside the origin but the pull requests'; and testdata/list.golden.html.
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
		`<p class="ws you"><b>3 tasks wait for you</b><span class="m">12 open · 3 in the merge queue · updated <time datetime="2026-11-05T14:52:10Z" title="Thu 5 Nov 2026, 14:52:10 UTC">now</time></span></p>`,
		`<section class="sg you" id="g-you"><div class="sgh"><h2>Needs you <span class="n you">3</span></h2><span class="sgm">oldest ask first</span></div>`,
		// a Needs you row: the repository part faint, p3 left out, step and attempt, slice, PR with its CI mark, the ask
		`<li class="r you"><span class="idc"><a class="tid" href="/firstmate/f31"><span class="rp">firstmate/</span>f31</a></span><span class="rt" title="Captain view keeps its scroll position across refreshes">Captain view keeps its scroll position across refreshes</span><span></span><span class="stp"><b>build</b> · attempt 3</span><span class="sx"><span class="lb">slice </span>1/2</span><span class="prc"><a href="https://github.com/ndrwl/firstmate/pull/90">#90</a><i class="ci bad" title="CI failed">✗</i></span><time datetime="2026-11-04T12:40:00Z" title="Wed 4 Nov 2026, 12:40:00 UTC">1 day ago</time><span class="ak">A grant for an attempt.</span></li>`,
		`<span class="pri p1">p1</span><span class="stp"><b>plan approval</b> · attempt 1</span>`,
		`<form class="fb" action="/" method="get" role="search"><input class="fq" type="search" name="q" value="" placeholder="Filter by id or title" aria-label="Filter by id or title">`,
		`<div class="fc"><span class="fl">Priority</span><a class="cp on" href="/">All</a><a class="cp" href="/?p=p1">p1<i>3</i></a><a class="cp" href="/?p=p2">p2<i>5</i></a><a class="cp" href="/?p=p3">p3<i>4</i></a></div>`,
		`<div class="fc"><span class="fl">Repository</span><a class="cp on" href="/">All<i>12</i></a><a class="cp" href="/?repo=guildhall">guildhall<i>10</i></a><a class="cp" href="/?repo=firstmate">firstmate<i>2</i></a></div>`,
		`<div class="rh"><a class="" href="/?sort=task">Task</a><span>Title</span><a class="on" href="/?dir=desc&amp;sort=pri">Pri</a><a class="" href="/?sort=stage">Stage · step</a><span>Slice</span><a class="" href="/?sort=pr">PR</a><a class="" href="/?sort=time">In state</a></div>`,
		`<div class="fc sortp"><span class="fl">Sort</span><a class="cp" href="/?sort=task">Id</a><a class="cp on" href="/?dir=desc&amp;sort=pri">Priority ▾</a>`,
		`<details class="sg mv" id="g-plan" open><summary><h2>Grill and plan <span class="n">1</span></h2><span class="sgm">1 grill</span></summary>`,
		`<details class="sg mv" id="g-build" open><summary><h2>Build and review <span class="n">2</span></h2><span class="sgm">1 build, 1 review</span></summary>`,
		`<details class="sg mv" id="g-ci" open><summary><h2>CI <span class="n">1</span></h2><span class="sgm">checks running on 1 PR</span></summary>`,
		`<span class="stp"><b>CI</b> · checks running</span>`,
		`<details class="sg mv" id="g-merge-queue" open><summary><h2>Merge queue <span class="n">3</span></h2><span class="sgm">queue order · 1 held</span></summary>`,
		`<p class="hold"><b>Front: guildhall/d208, PR #371.</b> Waiting on CI since <time datetime="2026-11-05T14:46:00Z" title="Thu 5 Nov 2026, 14:46:00 UTC">6 min ago</time>.</p>`,
		`<li class="r front"><span class="idc"><span class="qp">1</span><a class="tid" href="/guildhall/d208">`,
		`<span class="stp"><b>front</b> · waiting on CI</span>`,
		`<span class="stp">waiting · checks passed</span>`,
		`<span class="stp"><span class="hm">held</span>the Director holds its merge</span>`,
		`<details class="sg" id="g-held" open><summary><h2>Held <span class="n">2</span></h2>`,
		`<span class="stp">Waiting on other tasks: d208 must finish first.</span>`,
		`<span class="stp">Paused.</span>`, `title="Thu 1 Oct 2026, 13:50:00 UTC">1 Oct</time>`,
		`<details class="sg fin" id="g-done"><summary><h2>Recently finished <span class="n">2</span></h2><span class="sgm">last 24 hours · 1 merged, 1 cancelled</span></summary>`,
		`<span class="stp"><b>merged</b></span>`, `<span class="stp"><b>cancelled</b> · superseded by f29</span>`,
		`<p class="asof">Read from the store <time datetime="2026-11-05T14:52:10Z" title="Thu 5 Nov 2026, 14:52:10 UTC">now</time> · reload for newer</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task list lacks %s", want)
		}
	}
	// no "?" mark anywhere (d224): waiting on you is amber only
	_, page, _ := strings.Cut(body, "</head>")
	if strings.Contains(page, "? ") || strings.Contains(page, ">?<") {
		t.Error("the task list carries a ? mark")
	}
	// one GET form, the filter's; no POST, no button, no text area
	if n := strings.Count(body, "<form"); n != 1 || strings.Contains(body, `method="post"`) || strings.Contains(strings.ToLower(body), "method=post") {
		t.Errorf("task list holds %d forms or a POST one", n)
	}
	for _, bad := range []string{"<script", "<button", "<textarea"} {
		if strings.Contains(withoutOwnScripts(body), bad) {
			t.Errorf("task list holds %s", bad)
		}
	}
	if externalURL.MatchString(strings.ReplaceAll(body, `href="https://github.com/ndrwl/`, `href="/`)) {
		t.Error("task list links outside its origin, but for the pull requests")
	}
	inOrder(t, body, `id="g-you"`, `/firstmate/f31"`, `/guildhall/d219"`, `/guildhall/d222"`, `<form class="fb"`, `class="rh"`,
		`id="g-plan"`, `/guildhall/d225"`, `id="g-build"`, `/guildhall/d215"`, `/firstmate/f29"`, `id="g-ci"`, `/guildhall/d211"`,
		`id="g-merge-queue"`, `<p class="hold">`, `class="tid" href="/guildhall/d208"`, `/guildhall/d204"`, `/guildhall/d201"`,
		`id="g-held"`, `/guildhall/d217"`, `/guildhall/d203"`, `id="g-done"`, `/guildhall/d223"`, `/firstmate/f26"`, `class="asof"`)
	checkGolden(t, "list.golden.html", body)

	// HEAD reads it too
	r := request(http.MethodHead, "/", nil)
	hw := httptest.NewRecorder()
	s.ServeHTTP(hw, r)
	if hw.Code != http.StatusOK {
		t.Errorf("HEAD /: status %d", hw.Code)
	}
}

// TestTaskListOneRepository: with one repository a task is named by its id alone and there are no repository chips;
// with nothing waiting for the Guildmaster the band says so, is not amber and there is no Needs you; a held row with
// no sentence falls back to Clerk's phrase; testdata/list.one.golden.html.
func TestTaskListOneRepository(t *testing.T) {
	read := at("2026-11-05 14:52")
	s, src := listServer(TaskList{ReadAt: *read, Tasks: []Listed{
		{Repository: "app", ID: "a1", Title: "One", Group: GroupCI, Priority: 3, Step: "validation", Since: at("2026-11-05 12:40"), PR: &PullRequest{Number: 7, URL: "https://github.com/o/app/pull/7"}, CI: CIPending},
		{Repository: "app", ID: "a2", Title: "Two", Group: GroupHeld, Priority: 0, Stands: "waiting (mystery)", Since: at("2026-11-02 09:00")},
		{Repository: "app", ID: "a3", Title: "Three", Group: GroupBuild, Priority: 3, Step: "build", Attempt: 2, Since: at("2026-11-05 14:51")},
	}})
	// a quiet copy: nothing queued to merge, no config's cap, and a machine that gives only its cores
	src.stats = Stats{Running: 1, Day: *at("2026-11-05 00:00"), Spend: Spend{Attempts: 2, CostUSD: 1.5, CostMeasure: "measured"}, Machine: Vitals{Cores: 2}}
	body := get(t, s, "/").Body.String()
	for _, want := range []string{
		`<summary>1 running · 0 to merge · 0 merged · $1.50 today</summary>`, `<li>none in the merge queue</li>`, `<b>load unavailable</b> · 2 cores`,
		`<p class="ws"><b>Nothing waits for you</b><span class="m">3 open · updated <time`,
		`<a class="tid" href="/app/a1">a1</a>`, `<span class="stp">Waiting (mystery).</span>`, `<span class="pri p0">p0</span>`,
		`>3 days ago</time>`, `>2 h ago</time>`, `>1 min ago</time>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task list lacks %s", want)
		}
	}
	_, page, _ := strings.Cut(body, "</head>")
	for _, bad := range []string{`id="g-you"`, `id="g-done"`, `class="rp"`, "Repository</span>", `id="g-merge-queue"`, "in the merge queue ·"} {
		if strings.Contains(page, bad) {
			t.Errorf("task list holds %s", bad)
		}
	}
	checkGolden(t, "list.one.golden.html", body)
}

// TestTaskListEmpty: no task open and none finished says so, with no filter and no group.
func TestTaskListEmpty(t *testing.T) {
	s, _ := listServer(TaskList{ReadAt: *at("2026-11-04 14:52")})
	w := get(t, s, "/")
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != CSP {
		t.Fatalf("status %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	for _, want := range []string{`<b>Nothing waits for you</b><span class="m">No open tasks · updated`, `No task is open, and none finished in the last 24 hours.`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty list lacks %s", want)
		}
	}
	for _, bad := range []string{`class="sg`, "<form", `class="rh"`} {
		if strings.Contains(body, bad) {
			t.Errorf("empty list holds %s", bad)
		}
	}
}

// rowIDs are the task ids of body's rows in the order shown, from the group whose anchor is id to the next group.
func rowIDs(body, id string) []string {
	i := strings.Index(body, `id="`+id+`"`)
	if i < 0 {
		return nil
	}
	part := body[i:]
	if j := strings.Index(part, "</details>"); j >= 0 {
		part = part[:j]
	}
	if j := strings.Index(part, "</section>"); j >= 0 {
		part = part[:j]
	}
	var out []string
	for _, m := range regexp.MustCompile(`class="tid" href="/[^/"]+/([^"]+)"`).FindAllStringSubmatch(part, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestTaskListFilterSort: the server applies /?q=&p=&repo=&sort=&dir=, so a URL gives the same view with scripts
// off: the filter matches id or title, priority and repository; Needs you is never hidden; the merge queue keeps
// queue order under any sort; the default is priority then oldest in state; each sort and its reverse; a task with
// no value for the sort comes last; values the list does not know are dropped; nothing matching says so.
func TestTaskListFilterSort(t *testing.T) {
	l := fixtureList()
	l.Tasks = append(l.Tasks,
		Listed{Repository: "guildhall", ID: "d230", Title: "Second build", Group: GroupBuild, Priority: 1, Step: "build", Attempt: 1, Since: at("2026-11-05 10:00"), PR: &PullRequest{Number: 400}},
		Listed{Repository: "guildhall", ID: "d231", Title: "Third build", Group: GroupBuild, Priority: 1, Step: "rebase", Attempt: 1, Since: at("2026-11-05 14:00"), PR: &PullRequest{Number: 100}},
	)
	s, _ := listServer(l)
	for _, c := range []struct {
		target, group string
		want          []string
	}{
		{"/", "g-build", []string{"d230", "d215", "d231", "f29"}},
		{"/?sort=pri&dir=desc", "g-build", []string{"f29", "d230", "d215", "d231"}},
		{"/?sort=task", "g-build", []string{"f29", "d215", "d230", "d231"}},
		{"/?sort=task&dir=desc", "g-build", []string{"d231", "d230", "d215", "f29"}},
		{"/?sort=time", "g-build", []string{"d230", "d215", "d231", "f29"}},
		{"/?sort=time&dir=desc", "g-build", []string{"f29", "d231", "d215", "d230"}},
		{"/?sort=pr", "g-build", []string{"f29", "d231", "d230", "d215"}},
		{"/?sort=pr&dir=desc", "g-build", []string{"d230", "d231", "f29", "d215"}},
		{"/?sort=stage", "g-build", []string{"d230", "d215", "d231", "f29"}},
		{"/?sort=mystery&dir=sideways", "g-build", []string{"d230", "d215", "d231", "f29"}},
		{"/?sort=time&dir=desc", "g-merge-queue", []string{"d208", "d204", "d201"}},
		{"/?sort=task", "g-merge-queue", []string{"d208", "d204", "d201"}},
		{"/?q=build", "g-build", []string{"d230", "d231"}},
		{"/?q=D215", "g-build", []string{"d215"}},
		{"/?q=firstmate/", "g-build", []string{"f29"}},
		{"/?p=p2", "g-build", []string{"f29"}},
		{"/?p=p9", "g-build", []string{"d230", "d215", "d231", "f29"}},
		{"/?repo=firstmate", "g-build", []string{"f29"}},
		{"/?repo=firstmate", "g-you", []string{"f31", "d219", "d222"}},
		{"/?q=nothing-matches", "g-you", []string{"f31", "d219", "d222"}},
		{"/?repo=firstmate", "g-done", []string{"f26"}},
	} {
		body := get(t, s, c.target).Body.String()
		if got := rowIDs(body, c.group); strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: %s rows %v, want %v", c.target, c.group, got, c.want)
		}
	}

	body := get(t, s, "/?q=nothing-matches&sort=pr").Body.String()
	for _, want := range []string{`No task matches the filter. <a href="/?sort=pr">Show every task</a>`, `value="nothing-matches"`, `<input type="hidden" name="sort" value="pr">`} {
		if !strings.Contains(body, want) {
			t.Errorf("no match: lacks %s", want)
		}
	}
	for _, bad := range []string{`id="g-build"`, `id="g-done"`} {
		if strings.Contains(body, bad) {
			t.Errorf("no match: holds %s", bad)
		}
	}
	// a chip in effect is on and toggles off, keeping the rest of the view; a sort in effect reverses
	body = get(t, s, "/?p=p1&repo=guildhall&sort=time").Body.String()
	for _, want := range []string{
		`<a class="cp on" href="/?repo=guildhall&amp;sort=time">p1<i>5</i></a>`,
		`<a class="cp on" href="/?p=p1&amp;sort=time">guildhall<i>12</i></a>`,
		`<a class="on" href="/?dir=desc&amp;p=p1&amp;repo=guildhall&amp;sort=time">In state</a>`,
		`<input type="hidden" name="p" value="p1"><input type="hidden" name="repo" value="guildhall"><input type="hidden" name="sort" value="time">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("p1 guildhall by time: lacks %s", want)
		}
	}
	if want := `<a class="on desc" href="/?sort=time">In state</a>`; !strings.Contains(get(t, s, "/?sort=time&dir=desc").Body.String(), want) {
		t.Errorf("reversed sort lacks %s", want)
	}
	if want := `<a class="on desc" href="/">Pri</a>`; !strings.Contains(get(t, s, "/?dir=desc").Body.String(), want) {
		t.Errorf("reversed default lacks %s", want)
	}
}

// TestReadListQuery pins which values the list keeps and the URL it writes back.
func TestReadListQuery(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "/"},
		{"q=+d1+&p=p0&repo=app&sort=pr&dir=desc", "/?dir=desc&p=p0&q=d1&repo=app&sort=pr"},
		{"p=p6&sort=title&dir=up", "/"},
		{"p=P1&q=", "/"},
		{"repo=a%26b", "/?repo=a%26b"},
	} {
		v, _ := url.ParseQuery(c.in)
		if got := readListQuery(v).href(); got != c.want {
			t.Errorf("%q: href %q, want %q", c.in, got, c.want)
		}
	}
}

// TestTaskListTimes: every time on / is a <time> with a datetime and an exact-time title, and its text is relative.
func TestTaskListTimes(t *testing.T) {
	s, _ := listServer(fixtureList())
	body := get(t, s, "/").Body.String()
	all := regexp.MustCompile(`<time[ >]`).FindAllStringIndex(body, -1)
	full := regexp.MustCompile(`<time datetime="\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ" title="[A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4}, \d\d:\d\d:\d\d UTC">(now|\d+ min ago|\d+ h ago|1 day ago|\d+ days ago|\d{1,2} [A-Z][a-z]{2})</time>`).FindAllStringIndex(body, -1)
	// the band and the footer, 12 open rows, the front's banner and 2 finished rows
	if len(all) != 2+12+1+2 || len(full) != len(all) {
		t.Errorf("%d <time> elements, %d of them with datetime, title and relative text", len(all), len(full))
	}
	if regexp.MustCompile(`\b\d\d:\d\d\b`).MatchString(regexp.MustCompile(`<time[^>]*>|<style>[\s\S]*?</style>`).ReplaceAllString(body, "")) {
		t.Error("the task list shows a clock time outside a <time> title")
	}
}

// TestRelTime pins the relative-time steps at each boundary, a time in the future, the date past 30 days with its
// year when it is another year's, and the exact-time title in a zone that is not UTC.
func TestRelTime(t *testing.T) {
	now := time.Date(2026, 11, 5, 14, 52, 10, 0, time.UTC)
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "now"}, {59 * time.Second, "now"}, {-5 * time.Minute, "now"},
		{time.Minute, "1 min ago"}, {6*time.Minute + 59*time.Second, "6 min ago"}, {59 * time.Minute, "59 min ago"},
		{time.Hour, "1 h ago"}, {2*time.Hour + 59*time.Minute, "2 h ago"}, {23 * time.Hour, "23 h ago"}, {24*time.Hour - time.Second, "23 h ago"},
		{24 * time.Hour, "1 day ago"}, {47 * time.Hour, "1 day ago"}, {48 * time.Hour, "2 days ago"},
		{30 * 24 * time.Hour, "30 days ago"}, {31*24*time.Hour - time.Second, "30 days ago"}, {31 * 24 * time.Hour, "5 Oct"},
		{400 * 24 * time.Hour, "1 Oct 2025"},
	} {
		if got := relTime(now.Add(-c.ago), now, time.UTC); got != c.want {
			t.Errorf("%v ago: %q, want %q", c.ago, got, c.want)
		}
	}
	cet := time.FixedZone("CET", 3600)
	s := New(Options{Source: &fakeSource{}, Location: cet, Host: testHost})
	tv := s.timeAt(now.Add(-6*time.Minute), now)
	if want := (timeView{ISO: "2026-11-05T14:46:10Z", Title: "Thu 5 Nov 2026, 15:46:10 CET", Text: "6 min ago"}); tv != want {
		t.Errorf("timeAt = %+v, want %+v", tv, want)
	}
	// past 30 days the date is the server zone's: 23:30 UTC on 4 Oct is 5 Oct in CET
	if got := relTime(time.Date(2026, 10, 4, 23, 30, 0, 0, time.UTC), now, cet); got != "5 Oct" {
		t.Errorf("date in CET = %q, want 5 Oct", got)
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

// TestTaskListEscapes: hostile ids, repositories, titles, steps, asks, sentences, phrases, pull request URLs and
// filter values are escaped in every cell, chip and banner, and a pull request URL that is not http or https is not
// linked.
func TestTaskListEscapes(t *testing.T) {
	hostile := `<script>alert(1)</script><img src=x onerror=alert(1)>`
	pr := &PullRequest{Number: 1, URL: `javascript:alert(1)`}
	s, _ := listServer(TaskList{Tasks: []Listed{
		{Repository: hostile, ID: `"><script>x</script>`, Title: hostile, Group: GroupYou, Step: hostile, Ask: hostile, PR: pr, Where: Where{Lead: hostile, Rest: hostile, You: true}},
		{Repository: "app", ID: "a1", Title: hostile, Group: GroupMergeQueue, Step: hostile, PR: pr, Queue: &QueuePlace{Position: 1, Front: true, WaitingOn: hostile}},
		{Repository: "app", ID: "a2", Title: hostile, Group: GroupHeld, Stands: hostile, Where: Where{Lead: hostile, Rest: hostile}},
		{Repository: "app", ID: "a3", Title: hostile, Group: GroupBuild, Step: hostile},
		{Repository: "app", ID: "a4", Title: hostile, Group: GroupFinished, Finished: "cancelled", Where: Where{Rest: hostile}},
	}})
	for _, target := range []string{"/", "/?q=" + url.QueryEscape(hostile) + "&repo=" + url.QueryEscape(hostile), "/?q=%22%3E%3Cscript%3E"} {
		body := get(t, s, target).Body.String()
		for _, bad := range []string{"<script", "<img", "javascript:"} {
			if strings.Contains(withoutOwnScripts(body), bad) {
				t.Errorf("%s: task list holds %q unescaped", target, bad)
			}
		}
	}
	body := get(t, s, "/").Body.String()
	if !strings.Contains(body, `href="/%3Cscript%3Ealert%281%29%3C%2Fscript%3E%3Cimg%20src=x%20onerror=alert%281%29%3E/%22%3E%3Cscript%3Ex%3C%2Fscript%3E"`) {
		t.Error("a hostile id is not path-escaped in its link")
	}
}

// TestBandLines pins the band's words.
func TestBandLines(t *testing.T) {
	for _, c := range []struct {
		you  int
		want string
	}{{0, "Nothing waits for you"}, {1, "1 task waits for you"}, {3, "3 tasks wait for you"}} {
		if got := youLine(c.you); got != c.want {
			t.Errorf("youLine(%d) = %q, want %q", c.you, got, c.want)
		}
	}
	for _, c := range []struct {
		open, queued int
		want         string
	}{{0, 0, "No open tasks"}, {1, 0, "1 open"}, {25, 16, "25 open · 16 in the merge queue"}} {
		if got := metaLine(c.open, c.queued); got != c.want {
			t.Errorf("metaLine(%d, %d) = %q, want %q", c.open, c.queued, got, c.want)
		}
	}
}
