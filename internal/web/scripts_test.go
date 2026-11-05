package web

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// withoutOwnScripts is body with exactly the own <script> elements removed, as the pages inline them, so a check
// that body holds no "<script" still proves untrusted content gained none: any script left is not an own one.
func withoutOwnScripts(body string) string {
	for _, s := range ownScripts {
		body = strings.ReplaceAll(body, "<script>"+string(s.Body)+"</script>", "")
	}
	return body
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func isOwnScript(body string) bool {
	for _, s := range ownScripts {
		if string(s.Body) == body {
			return true
		}
	}
	return false
}

// TestWithoutOwnScripts: the helper removes the own scripts and nothing else, so a script that is not an own one,
// even one that repeats an own script's text with an attribute or a changed byte, is still reported.
func TestWithoutOwnScripts(t *testing.T) {
	s, _, _, _ := fixtureServer(t)
	body := get(t, s, "/guildhall/d122").Body.String()
	if !strings.Contains(body, "<script") {
		t.Fatal("the task page carries no own script")
	}
	if strings.Contains(withoutOwnScripts(body), "<script") {
		t.Fatal("the task page holds a script that is not an own one")
	}
	own := string(ownScripts[0].Body)
	for _, foreign := range []string{"<script>alert(1)</script>", `<script src="/x.js"></script>`,
		"<script nonce=x>" + own + "</script>", "<script>" + own + ";alert(1)</script>", "<SCRIPT>alert(1)</SCRIPT>"} {
		injected := strings.Replace(body, "</head>", foreign+"</head>", 1)
		if !strings.Contains(strings.ToLower(withoutOwnScripts(injected)), "<script") {
			t.Errorf("the helper hides the foreign script %s", foreign)
		}
	}
}

var scriptElement = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// checkOwnScripts fails t unless every <script> element of w's page has no attributes, holds exactly an own
// script's bytes and has its sha256 listed in w's CSP, and the page carries each own script once.
func checkOwnScripts(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if len(ownScripts) == 0 {
		t.Fatal("no own scripts are embedded")
	}
	body, csp := w.Body.String(), w.Header().Get("Content-Security-Policy")
	found := scriptElement.FindAllStringSubmatch(body, -1)
	if n := strings.Count(strings.ToLower(body), "<script"); n != len(found) || n != len(ownScripts) {
		t.Errorf("%s: %d <script, %d whole elements, %d own scripts", name, n, len(found), len(ownScripts))
	}
	for _, m := range found {
		if m[1] != "" {
			t.Errorf("%s: a script carries attributes %q", name, m[1])
		}
		if !isOwnScript(m[2]) {
			t.Errorf("%s: a script is not an own one: %q", name, m[2])
		}
		if hash := hashOf(m[2]); !strings.Contains(csp, hash) {
			t.Errorf("%s: the CSP %q does not list the script's hash %s", name, csp, hash)
		}
	}
}

// TestOwnScriptsPinned: on the task list, a task page and a reader page every script is pinned (the answering
// task page is checked by TestAnsweringPage); the refusal page carries none and the mock-up frame keeps FrameCSP.
func TestOwnScriptsPinned(t *testing.T) {
	s, _, _, _ := fixtureServer(t)
	l, _ := listServer(TaskList{Tasks: []Listed{{Repository: "app", ID: "d1", Title: "T", Group: GroupYou}}})
	for _, c := range []struct {
		name   string
		server http.Handler
		target string
	}{{"list", l, "/"}, {"task", s, "/guildhall/d122"}, {"reader", s, "/guildhall/d122/doc/plan-1-spec"}} {
		w := get(t, c.server, c.target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.name, w.Code)
		}
		checkOwnScripts(t, c.name, w)
	}
	refusal := get(t, s, "/guildhall/nope")
	if refusal.Code == http.StatusOK || strings.Contains(refusal.Body.String(), "<script") {
		t.Errorf("the refusal page (status %d) carries a script", refusal.Code)
	}
	frame := get(t, s, "/guildhall/d122/doc/designer-4-design-2/frame")
	if frame.Code != http.StatusOK || frame.Header().Get("Content-Security-Policy") != FrameCSP {
		t.Errorf("the frame (status %d) is not served under FrameCSP", frame.Code)
	}
}

// TestCSPAllowsOnlyOwnScripts: the CSP is today's policy plus a script-src of exactly the own scripts' hashes, with
// no keyword or host that would let any other script run, and one connect-src of the page's own origin only, for
// poll.js; a policy of no scripts is today's string exactly, and the frame's FrameCSP is unchanged.
func TestCSPAllowsOnlyOwnScripts(t *testing.T) {
	if policy(nil) != baseCSP {
		t.Errorf("a policy of no scripts is %q, want %q", policy(nil), baseCSP)
	}
	if !strings.HasPrefix(CSP, baseCSP+"; ") || !strings.Contains(CSP, "default-src 'none'") {
		t.Errorf("CSP %q does not keep today's policy", CSP)
	}
	if !strings.HasSuffix(CSP, "; connect-src 'self'") || strings.Count(CSP, "connect-src") != 1 {
		t.Errorf("CSP %q does not end in one connect-src 'self'", CSP)
	}
	if FrameCSP != "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; frame-src 'self' about:" {
		t.Errorf("FrameCSP changed: %q", FrameCSP)
	}
	var sources []string
	n := 0
	for _, d := range strings.Split(CSP, ";") {
		if f := strings.Fields(d); len(f) > 0 && f[0] == "script-src" {
			n++
			sources = f[1:]
		}
	}
	if n != 1 {
		t.Fatalf("CSP %q has %d script-src directives, want 1", CSP, n)
	}
	want := make([]string, len(ownScripts))
	for i, s := range ownScripts {
		want[i] = hashOf(string(s.Body))
	}
	if strings.Join(sources, " ") != strings.Join(want, " ") {
		t.Errorf("script-src %q, want the own scripts' hashes %q", sources, want)
	}
	for _, src := range sources {
		if !strings.HasPrefix(src, "'sha256-") || !strings.HasSuffix(src, "'") {
			t.Errorf("script-src holds %q, which is not a hash", src)
		}
		for _, bad := range []string{"unsafe-inline", "unsafe-eval", "unsafe-hashes", "strict-dynamic", "'self'", "nonce-", ":", "*"} {
			if strings.Contains(src, bad) {
				t.Errorf("script-src holds %q", bad)
			}
		}
	}
	loaded := loadScripts(fstest.MapFS{"scripts/a.js": {Data: []byte("x")}, "scripts/b.js": {Data: []byte("y")}, "scripts/c.txt": {Data: []byte("z")}})
	if len(loaded) != 2 || loaded[0].Name != "a.js" || loaded[1].Name != "b.js" {
		t.Errorf("loaded %+v, want a.js and b.js only", loaded)
	}
	if got := policy(loaded); got != baseCSP+"; script-src "+hashOf("x")+" "+hashOf("y")+"; connect-src 'self'" {
		t.Errorf("policy of two scripts = %q", got)
	}
}

// forbiddenInScripts are the calls and properties an own script may not use: none parses HTML from a string,
// evaluates a string as code, makes a request, loads a module or submits a form. poll.js alone may make one request,
// pollFetch, to its page's since route.
var forbiddenInScripts = []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function",
	"Function(", "fetch(", "XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "import(", ".submit(", "requestSubmit",
	"location.reload", "location.href", "location.assign", "location.replace"}

// pollFetch is the one request an own script makes: poll.js's GET of the since route its bar's data-since names.
const pollFetch = "window.fetch(since + '?after=' + encodeURIComponent(last), {credentials: 'same-origin', cache: 'no-store', redirect: 'error'})"

var (
	stringTimer   = regexp.MustCompile(`set(Timeout|Interval)\s*\(\s*['"` + "`" + `]`)
	handlerProp   = regexp.MustCompile(`\.on[a-z]+\s*=`)
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	tmplScript    = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
)

// TestOwnScriptCodeRules: no embedded own script uses a forbidden call, a string timer or an on… handler property,
// and page.tmpl has no inline handler, no javascript: link and no script but the own scripts' slot.
func TestOwnScriptCodeRules(t *testing.T) {
	names, err := fs.Glob(assets, "scripts/*.js")
	if err != nil || len(names) == 0 {
		t.Fatalf("no embedded scripts: %v", err)
	}
	for _, name := range names {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if name == "scripts/poll.js" {
			if strings.Count(src, "fetch(") != 1 || strings.Count(src, pollFetch) != 1 || strings.Count(src, "since = bar.getAttribute('data-since')") != 1 {
				t.Errorf("%s makes a request other than its one GET of data-since", name)
			}
			src = strings.Replace(src, pollFetch, "", 1)
		}
		for _, bad := range forbiddenInScripts {
			if strings.Contains(src, bad) {
				t.Errorf("%s uses %s", name, bad)
			}
		}
		if stringTimer.MatchString(src) {
			t.Errorf("%s passes a string to a timer", name)
		}
		if handlerProp.MatchString(src) {
			t.Errorf("%s attaches a listener other than with addEventListener", name)
		}
	}
	b, err := fs.ReadFile(assets, "page.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := string(b)
	if m := inlineHandler.FindString(tmpl); m != "" {
		t.Errorf("page.tmpl holds an inline handler %q", m)
	}
	if strings.Contains(strings.ToLower(tmpl), "javascript:") {
		t.Error("page.tmpl holds a javascript: link")
	}
	for _, m := range tmplScript.FindAllString(tmpl, -1) {
		if m != "<script>{{.}}</script>" {
			t.Errorf("page.tmpl holds a script other than an own one's slot: %q", m)
		}
	}
}
