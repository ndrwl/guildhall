package web

import (
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden task page, testdata/task.golden.html")

// testHost is the Host the page tests' servers are visited by, and that get and request send.
const testHost = "example.com:80"

// request is a test request for target sent to testHost.
func request(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = testHost
	return r
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, target, nil))
	return w
}

// externalURL matches a URL in an attribute that leaves the page's origin; the pull request links are the only
// ones a page carries, and they are plain links, which the CSP does not fetch.
var externalURL = regexp.MustCompile(`(?:src|href|action)="(?:https?:)?//`)

// TestTaskPageFixture renders design-3's mock-up content and checks every section, the anchors, the step and ask
// states, the slices, the void node, the Director-run grill, and the page's safety: a viewport, no script, no form,
// no external fetch and the CSP header.
func TestTaskPageFixture(t *testing.T) {
	s, _, _, _ := fixtureServer(t)
	w := get(t, s, "/guildhall/d122")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != CSP {
		t.Fatalf("CSP = %q, want %q", got, CSP)
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		// the band: crumbs, title, chips, slice and the stepper's states
		`<b>d122</b>`, `linked from <a href="/guildhall/d105">d105</a>`, `depends on <a href="/other/d7">other/d7</a>`,
		`<h1>Guildmaster task page: one web page per task with context, status and answerable decisions</h1>`,
		`<li class="c-you">? Review · round 2 · waiting on you</li>`, `PR #251</a> open`, `CI <span class="c-ok">✓ passed</span>`,
		`<span class="sliceof">slice 2 of 2</span>`,
		`<li class="ok"><b>Grill</b>3 rounds</li>`, `<li class="you"><b>Review</b>waiting on you</li>`, `<li class="later"><b>Deliver</b>later</li>`,
		// status
		`<span class="tag you">? Waiting on you</span> · 2 asks`, `<b>Review · round 2</b> · slice 2 of 2`, `review--s2-2 · succeeded 4 Nov 14:20`,
		`<a href="https://github.com/ndrwl/guildhall/pull/251">#251</a> · open`, `<span class="tag ok">✓ passed</span>`,
		`1. Serve read-only task and reader pages on the tailnet · merged · <a href="https://github.com/ndrwl/guildhall/pull/247">#247</a>`,
		`<li class="cur">2. Answer decisions on the task page · current`,
		`<li>checks of PR #251 are pending</li>`, `Read from the store at 14:52 · reload for newer`,
		// needs you: open asks only, the deferred merge is not in the index
		`Needs you <span class="n you">2</span>`, `<a href="#review--s2-x2"><b>Loop exit</b> 3f9c2ab<span>#review--s2-x2</span></a>`,
		`<a href="#grant-1"><b>Grant</b> approve, reject<span>#grant-1</span></a>`,
		// the task in your words, and the newest grill's decisions
		`“Perhaps its better if we move things like grills and decisions for me into a web interface”<cite>31 Oct 09:12 · task context · recorded by director</cite>`,
		`<cite>31 Oct 09:12 · task context</cite>`,
		`What the grill decided · 3 decisions`, `<strong>Rendering:</strong> goldmark with <code>tables</code>.`, `Read grill-3-notes`,
		// documents grouped, newest first, older dimmer, linked documents under their task
		`<li><span class="k">Review</span><a href="/guildhall/d122/doc/review--s2-2-outcome">review--s2-2-outcome</a><span class="d">4 Nov</span></li>`,
		`<li class="old"><span class="k"></span><a href="/guildhall/d122/doc/review-1-outcome">review-1-outcome</a>`,
		`<li><span class="k">Grill</span><a href="/guildhall/d122/doc/grill-3-notes">grill-3-notes</a>`,
		`<li><span class="k">d105</span><a href="/guildhall/d105/doc/design-3-design">design-3-design</a>`,
		`Documents <span class="n">8</span>`,
		// now: the asks in full, read-only, with anchors, meanings and tones
		`<span class="st">waiting on you · 2 asks</span><time>since 4 Nov 14:20</time>`,
		`? The Director runs grill with you in its session; answer it there.`,
		`<div class="ask" id="review--s2-x2">`, `<span class="since">open since 4 Nov 14:20</span>`,
		`<p class="q">Review round 2 still asks for changes. Continue, accept or reject?</p>`, `About <code>3f9c2ab</code>`,
		`<summary>The rest of the question</summary>`, `<summary>The open findings · 1</summary>`, `<span class="sev">medium</span><span><b>F4</b>`,
		`<button class="opt carry" type="button" disabled><b>Continue</b><span>another round of the loop</span></button>`,
		`<button class="opt pos" type="button" disabled><b>Accept</b>`, `<button class="opt neg" type="button" disabled><b>Reject</b>`,
		`<div class="ask" id="grant-1">`, `<b>Approve</b><span>the grant takes effect for its attempt</span>`,
		`<div class="ask def" id="deliver-1">`, `<span class="tag you">deferred</span>`,
		// an option the table does not describe shows its word alone, neutral
		`<button class="opt" type="button" disabled><b>Later</b></button>`,
		`Answer in chat or with <code>guildhall decide answer</code> for now; answering on this page comes in the next slice.`,
		// the story: the key, newest first, states, findings fixed, the void acceptance, slice merge, the fold
		`<li><span class="dot back">↩&#xFE0E;</span>sent back</li>`, `<li><span class="dot later"></span>later</li>`,
		`<li class="node later"><div class="nh"><span class="t">Later</span><span class="st">not started</span></div><div class="nb">Publish · Deliver</div></li>`,
		`<li class="node you"><div class="nh"><span class="t">Grant · grant-1</span><span class="st">waiting on you</span><time>4 Nov 14:31</time></div><div class="links"><a class="flag" href="#grant-1">Answer #grant-1 above ↑</a></div></li>`,
		`<span class="t">Review · round 2 · slice 2</span><span class="st">changes requested · waiting on you</span><time>4 Nov 13:58–14:20</time>`,
		`<li class="node ok"><div class="nh"><span class="t">Slice 1 merged</span>`,
		`<li class="node back"><div class="nh"><span class="t">Review · round 1</span><span class="st">changes requested · sent back to build</span>`,
		`<span class="fx">fixed</span>`,
		`<details class="older"><summary>5 earlier entries · from 31 Oct</summary>`,
		`<li class="node void"><div class="nh"><span class="t">Approval · accept-1</span><span class="st">void</span>`, `void: superseded: the spec changed`,
		`<div class="said">“approve all”<small>answered via the Director or decide answer</small></div>`,
		`<a class="att" href="/guildhall/d122/doc/plan-1-spec">plan-1-spec</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task page lacks %s", want)
		}
	}
	for _, bad := range []string{"<script", "<form", "<iframe", "<textarea", "Send words only", "Answering as"} {
		if strings.Contains(body, bad) {
			t.Errorf("task page holds %q; slice 1 pages are read-only and script-free", bad)
		}
	}
	if m := externalURL.FindAllString(body, -1); len(m) != 4 {
		t.Errorf("external links = %q, want only the four pull request links (chip, status, two slices)", m)
	}
	// The story shows the hollow later node and the six newest nodes before the fold, newest first.
	story := body[strings.Index(body, `class="c2-story"`):]
	shown := story[:strings.Index(story, `class="older"`)]
	if n := strings.Count(shown, `<li class="node`); n != storyShown+1 {
		t.Errorf("%d nodes before the fold, want the later node and %d", n, storyShown)
	}
	if strings.Index(shown, "Grant · grant-1") > strings.Index(shown, "Slice 1 merged") {
		t.Error("the story is not newest first")
	}
	if strings.Index(body, `id="grant-1"`) > strings.Index(body, `id="deliver-1"`) {
		t.Error("the deferred ask is not after the open ones")
	}

	golden := filepath.Join("testdata", "task.golden.html")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/web -run TestTaskPageFixture -update", err)
	}
	if got := strings.ReplaceAll(body, "\r\n", "\n"); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("the task page differs from %s; review it and run with -update", golden)
	}
}

// TestTaskPageEscapes: hostile titles, questions, words, findings, document names and story text are escaped.
func TestTaskPageEscapes(t *testing.T) {
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	hostile := `<script>alert(1)</script><img src=x onerror=alert(1)>`
	task.Title = hostile
	task.Asks[0].Question = hostile + "\n" + hostile
	task.Asks[0].Options = []string{hostile}
	task.Asks[0].Findings[0].Summary = hostile
	task.Context[0].Text = hostile
	task.DirectorWaits = []string{hostile}
	task.Story[0].Title, task.Story[0].Body = hostile, hostile
	task.Story[5].Said[0].Text = hostile
	task.Documents[3].Name = `"><script>alert(1)</script>`
	task.Status.PR.URL = "javascript:alert(1)"
	task.Slices.Items[1].PR.URL = "javascript:alert(2)"
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	body := get(t, s, "/guildhall/d122").Body.String()
	for _, bad := range []string{"<script", "<img", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("page holds %q unescaped", bad)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the hostile title is not shown escaped")
	}
}

// TestTaskPageEmpty: a task with nothing recorded yet renders every section with its empty line.
func TestTaskPageEmpty(t *testing.T) {
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"r/t1": {Repository: "r", ID: "t1", Title: "A task", State: "open", Stands: "not started"}}}, Location: time.UTC, Host: testHost})
	w := get(t, s, "/r/t1")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	for _, want := range []string{"Nothing needs you on this task.", "No words of yours are recorded on this task.", "No documents yet.",
		`<span class="st">nothing waits on you</span>`, `<p class="empty">not started.</p>`, "<dt>PR</dt><dd>none</dd>", "no checks recorded"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty page lacks %s", want)
		}
	}
}

// TestRoutes: unknown paths, other methods, a missing task, a busy store and a failing source each give their
// refusal page with no detail.
func TestRoutes(t *testing.T) {
	s, src, _, _ := fixtureServer(t)
	for target, code := range map[string]int{
		"/": 404, "/guildhall": 404, "/guildhall/d122/": 404, "/guildhall/d122/extra": 404, "/guildhall/d122/doc": 404,
		"/guildhall/d122/doc/plan-1-spec/other": 404, "/guildhall/nope": 404, "/guildhall/%2e%2e": 404, "/a%2Fb/c": 404, "/guildhall/..": 404,
	} {
		w := get(t, s, target)
		if w.Code != code {
			t.Errorf("%s: status %d, want %d", target, w.Code, code)
		}
		if w.Header().Get("Content-Security-Policy") != CSP {
			t.Errorf("%s: no CSP on the refusal", target)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(http.MethodPost, "/guildhall/d122", strings.NewReader("x=1")))
	if w.Code != http.StatusNotFound {
		t.Errorf("POST: status %d, want 404; answering comes in the next slice", w.Code)
	}
	src.err = ErrBusy
	if w := get(t, s, "/guildhall/d122"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "The store is busy") {
		t.Errorf("busy: status %d\n%s", w.Code, w.Body)
	}
	var logged []string
	s.o.Log = func(l string) { logged = append(logged, l) }
	src.err = errors.New("open /secret/state/director.store: permission denied")
	w = get(t, s, "/guildhall/d122")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "/secret") {
		t.Errorf("failing source: status %d, body leaks the error:\n%s", w.Code, w.Body)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "/secret") {
		t.Errorf("logged %q, want the error", logged)
	}
}

// TestDocuments: a document is served only on a sha256 match; a mismatch, a missing file and an unknown name each
// give a refusal page naming no path; Raw is text/plain; only an HTML document has a frame, served sandboxed; a
// document of another task resolves only on that task's own route.
func TestDocuments(t *testing.T) {
	s, _, dir, _ := fixtureServer(t)
	w := get(t, s, "/guildhall/d122/doc/plan-1-spec")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("reader page: status %d\n%s", w.Code, body)
	}
	for _, want := range []string{`<a class="backto" href="/guildhall/d122">← Back to the task</a>`, `<p class="dname">plan-1-spec</p>`,
		`<span>Spec · plan · 1</span>`, `recorded 31 Oct 16:10`, `✓ sha256 matches the record`, `<a href="/guildhall/d122/doc/plan-1-spec/raw">Raw</a>`,
		`<a class="jn" href="#needs">? Needs you · 2 · below ↓</a>`, `<article class="md"><h1>The spec</h1>`, `<table>`, `<td>1</td>`,
		`<div class="ask" id="review--s2-x2">`, `<section id="needs">`, `<div class="endback">`} {
		if !strings.Contains(body, want) {
			t.Errorf("reader page lacks %s", want)
		}
	}
	for _, bad := range []string{"<script", "javascript:", dir} {
		if strings.Contains(body, bad) {
			t.Errorf("reader page holds %q", bad)
		}
	}
	raw := get(t, s, "/guildhall/d122/doc/plan-1-spec/raw")
	if raw.Code != 200 || raw.Header().Get("Content-Type") != "text/plain; charset=utf-8" || raw.Body.String() != fixtureFiles["tasks/guildhall/d122/plan-1/spec.md"] {
		t.Errorf("raw: %d %q %q", raw.Code, raw.Header().Get("Content-Type"), raw.Body)
	}
	if w := get(t, s, "/guildhall/d122/doc/plan-1-spec/frame"); w.Code != http.StatusNotFound {
		t.Errorf("frame of a Markdown document: status %d, want 404", w.Code)
	}
	html := get(t, s, "/guildhall/d122/doc/design-1-design")
	if !strings.Contains(html.Body.String(), `<iframe sandbox src="/guildhall/d122/doc/design-1-design/frame"`) || strings.Contains(html.Body.String(), "a design") {
		t.Errorf("an HTML document is not shown only in its sandboxed frame:\n%s", html.Body)
	}
	frame := get(t, s, "/guildhall/d122/doc/design-1-design/frame")
	if frame.Code != 200 || frame.Header().Get("Content-Security-Policy") != FrameCSP || frame.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("frame: %d, CSP %q, type %q", frame.Code, frame.Header().Get("Content-Security-Policy"), frame.Header().Get("Content-Type"))
	}
	json := get(t, s, "/guildhall/d122/doc/review-1-outcome")
	if !strings.Contains(json.Body.String(), `<pre class="txt">{&#34;verdict&#34;:&#34;changes_requested&#34;}</pre>`) {
		t.Errorf("a JSON document is not shown as escaped text:\n%s", json.Body)
	}
	// A linked document of another task does not resolve on this task's route.
	if w := get(t, s, "/guildhall/d122/doc/design-3-design"); w.Code != http.StatusNotFound {
		t.Errorf("another task's document on this task's route: %d, want 404", w.Code)
	}

	refused := func(target string, code int, words string) {
		t.Helper()
		w := get(t, s, target)
		if w.Code != code || !strings.Contains(w.Body.String(), words) {
			t.Errorf("%s: %d, want %d with %q:\n%s", target, w.Code, code, words, w.Body)
		}
		for _, bad := range []string{dir, "tasks/guildhall", "state/"} {
			if strings.Contains(w.Body.String(), bad) {
				t.Errorf("%s: refusal names the path %q", target, bad)
			}
		}
	}
	refused("/guildhall/d122/doc/nope", 404, "Task guildhall/d122 has no document nope.")
	if err := os.WriteFile(filepath.Join(dir, "tasks", "guildhall", "d122", "plan-1", "spec.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused("/guildhall/d122/doc/plan-1-spec", 409, "does not match the sha256 recorded for it")
	refused("/guildhall/d122/doc/plan-1-spec/raw", 409, "does not match the sha256 recorded for it")
	if err := os.Remove(filepath.Join(dir, "tasks", "guildhall", "d122", "plan-1", "spec.md")); err != nil {
		t.Fatal(err)
	}
	refused("/guildhall/d122/doc/plan-1-spec", 404, "The file of plan-1-spec is missing")
}

// TestDocumentPathFromRecordOnly: a record whose path climbs out of the document directory is refused, so no path is
// read outside it.
func TestDocumentPathFromRecordOnly(t *testing.T) {
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	task.Documents = append(task.Documents, Document{Repository: "guildhall", TaskID: "d122", Name: "evil", Path: "../outside.md", SHA256: sums["tasks/guildhall/d122/grill-1/notes.md"]})
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Host: testHost})
	if w := get(t, s, "/guildhall/d122/doc/evil"); w.Code != http.StatusConflict {
		t.Errorf("a climbing path: %d, want 409", w.Code)
	}
}

// TestGrillFold: with no "## Decisions" section the fold is a link to the grill document.
func TestGrillFold(t *testing.T) {
	if _, _, ok := grillDecisions([]byte("# notes\n\n## Facts\n- x\n")); ok {
		t.Error("a document without ## Decisions has a section")
	}
	section, n, ok := grillDecisions([]byte("## Decisions\r\n- a\r\n  - nested\r\n* b\r\n```\n## not a heading\n- not an item\n```\n## Facts\n- c\n"))
	if !ok || n != 2 || strings.Contains(section, "c") {
		t.Errorf("section %q, %d items, %v", section, n, ok)
	}
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	p := "tasks/guildhall/d122/grill-3/notes.md"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte("# no decisions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task.Documents[2].SHA256 = "2c0c5e4a3e1cd2f3e2fb2b0a0c0d4f1ad5a3b1c9e5b2a8b5f7c9d0e1f2a3b4c5"
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Host: testHost})
	body := get(t, s, "/guildhall/d122").Body.String()
	if !strings.Contains(body, `<a class="flink" href="/guildhall/d122/doc/grill-3-notes">What the grill decided · read grill-3-notes</a>`) {
		t.Error("the grill fold is not a link when the document cannot give its decisions")
	}
}
