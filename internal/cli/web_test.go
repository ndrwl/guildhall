package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/web"
)

// webSourceFor is the task pages' Source over the marked copy dir, at clerkNow.
func webSourceFor(t *testing.T, dir string) webSource {
	t.Helper()
	app, _, _ := newTestApp(nil, nil)
	app.Now = func() time.Time { return clerkNow }
	c, err := app.clerkCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	return webSource{a: app, c: c}
}

// TestWebSourceAsksFollowClerk: a task's asks are its open decisions; once answered, a decision waiting for task
// start is no ask but the story's answered node, and a void decision is no ask but a void node, as Clerk's Needs
// you split has it.
func TestWebSourceAsksFollowClerk(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 1 || task.Asks[0].ID != "accept-plan" || task.Asks[0].Kind != "approval" || task.Asks[0].Question != "Accept the plan?" ||
		task.Asks[0].Subject != "sha256:"+strings.Repeat("a", 64) || task.Asks[0].OpenedAt == nil {
		t.Fatalf("asks = %+v", task.Asks)
	}
	if task.Stands != "waiting for your approval" || task.Title != "Approve the plan" || task.State != "open" || !task.ReadAt.Equal(clerkNow) {
		t.Errorf("task = %q %q %q %v", task.Stands, task.Title, task.State, task.ReadAt)
	}
	var plan, decision *web.Node
	for i, n := range task.Story {
		switch n.Title {
		case "Plan · round 1":
			plan = &task.Story[i]
		case "Approval · accept-plan":
			decision = &task.Story[i]
		}
	}
	if plan == nil || plan.State != web.StateDone || plan.Body != "Planned the change in three steps." {
		t.Errorf("plan node = %+v in %+v", plan, task.Story)
	}
	if decision == nil || decision.State != web.StateYou || decision.Ask != "accept-plan" {
		t.Errorf("decision node = %+v", decision)
	}
	if task.Story[0].Title != "Task added" {
		t.Errorf("the story does not start with the task added: %+v", task.Story[0])
	}

	answerClerkDecision(t, dir, "app", "a-approve", "accept-plan", "approve\n\nGo ahead.")
	task, err = src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 0 || task.Stands != "waiting for the Director" {
		t.Fatalf("an answered decision waiting for task start is an ask: %+v, %q", task.Asks, task.Stands)
	}
	for _, n := range task.Story {
		if n.Title == "Approval · accept-plan" && (n.State != web.StateDone || n.Word != "Approve" || len(n.Said) != 1 || n.Said[0].Text != "Go ahead.") {
			t.Errorf("answered node = %+v", n)
		}
	}

	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("open-void", store.Decision{Repository: "app", TaskID: "a-approve", ID: "accept-2", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("b", 64)}, Options: []string{"approved", "changes_requested"}, Question: "Accept again?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VoidDecision("void", "app", "a-approve", "accept-2", "superseded: the spec changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	task, err = src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 0 {
		t.Errorf("a void decision is an ask: %+v", task.Asks)
	}
	found := false
	for _, n := range task.Story {
		if n.Title == "Approval · accept-2" {
			found = n.State == web.StateVoid && strings.Contains(n.Body, "superseded: the spec changed") && n.Ask == ""
		}
	}
	if !found {
		t.Errorf("no void node for accept-2 in %+v", task.Story)
	}
}

// TestWebSourceDecisionTone: an answered decision's step and story node take their state from the page's option
// table, so a merge hold and a follow-ups none are done with their word, not sent back; and a question with options
// answered in free text shows the text.
func TestWebSourceDecisionTone(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("d", 64)}
	for _, d := range []store.Decision{
		{ID: "deliver-1", Kind: store.DecisionMerge, Subject: subject, Options: []string{"merge", "hold"}, Question: "Merge it?"},
		{ID: "validate-1", Kind: store.DecisionFollowUps, Subject: subject, Options: []string{"pick", "none"}, Question: "Which follow-ups?"},
		{ID: "ask-1", Kind: store.DecisionQuestion, Options: []string{"left", "right"}, Question: "Which way?"},
	} {
		d.Repository, d.TaskID = "app", "a-approve"
		if _, err := s.OpenDecision("open:"+d.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	answerClerkDecision(t, dir, "app", "a-approve", "deliver-1", "hold")
	answerClerkDecision(t, dir, "app", "a-approve", "validate-1", "none")
	answerClerkDecision(t, dir, "app", "a-approve", "ask-1", "Neither; go straight on.")
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]web.Node{}
	for _, n := range task.Story {
		nodes[n.Title] = n
	}
	if n, ok := nodes["Merge · deliver-1"]; !ok || n.State != web.StateDone || n.Word != "Hold" {
		t.Errorf("merge hold node = %+v", n)
	}
	if n, ok := nodes["Follow ups · validate-1"]; !ok || n.State != web.StateDone || n.Word != "None" {
		t.Errorf("follow-ups none node = %+v", n)
	}
	if n, ok := nodes["Question · ask-1"]; !ok || n.State != web.StateDone || n.Word != "answered" || len(n.Said) != 1 || n.Said[0].Text != "Neither; go straight on." {
		t.Errorf("free-text question node = %+v", n)
	}
	steps := map[string]web.Step{}
	for _, st := range task.Steps {
		steps[st.Name] = st
	}
	if st := steps["Deliver"]; st.State != web.StateDone || st.Note != "deliver-1 hold" {
		t.Errorf("deliver step = %+v", st)
	}
	if st := steps["Validate"]; st.State != web.StateDone || st.Note != "validate-1 none" {
		t.Errorf("validate step = %+v", st)
	}
}

// TestWebSourceStagesSlicesAndRefusals: a grill handed to the Director is its stage, not an ask; a multi-PR task has
// its slices, latest PR and slice merge; an unknown task is ErrNotFound and a held store ErrBusy.
func TestWebSourceStagesSlicesAndRefusals(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	grill, err := src.Task("app", "a-grill")
	if err != nil {
		t.Fatal(err)
	}
	if grill.DirectorStage != "grill" || len(grill.Asks) != 0 {
		t.Errorf("grill: stage %q, asks %+v", grill.DirectorStage, grill.Asks)
	}
	if len(grill.Steps) == 0 || grill.Steps[0].Name != "Grill" || grill.Steps[len(grill.Steps)-1].State != web.StateLater {
		t.Errorf("grill steps = %+v", grill.Steps)
	}
	sliced, err := src.Task("app", "a-sliced")
	if err != nil {
		t.Fatal(err)
	}
	if sl := sliced.Slices; sl == nil || sl.Current != 2 || sl.Total != 3 || len(sl.Items) != 3 || sl.Items[0].State != "merged" || sl.Items[0].PR == nil || sl.Items[0].PR.Number != 401 {
		t.Fatalf("slices = %+v", sliced.Slices)
	}
	if pr := sliced.Status.PR; pr == nil || pr.Number != 402 || sliced.Status.CI != web.CIPending || sliced.Status.Step != "Validate" {
		t.Errorf("status = %+v", sliced.Status)
	}
	merged := false
	for _, n := range sliced.Story {
		merged = merged || n.Title == "Slice 1 merged"
	}
	if !merged {
		t.Errorf("no slice merge node in %+v", sliced.Story)
	}
	var spec *web.Document
	for i, d := range sliced.Documents {
		if d.Name == "accept-1-spec" {
			spec = &sliced.Documents[i]
		}
	}
	if spec == nil || spec.Kind != "Spec" || spec.Path != "tasks/app/a-sliced/plan-1/spec.md" || spec.RecordedAt == nil {
		t.Errorf("spec document = %+v", spec)
	}

	if _, err := src.Task("app", "nope"); !errors.Is(err, web.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	held, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	old := webLockWait
	// timing:allow deliberate refusal input: the writer holds the lock for the whole call, so any wait is refused.
	webLockWait = 10 * time.Millisecond
	defer func() { webLockWait = old }()
	if _, err := src.Task("app", "a-grill"); !errors.Is(err, web.ErrBusy) {
		t.Errorf("held store: %v, want ErrBusy", err)
	}
}

// TestWebNeedsYouLeavesOthersToTheDirector: the copy's install item is no ask and no Director wait, the task's
// decision items are its asks, and its other needs_me items wait on the Director.
func TestWebNeedsYouLeavesOthersToTheDirector(t *testing.T) {
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", Title: "T", State: store.TaskOpen}}}
	d := clerkData{now: clerkNow, byKey: map[string]clerkTask{"app/t1": ct}}
	d.status.NeedsMe = []needsMeItem{
		{Kind: decisionKind, Repository: "app", TaskID: "t1", Decision: "d1", Reason: "Answer it"},
		{Kind: installKind, Repository: "app", TaskID: "t1", Reason: "install the new guildhall"},
		{Kind: checksKind, Repository: "app", TaskID: "t1", Reason: "checks are pending"},
		{Kind: checksKind, Repository: "app", TaskID: "t2", Reason: "another task's"},
	}
	d.status.Decisions = []statusDecision{{Repository: "app", TaskID: "t1", ID: "d1", Question: "Answer it"}, {Repository: "app", TaskID: "t2", ID: "d2"}}
	task := webBuilder{d: d, ct: ct}.task()
	if len(task.DirectorWaits) != 1 || task.DirectorWaits[0] != "checks are pending" {
		t.Errorf("Director waits = %q", task.DirectorWaits)
	}
	if len(task.Asks) != 1 || task.Asks[0].ID != "d1" {
		t.Errorf("asks = %+v", task.Asks)
	}
}

// webStub is a WebListen that binds loopback ports, as a test cannot bind a tailnet address, recording each address
// it was asked for; with fail set it refuses as an address off the tailnet is refused.
type webStub struct {
	mu    sync.Mutex
	asked []string
	addrs []string
	fail  error
}

func (w *webStub) listen(address string) (net.Listener, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked = append(w.asked, address)
	if w.fail != nil {
		return nil, w.fail
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		w.addrs = append(w.addrs, ln.Addr().String())
	}
	return ln, err
}

func (w *webStub) bound() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.addrs...)
}

// testWebHost is the [web] address the tests configure, which the server requires as each request's Host.
const testWebHost = "host.tail.ts.net:4388"

// getPage fetches a page from a test server, its listener at url's host, as the configured address testWebHost.
func getPage(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testWebHost
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestWebCommand: guildhall web serves the copy's pages at its [web] address until it is stopped; a copy with no
// [web] table and an address off the tailnet are refused.
func TestWebCommand(t *testing.T) {
	dir := clerkFixture(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return dir, nil }
	if code := app.Run([]string{"web"}); code != 1 || !strings.Contains(stderr.String(), "no [web] table") {
		t.Errorf("no [web]: exit %d, stderr %s", code, stderr)
	}
	if code := app.Run([]string{"web", "extra"}); code != 2 {
		t.Errorf("an argument: exit %d, want 2", code)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"127.0.0.1:4388\"\n")
	stderr.Reset()
	if code := app.Run([]string{"web", "--copy", dir}); code != 1 || !strings.Contains(stderr.String(), "not a Tailscale address") {
		t.Errorf("loopback: exit %d, stderr %s; want it refused off the tailnet", code, stderr)
	}

	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{}
	app.WebListen = stub.listen
	app.webSignals = make(chan os.Signal, 1)
	served := make(chan string, 1)
	app.webServing = func(addr string) { served <- addr }
	done := make(chan int, 1)
	go func() { done <- app.Run([]string{"web"}) }()
	addr := <-served
	code, body := getPage(t, "http://"+addr+"/app/a-approve")
	if code != http.StatusOK || !strings.Contains(body, "<h1>Approve the plan</h1>") || !strings.Contains(body, `id="accept-plan"`) {
		t.Errorf("page: %d\n%s", code, body)
	}
	app.webSignals <- os.Interrupt
	if code := <-done; code != 0 {
		t.Errorf("web exited %d, stderr %s", code, stderr)
	}
	if len(stub.asked) != 1 || stub.asked[0] != "host.tail.ts.net:4388" || !strings.Contains(stdout.String(), "serving the task pages at http://host.tail.ts.net:4388/<repo>/<task>") {
		t.Errorf("asked %q; stdout %s", stub.asked, stdout)
	}
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Error("the server still listens after web stopped")
	}
}

// TestStartWebRecordsAndWarns: a [web] table serves and records the session serving it, which director status
// reports while that session runs; a bind failure only warns and is recorded; no table serves nothing.
func TestStartWebRecordsAndWarns(t *testing.T) {
	dir := clerkFixture(t)
	state := filepath.Join(dir, "state")
	cfg := &config.Config{Web: &config.Web{Address: "host.tail.ts.net:4388"}}
	app, stdout, _ := newTestApp(nil, nil)
	if s := app.startWeb(&config.Config{}, 1, dir, filepath.Join(dir, "director.store"), state); s != nil {
		t.Error("no [web] table serves")
	}
	if _, err := os.Stat(filepath.Join(state, webRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no [web] table wrote a record: %v", err)
	}
	stub := &webStub{}
	app.WebListen = stub.listen
	s := app.startWeb(cfg, 3, dir, filepath.Join(dir, "director.store"), state)
	if s == nil || len(stub.bound()) != 1 {
		t.Fatalf("not serving; stdout %s", stdout)
	}
	if code, _ := getPage(t, "http://"+stub.bound()[0]+"/app/a-grill"); code != http.StatusOK {
		t.Errorf("page: %d", code)
	}
	running := statusSession{State: string(store.DirectorRunning), Number: 3}
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := readWebStatus(cfgPath, state, running); got == nil || got.State != webNotConfigured {
		t.Errorf("status with no [web]: %+v", got)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	if got := readWebStatus(cfgPath, state, running); got == nil || *got != (webStatus{State: webServing, URL: "http://host.tail.ts.net:4388"}) {
		t.Errorf("status while serving: %+v", got)
	}
	if got := readWebStatus(cfgPath, state, statusSession{State: string(store.DirectorRunning), Number: 4}); got.State != webNotServing {
		t.Errorf("status of another session: %+v", got)
	}
	s.close()
	s.close()
	if _, err := net.Dial("tcp", stub.bound()[0]); err == nil {
		t.Error("still listening after close")
	}
	if got := readWebStatus(cfgPath, state, running); got.State != webNotServing {
		t.Errorf("status after close: %+v", got)
	}

	stub.fail = errors.New(`web address "host.tail.ts.net:4388" resolves to 192.168.1.5: not a Tailscale address`)
	stdout.Reset()
	if s := app.startWeb(cfg, 3, dir, filepath.Join(dir, "director.store"), state); s != nil {
		t.Error("a refused bind serves")
	}
	if !strings.Contains(stdout.String(), "web: warning: not serving the task pages:") {
		t.Errorf("no warning: %s", stdout)
	}
	if got := readWebStatus(cfgPath, state, running); got.State != webFailed || !strings.Contains(got.Reason, "not a Tailscale address") {
		t.Errorf("status after a refused bind: %+v", got)
	}
}

// TestDirectorSessionServesTheTaskPages: a supervised Director session with a [web] table serves the pages while it
// runs, binds again at a reload and stops serving when it ends; a Steward session's watchers serve nothing.
func TestDirectorSessionServesTheTaskPages(t *testing.T) {
	dir, _ := supervisedCopy(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.WebListen = stub.listen })
	testenv.WaitFor(t, "session 1 serves", func() bool { return len(stub.bound()) == 1 })
	first := stub.bound()[0]
	if code, body := getPage(t, "http://"+first+"/app/t-1"); code != http.StatusOK || !strings.Contains(body, "Sort the task list") {
		t.Errorf("page: %d\n%s", code, body)
	}
	if code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir); code != 0 || !strings.Contains(out, `"web":{"state":"serving","url":"http://host.tail.ts.net:4388"}`) {
		t.Errorf("status: exit %d, %s, %s", code, out, stderr)
	}
	copyRun(t, dir, "director", "reload")
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	testenv.WaitFor(t, "session 2 binds again", func() bool { return len(stub.bound()) == 2 })
	if _, err := net.Dial("tcp", first); err == nil {
		t.Error("session 1's server still listens after the reload")
	}
	if code, _ := getPage(t, "http://"+stub.bound()[1]+"/app/t-1"); code != http.StatusOK {
		t.Errorf("session 2's page: %d", code)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d", code)
	}
	if _, err := net.Dial("tcp", stub.bound()[1]); err == nil {
		t.Error("the server still listens after the session stopped")
	}

	w, err := stewardRole.watch(nil, startPlan{cfg: &config.Config{Web: &config.Web{Address: "host.tail.ts.net:4388"}}}, 1, "", "", "", "")
	if err != nil || w.web != nil {
		t.Errorf("a Steward session's watchers: %+v, %v; want no web server", w, err)
	}
}

// TestDirectorSessionBindFailureOnlyWarns: a refused bind leaves the session running, and status says it failed.
func TestDirectorSessionBindFailureOnlyWarns(t *testing.T) {
	dir, _ := supervisedCopy(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{fail: errors.New("not a Tailscale address")}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.WebListen = stub.listen })
	if code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir); code != 0 || !strings.Contains(out, `"web":{"state":"failed to bind","url":"http://host.tail.ts.net:4388","reason":"not a Tailscale address"}`) {
		t.Errorf("status: exit %d, %s, %s", code, out, stderr)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d", code)
	}
}

// feedbackFrom writes body to <dir>/documents/<rel> and returns a webBuilder whose files record it, under the
// slash path rel, as send-back-build-1 with its sha256.
func feedbackFrom(t *testing.T, dir, rel, body string) webBuilder {
	t.Helper()
	file := filepath.Join(dir, "documents", rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	doc := store.DocumentRef{Name: "send-back-build-1", Path: rel, SHA256: hex.EncodeToString(sum[:])}
	return webBuilder{fl: taskFiles{Documents: []timedDocument{{DocumentRef: doc}}}, state: dir}
}

func TestWebFeedbackReadsDotDotPrefixedName(t *testing.T) {
	b := feedbackFrom(t, t.TempDir(), "..foo/x.md", "Fix the test.\n")
	if got := b.feedback("build", 1); got != "Fix the test." {
		t.Errorf("feedback = %q, want the quoted text", got)
	}
}

func TestWebFeedbackRefusesBackslashPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash is a path separator on Windows")
	}
	b := feedbackFrom(t, t.TempDir(), `a\b.md`, "Fix the test.\n")
	if got := b.feedback("build", 1); got != "" {
		t.Errorf("feedback = %q, want none for a backslash path", got)
	}
}
