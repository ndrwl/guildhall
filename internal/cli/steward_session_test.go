package cli

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// stewardSessionConfig is a Steward copy's config naming the private checkout private, with table appended.
func stewardSessionConfig(private, table string) string {
	return "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = '" + private + "'\n\n" + table
}

// stewardCopy is a Steward copy: its config with [steward] running the fake runtime, an empty steward.store, a
// private config checkout that is a Git work tree holding cliRoster and an empty managed.toml, and
// baseline/roles/steward.md playing steps. guildhall on PATH is this test binary, and so are ssh and scp, which
// no session test runs. The copy's path is short, as a session endpoint's path is bounded.
func stewardCopy(t *testing.T, steps ...string) string {
	t.Helper()
	t.Setenv(roleEnv, "guildhall")
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	private := filepath.Join(dir, "private")
	for _, d := range []string{private, filepath.Join(dir, "baseline", "roles")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(private, "roster.toml"), cliRoster)
	writeFile(t, filepath.Join(private, "managed.toml"), "schema_version = 1\n")
	if out, err := exec.Command("git", "init", "-q", private).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	writeFile(t, filepath.Join(dir, "config.toml"), stewardSessionConfig(private, "[steward]\nname = \"steward\"\nadapter = \"fake\"\n"))
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, stewardStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "baseline", "roles", "steward.md"), sessionScript(steps...))
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"guildhall", "ssh", "scp"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.Link(exe, filepath.Join(bin, name)); err != nil {
			copyFile(t, exe, filepath.Join(bin, name))
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// stewardSessions lists the Steward copy's sessions from its steward.store.
func stewardSessions(t *testing.T, dir string) []store.DirectorSession {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, stewardStoreFile), store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var out []store.DirectorSession
	for n := 1; ; n++ {
		d, err := s.DirectorSession(n)
		if errors.Is(err, store.ErrNotFound) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
}

// waitStewardSession waits until the Steward copy's newest session is number n and in state.
func waitStewardSession(t *testing.T, dir string, n int, state store.DirectorState) []store.DirectorSession {
	t.Helper()
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(50 * time.Millisecond) {
		got := stewardSessions(t, dir)
		if len(got) == n && got[n-1].State == state {
			return got
		}
		if len(got) > n || time.Now().After(end) {
			t.Fatalf("steward sessions = %+v; want session %d %s", got, n, state)
		}
	}
}

// superviseSteward runs `steward start` over dir in this process with grace as its stop grace, its runtime
// reading a pipe that stays open, and returns its exit status once it ends.
func superviseSteward(t *testing.T, dir string, grace time.Duration) <-chan int {
	t.Helper()
	stdin, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = typing.Close() })
	app, _, _ := supervisorApp(stdin, grace)
	app.Stdout, app.Stderr = io.Discard, io.Discard
	done := make(chan int, 1)
	// exited closes when the supervisor returns: the test's own ended() drains done, so the cleanup cannot read it.
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- app.Run([]string{"steward", "start", "--copy", dir})
	}()
	t.Cleanup(func() {
		// A test that failed before stopping the session ends it, so its supervisor does not outlive the copy.
		select {
		case <-exited:
		default:
			_, _, _ = runCLI(t, "steward", "stop", "--now", "--copy", dir)
			select {
			case <-exited:
			case <-time.After(testenv.Ceiling(time.Minute)):
			}
		}
	})
	return done
}

// TestStewardSessionLifecycle: steward start supervises a Steward session in the copy's steward.store, never a
// director.store, rendered with the Steward's role and startup; the session recovers until steward ready, and steward
// pause, resume, reload, handoff and stop act on it as the Director's commands act on a Director session.
func TestStewardSessionLifecycle(t *testing.T) {
	dir := stewardCopy(t, "guildhall steward ready")
	done := superviseSteward(t, dir, time.Nanosecond)
	got := waitStewardSession(t, dir, 1, store.DirectorRunning)
	if got[0].Adapter != "fake" || got[0].Dir != filepath.Join(dir, "director", "session-1") {
		t.Errorf("session 1 = %+v", got[0])
	}
	if _, err := os.Stat(filepath.Join(dir, "director.store")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a Steward session made a director.store: %v", err)
	}
	startup, err := os.ReadFile(filepath.Join(dir, "director", "session-1", render.StartupFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Guildhall Steward session 1\n", "run `guildhall steward ready`"} {
		if !strings.Contains(string(startup), want) {
			t.Errorf("startup.md lacks %q:\n%s", want, startup)
		}
	}
	if strings.Contains(string(startup), "Director") {
		t.Errorf("a Steward's startup.md names the Director:\n%s", startup)
	}
	role, err := os.ReadFile(filepath.Join(dir, "director", "session-1", render.RoleFile))
	if err != nil || !strings.Contains(string(role), "fake-director: guildhall steward ready") {
		t.Errorf("role prompt = %q, %v; want the copy's baseline/roles/steward.md", role, err)
	}

	copyRun(t, dir, "steward", "pause")
	waitStewardSession(t, dir, 1, store.DirectorPaused)
	copyRun(t, dir, "steward", "resume")
	waitStewardSession(t, dir, 1, store.DirectorRunning)

	copyRun(t, dir, "steward", "reload", "--now")
	got = waitStewardSession(t, dir, 2, store.DirectorRunning)
	if got[0].State != store.DirectorStopped || got[0].EndReason != "reloaded" {
		t.Errorf("session 1 = %+v; want stopped as reloaded", got[0])
	}
	copyRun(t, dir, "steward", "handoff")
	got = waitStewardSession(t, dir, 3, store.DirectorRunning)
	if got[1].EndReason != "reloaded" {
		t.Errorf("session 2 = %+v; want the handoff to have reloaded it", got[1])
	}
	r := readReload(t, filepath.Join(dir, stewardStoreFile))
	if r.State != store.ReloadApplied || r.OldSession != 2 || r.NewSession != 3 || !r.Now {
		t.Errorf("handoff reload = %+v; want --now applied from session 2 to 3", r)
	}

	copyRun(t, dir, "steward", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("steward start exited %d, want 0", code)
	}
	if got := stewardSessions(t, dir); len(got) != 3 || got[2].State != store.DirectorStopped {
		t.Errorf("sessions = %+v; want three, the third stopped", got)
	}
}

// TestStewardSessionRecoversAfterACrash: a runtime that crashes leaves its session stopped, and the next steward
// start records a new session, recovering until steward ready.
func TestStewardSessionRecoversAfterACrash(t *testing.T) {
	dir := stewardCopy(t, "exit 3")
	app, stdout, stderr := supervisorApp(nil, time.Hour)
	if code := app.Run([]string{"steward", "start", "--copy", dir}); code != 1 || !strings.Contains(stderr.String(), "steward start: the runtime exited with status 3") {
		t.Fatalf("crashed session: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if got := stewardSessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped {
		t.Fatalf("sessions after the crash = %+v", got)
	}
	writeFile(t, filepath.Join(dir, "baseline", "roles", "steward.md"), sessionScript("guildhall steward ready"))
	done := superviseSteward(t, dir, time.Hour)
	got := waitStewardSession(t, dir, 2, store.DirectorRunning)
	if got[0].State != store.DirectorStopped {
		t.Errorf("session 1 = %+v", got[0])
	}
	copyRun(t, dir, "steward", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("steward start exited %d, want 0", code)
	}
}

// TestStewardSessionIsWokenByTheGuildmaster: the Guildmaster's message to the steward inbox is stored and woken
// for; one sent from inside a session, a Director's or the Steward's own, is refused, and so is a message to the
// Director's inbox in a Steward store.
func TestStewardSessionIsWokenByTheGuildmaster(t *testing.T) {
	dir := stewardCopy(t, "guildhall steward ready")
	done := superviseSteward(t, dir, time.Hour)
	waitStewardSession(t, dir, 1, store.DirectorRunning)
	envelope := `{"schema": "guildhall.message/1", "id": "g-1", "recipient": "steward", "repository": "steward", "task": "guildmaster", "type": "note", "text": "Roll out standards-3 to the laptop."}`
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(envelope)
	if code := app.Run([]string{"messages", "send", "--copy", dir, "steward", "-"}); code != 0 {
		t.Fatalf("send: exit %d\n%s\n%s", code, stdout, stderr)
	}
	path := filepath.Join(dir, stewardStoreFile)
	m := readMessage(t, path, "g-1")
	if m.Sender != store.SenderGuildmaster || m.Recipient != store.RecipientSteward {
		t.Errorf("message = %+v", m)
	}
	w, err := withStore(path, true, func(s *store.Store) (store.Wake, error) { return s.WakeFor("g-1") })
	if err != nil || w.MessageID != "g-1" {
		t.Errorf("wake for g-1 = %+v, %v; want one recorded", w, err)
	}
	unread, err := withStore(path, true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
	if err != nil || unread[store.UnreadFrom(store.MessageNote, store.SenderGuildmaster)] != 1 {
		t.Errorf("unread = %v, %v; want the Guildmaster's note", unread, err)
	}

	inSession, _, stderr := newTestApp(nil, nil)
	inSession.Getenv = func(k string) string { return map[string]string{session.NumberEnv: "1"}[k] }
	inSession.Getwd = func() (string, error) { return filepath.Join(dir, "director", "session-1"), nil }
	inSession.Stdin = strings.NewReader(strings.Replace(envelope, "g-1", "g-2", 1))
	if code := inSession.Run([]string{"messages", "send", "steward", "-"}); code != 1 || !strings.Contains(stderr.String(), "from guildmaster only") {
		t.Errorf("send from the Steward's own session: exit %d, stderr %s; want it refused", code, stderr)
	}
	app, _, stderr = newTestApp(nil, nil)
	app.Stdin = strings.NewReader(strings.Replace(strings.Replace(envelope, "g-1", "g-3", 1), `"recipient": "steward"`, `"recipient": "director"`, 1))
	if code := app.Run([]string{"messages", "send", "--copy", dir, "director", "-"}); code != 2 || !strings.Contains(stderr.String(), "session inbox is \"steward\"") {
		t.Errorf("send to the director inbox of a Steward store: exit %d, stderr %s; want a usage error", code, stderr)
	}

	copyRun(t, dir, "steward", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("steward start exited %d, want 0", code)
	}
}

// TestStewardStartPreflight: steward start --check passes over a sound copy and refuses, recording nothing, a copy
// whose config, store, private checkout, roster or SSH tools are not fit to run a Steward session.
func TestStewardStartPreflight(t *testing.T) {
	// check runs the preflight with hide, if it is set, not found on PATH.
	check := func(t *testing.T, dir, hide string) (int, string, string) {
		t.Helper()
		app, stdout, stderr := supervisorApp(nil, time.Hour)
		app.LookPath = func(name string) (string, error) {
			if name == hide {
				return "", exec.ErrNotFound
			}
			return exec.LookPath(name)
		}
		code := app.Run([]string{"steward", "start", "--check", "--copy", dir})
		return code, stdout.String(), stderr.String()
	}
	t.Run("sound", func(t *testing.T) {
		dir := stewardCopy(t, "/exit")
		code, out, stderr := check(t, dir, "")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
		}
		for _, want := range []string{"config: admitted", "runtime: fake", "store: opens", "private config: ", "roster: 1 machines, 1 Director copies", "ssh: ", "scp: "} {
			if !strings.Contains(out, want) {
				t.Errorf("preflight lacks %q:\n%s", want, out)
			}
		}
		if got := stewardSessions(t, dir); len(got) != 0 {
			t.Errorf("--check recorded sessions %+v", got)
		}
	})
	keep := func(*testing.T, string) {}
	for name, c := range map[string]struct {
		change func(t *testing.T, dir string)
		want   string
		hide   string
	}{
		"no steward name": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "config.toml"), stewardSessionConfig(filepath.Join(dir, "private"), "[steward]\nadapter = \"fake\"\n"))
		}, "config: refused: ", ""},
		"a director config": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "config.toml"), strings.Replace(cliCopy, "/srv/src/cli", filepath.ToSlash(t.TempDir()), 1))
		}, "not the steward's", ""},
		"a managed copy": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "managed.toml"), "schema_version = 1\n")
		}, "a Steward copy is never managed", ""},
		"no store": {func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, stewardStoreFile)); err != nil {
				t.Fatal(err)
			}
		}, "store: refused: no store at", ""},
		"private config not a work tree": {func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "private", ".git")); err != nil {
				t.Fatal(err)
			}
		}, "is not a Git work tree", ""},
		"invalid roster": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "private", "roster.toml"), "schema_version = 1\n[[machine]]\nlabel = \"Bad Label\"\n")
		}, "roster: refused: ", ""},
		"no ssh":       {keep, "ssh: refused: ", "ssh"},
		"no scp":       {keep, "scp: refused: ", "scp"},
		"no guildhall": {keep, "guildhall: refused: ", "guildhall"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := stewardCopy(t, "/exit")
			c.change(t, dir)
			code, out, stderr := check(t, dir, c.hide)
			if code != 1 || !strings.Contains(out+stderr, c.want) || !strings.Contains(stderr, "guildhall: steward start: ") {
				t.Errorf("exit %d\n%s\n%s\nwant it refused with %q", code, out, stderr, c.want)
			}
			if _, err := os.Stat(filepath.Join(dir, stewardStoreFile)); err == nil {
				if got := stewardSessions(t, dir); len(got) != 0 {
					t.Errorf("a refused start recorded sessions %+v", got)
				}
			}
		})
	}
}

// TestStewardLifecycleRefusesADirectorCopy: the Steward's lifecycle commands never act on a Director's store.
func TestStewardLifecycleRefusesADirectorCopy(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	recordSession(t, filepath.Join(dir, "director.store"), false, store.DirectorRunning)
	for _, sub := range []string{"pause", "stop", "reload", "handoff", "ready"} {
		code, _, stderr := runCLI(t, "steward", sub, "--copy", dir)
		if code != 1 || !strings.Contains(stderr, "is not a Steward copy's store") {
			t.Errorf("steward %s on a Director copy: exit %d, stderr %s", sub, code, stderr)
		}
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorRunning {
		t.Errorf("Director sessions = %+v; want the one session untouched", got)
	}
}

// TestCopyDiscoveryFindsTheStewardStore: a copy holding steward.store and no director.store is a Steward copy, whose
// store copy discovery and --copy name; every other copy's is director.store.
func TestCopyDiscoveryFindsTheStewardStore(t *testing.T) {
	dir := t.TempDir()
	if got := copyStoreFile(dir); got != "director.store" {
		t.Errorf("empty copy: %s", got)
	}
	writeFile(t, filepath.Join(dir, stewardStoreFile), "")
	if got := copyStoreFile(dir); got != stewardStoreFile {
		t.Errorf("Steward copy: %s", got)
	}
	var cp copyFlags
	cp.dir = dir
	var storePath string
	app, _, _ := newTestApp(nil, nil)
	if err := cp.apply(app, &storePath, nil, nil); err != nil || storePath != filepath.Join(dir, stewardStoreFile) {
		t.Errorf("--copy store = %s, %v", storePath, err)
	}
	writeFile(t, filepath.Join(dir, "director.store"), "")
	if got := copyStoreFile(dir); got != "director.store" {
		t.Errorf("copy with both stores: %s", got)
	}
}

// stewardRolloutCopy is stewardApp's fleet and Steward config with a policy change to roll out, its copy being the
// config's directory with an empty steward.store, and the dry run's plan directory and digest.
func stewardRolloutCopy(t *testing.T) (app *App, run func(env map[string]string, stdin string, args ...string) (int, string, string), copyDir, plan, digest string) {
	t.Helper()
	app, stdout, stderr, cfg, _ := stewardApp(t, "schema_version = 1\n[managed.review_rule]\nvalue = \"two reviewers\"\npolicy = true\n")
	copyDir = filepath.Dir(cfg)
	// The copy's own config.toml is the Steward config, as steward status reads it.
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, "config.toml"), string(data))
	s, err := store.Open(filepath.Join(copyDir, stewardStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	plan = filepath.Join(copyDir, stewardRollouts, "standards-3")
	run = func(env map[string]string, stdin string, args ...string) (int, string, string) {
		stdout.Reset()
		stderr.Reset()
		app.Getenv = func(k string) string { return env[k] }
		app.Stdin = strings.NewReader(stdin)
		if args[0] == "rollout" {
			args = append(slices.Clone(args), "--config", cfg, "--machine", "workstation", "--plan", plan)
		}
		code := app.Run(append([]string{"steward"}, args...))
		return code, stdout.String(), stderr.String()
	}
	code, out, errOut := run(nil, "", "rollout", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: exit %d\n%s\n%s", code, out, errOut)
	}
	m := regexp.MustCompile(`rollout plan (sha256:[0-9a-f]{64})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no digest in:\n%s", out)
	}
	return app, run, copyDir, plan, m[1]
}

// TestStewardSessionApproveNeedsTheGuildmaster: inside a session, --approve is refused until the Steward store holds
// the Guildmaster's answer "approve" to the plan's approval decision, which no session can record for them, and is
// accepted once it does; outside a session --approve stands as before.
func TestStewardSessionApproveNeedsTheGuildmaster(t *testing.T) {
	app, run, copyDir, plan, digest := stewardRolloutCopy(t)
	inSession := map[string]string{session.NumberEnv: "4"}
	if code, _, stderr := run(inSession, "", "rollout", "--apply", "--approve", digest); code != 1 || !strings.Contains(stderr, "steward approval request") {
		t.Fatalf("approve without the Guildmaster's answer: exit %d, stderr %s", code, stderr)
	}
	code, out, stderr := run(inSession, "", "approval", "request", "--plan", plan, "--copy", copyDir)
	if code != 0 {
		t.Fatalf("approval request: exit %d\n%s\n%s", code, out, stderr)
	}
	var opened struct {
		Decision store.Decision `json:"decision"`
		Answer   string         `json:"answer"`
	}
	if err := json.Unmarshal([]byte(out), &opened); err != nil {
		t.Fatal(err)
	}
	id := approvalDecisionID(digest)
	if d := opened.Decision; d.ID != id || d.Kind != store.DecisionApproval || d.Subject == nil || d.Subject.Digest != digest || !d.Open() ||
		!strings.Contains(d.Question, digest) || !strings.Contains(opened.Answer, "--decision "+id) {
		t.Errorf("approval decision = %+v", opened)
	}
	// Asking again opens nothing new.
	if code, again, _ := run(inSession, "", "approval", "request", "--plan", plan, "--copy", copyDir); code != 0 || again != out {
		t.Errorf("approval request again: exit %d, %s; want %s", code, again, out)
	}

	answer := []string{"decide", "answer", "--copy", copyDir, "--repo", stewardRepository, "--task", stewardRolloutsTask, "--decision", id, "--answer-file", "-"}
	app.Getenv = func(k string) string { return inSession[k] }
	app.Stdin = strings.NewReader("approve\n")
	if code := app.Run(answer); code != 1 {
		t.Fatalf("a session answered the approval: exit %d", code)
	}
	if code, _, stderr := run(inSession, "", "rollout", "--apply", "--approve", digest); code != 1 || !strings.Contains(stderr, "needs the Guildmaster's answer") {
		t.Fatalf("approve before the answer: exit %d, stderr %s", code, stderr)
	}
	app.Getenv = nil
	app.Stdin = strings.NewReader("approve\n")
	if code := app.Run(answer); code != 0 {
		t.Fatalf("the Guildmaster's answer: exit %d", code)
	}
	if code, out, stderr := run(inSession, "", "rollout", "--apply", "--approve", digest); code != 0 || !strings.Contains(out, "workstation/tools: applied") {
		t.Fatalf("approved apply in a session: exit %d\n%s\n%s", code, out, stderr)
	}
	// steward status shows the approval and the copy's last rollout.
	code, out, stderr = run(nil, "", "status", "--copy", copyDir)
	if code != 0 {
		t.Fatalf("status: exit %d\n%s\n%s", code, out, stderr)
	}
	var st stewardStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Approvals) != 1 || st.Approvals[0].State != "approved" || st.Approvals[0].Plan != digest {
		t.Errorf("approvals = %+v", st.Approvals)
	}
	if len(st.Roster) != 1 || len(st.Roster[0].Directors) != 1 {
		t.Fatalf("roster = %+v", st.Roster)
	}
	if r := st.Roster[0].Directors[0].LastRollout; r == nil || r.PlanDigest != digest || r.Status != "applied" {
		t.Errorf("last rollout = %+v", r)
	}
}

// TestStewardSessionApproveRefusesADecline: an answer of decline approves nothing.
func TestStewardSessionApproveRefusesADecline(t *testing.T) {
	app, run, copyDir, plan, digest := stewardRolloutCopy(t)
	if code, _, stderr := run(nil, "", "approval", "request", "--plan", plan, "--copy", copyDir); code != 0 {
		t.Fatal(stderr)
	}
	app.Getenv = nil
	app.Stdin = strings.NewReader("decline\nnot this week\n")
	if code := app.Run([]string{"decide", "answer", "--copy", copyDir, "--repo", stewardRepository, "--task", stewardRolloutsTask,
		"--decision", approvalDecisionID(digest), "--answer-file", "-"}); code != 0 {
		t.Fatal("decline")
	}
	if code, _, stderr := run(map[string]string{session.NumberEnv: "4"}, "", "rollout", "--apply", "--approve", digest); code != 1 || !strings.Contains(stderr, "needs the Guildmaster's answer") {
		t.Fatalf("approve after a decline: exit %d, stderr %s", code, stderr)
	}
}

// TestStewardStatusWithoutASession: steward status reads the roster and approvals with no session yet; with no ssh
// to read a copy's mode, that is the copy's problem and the command still succeeds.
func TestStewardStatusWithoutASession(t *testing.T) {
	dir := stewardCopy(t, "/exit")
	code, out, stderr := runCLI(t, "steward", "status", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	var st stewardStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.Session != nil || len(st.Roster) != 1 || st.Roster[0].Label != "workstation" || st.Roster[0].Directors[0].LastRollout != nil ||
		len(st.Problems) != 1 || !strings.HasPrefix(st.Problems[0], "workstation/tools: mode: ssh is needed") {
		t.Errorf("status = %+v", st)
	}
}
