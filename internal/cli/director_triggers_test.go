package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// contextReport runs `director context-report` in this process with stdin as Claude Code's status-line input
// and env as its environment.
func contextReport(t *testing.T, stdin string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getenv = func(k string) string { return env[k] }
	app.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"director", "context-report"}, args...))
	return code, stdout.String(), stderr.String()
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "adapter", "claude", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestContextReportWritesTheSessionsReport: over Claude Code's recorded status-line input, context-report writes
// the session's context.json from GUILDHALL_COPY and GUILDHALL_SESSION and prints the percent as the status line;
// input with no percent yet writes and prints nothing; it never opens a store.
func TestContextReportWritesTheSessionsReport(t *testing.T) {
	copyDir := t.TempDir()
	dir := filepath.Join(copyDir, "director", "session-3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GUILDHALL_COPY": copyDir, session.NumberEnv: "3"}
	code, out, stderr := contextReport(t, fixture(t, "statusline.json"), env)
	if code != 0 || out != "context 72%\n" {
		t.Fatalf("context-report = %d, %q, stderr %s; want the percent", code, out, stderr)
	}
	got, err := session.ReadContext(filepath.Join(dir, session.ContextFile))
	if err != nil || got != (session.Context{UsedPercent: 72, ReportedAt: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}) {
		t.Fatalf("context.json = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "director.store")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("context-report made a store: %v", err)
	}

	fresh := filepath.Join(copyDir, "director", "session-4")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	env[session.NumberEnv] = "4"
	if code, out, stderr := contextReport(t, fixture(t, "statusline-fresh.json"), env); code != 0 || out != "" {
		t.Fatalf("no percent yet = %d, %q, stderr %s; want nothing", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(fresh, session.ContextFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no percent yet wrote a report: %v", err)
	}
}

func TestContextReportRefusals(t *testing.T) {
	copyDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(copyDir, "director", "session-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{"GUILDHALL_COPY": copyDir, session.NumberEnv: "1"}
	cases := []struct {
		name, stdin string
		env         map[string]string
		args        []string
		want        string
	}{
		{"no copy", fixture(t, "statusline.json"), map[string]string{session.NumberEnv: "1"}, nil, "--copy is required, or GUILDHALL_COPY"},
		{"no session", fixture(t, "statusline.json"), map[string]string{"GUILDHALL_COPY": copyDir}, nil, "GUILDHALL_SESSION"},
		{"bad session", fixture(t, "statusline.json"), map[string]string{"GUILDHALL_COPY": copyDir, session.NumberEnv: "one"}, nil, "GUILDHALL_SESSION"},
		{"not json", "context: lots", ok, nil, "status line"},
		{"argument", fixture(t, "statusline.json"), ok, []string{"extra"}, `unexpected argument "extra"`},
	}
	for _, c := range cases {
		code, out, stderr := contextReport(t, c.stdin, c.env, c.args...)
		if code != 1 || out != "" || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %q, stderr %q; want exit 1 naming %q", c.name, code, out, stderr, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(copyDir, "director", "session-1", session.ContextFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused report wrote: %v", err)
	}
}

// TestHandoffPolicyNamesTheReportOnlyForARuntimeWithOne: the session records the config's thresholds and its
// context.json path only when the runtime reports context.
func TestHandoffPolicyNamesTheReportOnlyForARuntimeWithOne(t *testing.T) {
	cfg, err := config.Parse([]byte(directorConfigWith(t, "[director]\nhandoff_after_tasks = 2\nhandoff_context_percent = 80\nhandoff_idle = \"30m\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "director", "session-1")
	if got, err := handoffPolicy(cfg, claude.Adapter{}, dir); err != nil ||
		got != (store.HandoffPolicy{AfterTasks: 2, ContextPercent: 80, ContextFile: filepath.Join(dir, session.ContextFile)}) {
		t.Errorf("claude: policy = %+v, %v", got, err)
	}
	if got, err := handoffPolicy(cfg, fake.Adapter{}, dir); err != nil || got != (store.HandoffPolicy{AfterTasks: 2, ContextPercent: 80}) {
		t.Errorf("fake: policy = %+v, %v; want no report path", got, err)
	}
}

// TestDirectorStartRecordsTheHandoffPolicy: a supervised session records its config's thresholds, and the path of
// its context report only under a runtime that reports context.
func TestDirectorStartRecordsTheHandoffPolicy(t *testing.T) {
	for _, rt := range []string{"fake", "claude"} {
		t.Run(rt, func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \""+rt+"\"\nhandoff_after_tasks = 4\n", "guildhall director ready", "/exit")
			if rt == "claude" {
				claudetest.Install(t)
			}
			if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
				t.Fatalf("director start: exit %d, %s, stderr %s", code, out, stderr)
			}
			want := store.HandoffPolicy{AfterTasks: 4, ContextPercent: config.DefaultHandoffContextPercent}
			if rt == "claude" {
				want.ContextFile = filepath.Join(dir, "director", "session-1", session.ContextFile)
			}
			if got := sessions(t, dir); len(got) != 1 || got[0].Handoff != want {
				t.Fatalf("sessions = %+v; want the policy %+v", got, want)
			}
		})
	}
}

// idleWatch records session 1 running in a pane in a new store and returns a hooked watch over it with
// handoff_idle 30m, five minutes' idle_after, a pane double and a clock the test moves.
func idleWatch(t *testing.T) (*sessionWatch, *time.Time, *typedPanes, string) {
	t.Helper()
	path := emptyStore(t)
	startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	pane := &typedPanes{}
	w := &sessionWatch{storePath: path, state: t.TempDir(), n: 1, grace: time.Minute, hook: true, idleAfter: 5 * time.Minute,
		handoffIdle: 30 * time.Minute, pane: pane, now: func() time.Time { return clock }}
	return w, &clock, pane, path
}

func handoffDues(t *testing.T, path string) []store.HandoffDue {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.HandoffDues(1)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestIdleTimerTypesTheLineOncePerQuietPeriod: with an injected clock, the supervisor types the idle line once
// handoff_idle passes after a turn end with no wake or turn signal since, not again in the same quiet period, and
// again after the next turn.
func TestIdleTimerTypesTheLineOncePerQuietPeriod(t *testing.T) {
	w, clock, pane, path := idleWatch(t)
	w.decide(session.TurnEnded)
	*clock = clock.Add(30*time.Minute - time.Second)
	w.decide("")
	if len(pane.lines) != 0 {
		t.Fatalf("before handoff_idle typed %q", pane.lines)
	}
	*clock = clock.Add(time.Second)
	if got := w.decide(""); got != keepRunning {
		t.Fatalf("the idle line ended the runtime: %d", got)
	}
	want := "w1:p2 guildhall: 0 unread; handoff due (idle 30m0s)"
	if len(pane.lines) != 1 || pane.lines[0] != want {
		t.Fatalf("at handoff_idle typed %q, want %q", pane.lines, want)
	}
	*clock = clock.Add(2 * time.Hour)
	w.decide("")
	if len(pane.lines) != 1 {
		t.Fatalf("the same quiet period typed again: %q", pane.lines)
	}
	w.decide(session.TurnStarted)
	w.decide(session.TurnEnded)
	*clock = clock.Add(30 * time.Minute)
	w.decide("")
	if len(pane.lines) != 2 {
		t.Fatalf("the next quiet period typed %q; want a second line", pane.lines)
	}
	if got := handoffDues(t, path); len(got) != 2 || got[0].Crossing != 1 || got[1].Crossing != 2 || got[1].Trigger != store.HandoffIdle {
		t.Fatalf("dues = %+v; want two idle crossings", got)
	}
}

// TestIdleTimerWaitsForTheTurnAndIsOffAtZero: mid-turn the idle line waits until the Director counts as idle, and
// handoff_idle 0 never types it.
func TestIdleTimerWaitsForTheTurnAndIsOffAtZero(t *testing.T) {
	w, clock, pane, _ := idleWatch(t)
	w.idleAfter = time.Hour
	w.decide(session.TurnEnded)
	w.decide(session.TurnStarted)
	*clock = clock.Add(45 * time.Minute)
	w.decide("")
	if len(pane.lines) != 0 {
		t.Fatalf("mid-turn typed %q", pane.lines)
	}
	*clock = clock.Add(15 * time.Minute)
	w.decide("")
	if len(pane.lines) != 1 {
		t.Fatalf("past idle_after typed %q; want the idle line", pane.lines)
	}

	off, clock, pane, _ := idleWatch(t)
	off.handoffIdle = 0
	off.decide(session.TurnEnded)
	*clock = clock.Add(24 * time.Hour)
	off.decide("")
	if len(pane.lines) != 0 {
		t.Fatalf("handoff_idle 0 typed %q", pane.lines)
	}
}

// TestDirectorStatusCarriesTheContextReport: the status feed's session section carries the newest context report
// from the path the session recorded, and none when nothing was reported.
func TestDirectorStatusCarriesTheContextReport(t *testing.T) {
	d := newRunDirector(t)
	dir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
		Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64),
		Handoff: store.HandoffPolicy{ContextPercent: 70, ContextFile: filepath.Join(dir, session.ContextFile)}})
	if cerr := s.Close(); err != nil || cerr != nil {
		t.Fatal(err, cerr)
	}
	status := func() statusSession {
		code, out, stderr := runCLI(t, "director", "status", "--json", "--store", d.store, "--state", d.state)
		var st directorStatus
		if err := json.Unmarshal([]byte(out), &st); code != 0 || err != nil {
			t.Fatalf("status: exit %d, %v, stderr %s", code, err, stderr)
		}
		return st.Session
	}
	if got := status(); got.Context != nil {
		t.Fatalf("no report: context = %+v", got.Context)
	}
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if err := session.WriteContext(dir, session.Context{UsedPercent: 64, ReportedAt: at}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.Context == nil || *got.Context != (session.Context{UsedPercent: 64, ReportedAt: at}) {
		t.Fatalf("context = %+v; want the report", got.Context)
	}
}

// directorConfigWith is the test copy's config with table appended.
func directorConfigWith(t *testing.T, table string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "run", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n" + table
}
