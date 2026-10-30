package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/clerk"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/skills"
)

// sessionScript is a Director role prompt that both session doubles play: the fake Director reads its
// fake-director: lines and the Claude stub its claude-stub: lines.
func sessionScript(steps ...string) string {
	var b strings.Builder
	b.WriteString("# Director\n\n")
	for _, prefix := range []string{"fake-director:", claudetest.Directive} {
		for _, s := range steps {
			b.WriteString(prefix + " " + s + "\n")
		}
	}
	return b.String()
}

// directorCopy is a Director copy over testdata/run with table appended to its config, an empty store, app's
// workflow approved, and baseline/roles/director.md playing steps; guildhall on PATH is this test binary.
func directorCopy(t *testing.T, table string, steps ...string) string {
	t.Helper()
	t.Setenv(roleEnv, "guildhall")
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A role profile the table declares replaces the fixture's fake one.
	for _, role := range []string{"researcher", "planner", "builder", "reviewer", "rebaser", "designer"} {
		if section := "[profile." + role + "]"; strings.Contains(table, section) {
			b = bytes.Replace(b, []byte(section+"\nadapter = \"fake\"\n"), nil, 1)
		}
	}
	if err := os.WriteFile(cfg, append(b, []byte("\n"+table)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	approveRepo(t, filepath.Join(dir, "director.store"), cfg, "app")
	writeFile(t, filepath.Join(dir, "baseline", "roles", "director.md"), sessionScript(steps...))
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "guildhall"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.Link(exe, filepath.Join(bin, name)); err != nil {
		copyFile(t, exe, filepath.Join(bin, name))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// runDirectorCmd runs `guildhall director <args>` in this process as cmd/guildhall assembles it, with no working
// directory to discover a copy from and empty standard input.
func runDirectorCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runDirectorCmdWith(t, nil, args...)
}

// runDirectorCmdWith is runDirectorCmd with the App changed by configure first, when it is not nil.
func runDirectorCmdWith(t *testing.T, configure func(*App), args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	if configure != nil {
		configure(app)
	}
	app.Executable = os.Executable
	app.LookPath = exec.LookPath
	app.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).CombinedOutput()
	}
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	app.Stdin = strings.NewReader("")
	code := app.Run(append([]string{"director"}, args...))
	return code, stdout.String(), stderr.String()
}

func sessions(t *testing.T, dir string) []store.DirectorSession {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{ReadOnly: true, RequireStore: true})
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

func hasTask(t *testing.T, dir, id string) bool {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, err = s.Task("app", id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// TestDirectorStartRunsASession runs a whole session on each runtime: the session is recorded recovering, a
// task new before ready is refused, ready admits the next one, and the session is recorded stopped at exit.
func TestDirectorStartRunsASession(t *testing.T) {
	for _, rt := range []string{"fake", "claude"} {
		t.Run(rt, func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \""+rt+"\"\nmodel = \"opus\"\n",
				"guildhall task new --repo app --title Early t-early",
				"guildhall director ready",
				"guildhall task new --repo app --title Late t-late",
				"/exit")
			if rt == "claude" {
				claudetest.Install(t)
			}
			code, out, stderr := runDirectorCmd(t, "start", "--copy", dir)
			if code != 0 {
				t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, stderr)
			}
			for _, want := range []string{"config: ", "runtime: " + rt, "store: ", "skills: ", "session: 1"} {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q:\n%s", want, out)
				}
			}
			if !strings.Contains(out+stderr, "guildhall director ready") {
				t.Errorf("the refused task new does not name director ready:\n%s\n%s", out, stderr)
			}
			got := sessions(t, dir)
			if len(got) != 1 || got[0].State != store.DirectorStopped || got[0].ReadyAt.IsZero() || got[0].EndedAt.IsZero() ||
				got[0].Identity.PID != os.Getpid() || got[0].Release == "" {
				t.Fatalf("sessions = %+v; want session 1 stopped after ready, run by this process", got)
			}
			if hasTask(t, dir, "t-early") || !hasTask(t, dir, "t-late") {
				t.Errorf("t-early recorded %v, t-late recorded %v; want only t-late", hasTask(t, dir, "t-early"), hasTask(t, dir, "t-late"))
			}
			session := filepath.Join(dir, "director", "session-1")
			for _, f := range []string{render.RoleFile, render.StartupFile, render.ManifestFile} {
				if _, err := os.Stat(filepath.Join(session, f)); err != nil {
					t.Errorf("session file %s: %v", f, err)
				}
			}
			var m render.SessionManifest
			b, err := os.ReadFile(filepath.Join(session, render.ManifestFile))
			if err != nil || json.Unmarshal(b, &m) != nil || m.Session != 1 || m.Role.Root != "copy" || len(m.Skills) == 0 {
				t.Errorf("manifest = %+v, %v", m, err)
			}
			if rt == "claude" {
				settings, err := os.ReadFile(filepath.Join(session, ".claude", "settings.json"))
				if err != nil || !strings.Contains(string(settings), "config.toml") {
					t.Errorf("settings = %s, %v", settings, err)
				}
				if _, err := os.Stat(filepath.Join(session, ".claude", "skills", "guildhall-grill", "SKILL.md")); err != nil {
					t.Errorf("claude skills: %v", err)
				}
			}
		})
	}
}

// TestDirectorStartClaudeWithoutModel delivers the startup turn without a model flag, through the strict stub.
func TestDirectorStartClaudeWithoutModel(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"claude\"\n", "guildhall director ready", "/exit")
	claudetest.Install(t)
	code, out, stderr := runDirectorCmd(t, "start", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	got := sessions(t, dir)
	if len(got) != 1 || got[0].ReadyAt.IsZero() || got[0].State != store.DirectorStopped {
		t.Fatalf("sessions = %+v; want startup turn ready then stopped", got)
	}
	// The one first message is the /version command, naming startup.md by its absolute path.
	workspace := filepath.Join(dir, "director", "claude")
	first, err := os.ReadFile(filepath.Join(workspace, claudetest.FirstMessageFile))
	if want := fmt.Sprintf("/version startup %q", filepath.Join(dir, "director", "session-1", render.StartupFile)); err != nil || string(first) != want {
		t.Errorf("first message = %q, %v; want %q", first, err, want)
	}
	if b, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(claude.VersionCommandFile))); err != nil || !strings.Contains(string(b), "guildhall version --startup") {
		t.Errorf("workspace /version command = %q, %v", b, err)
	}
}

// TestDirectorStartWritesClerk: director start writes the Clerk plugin from the running binary into the Claude
// workspace beside Lanterns, and a second start rewrites a copy that changed.
func TestDirectorStartWritesClerk(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"claude\"\n", "guildhall director ready", "/exit")
	claudetest.Install(t)
	plugin := filepath.Join(dir, "director", "claude", filepath.FromSlash(claude.ClerkPluginDir))
	check := func(when string) {
		t.Helper()
		for name, want := range clerk.Files() {
			if got, err := os.ReadFile(filepath.Join(plugin, filepath.FromSlash(name))); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: %s = %q, %v; want the embedded file", when, name, got, err)
			}
		}
	}
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first start: exit %d\n%s\n%s", code, out, stderr)
	}
	check("first start")
	writeFile(t, filepath.Join(plugin, "commands", "guild.md"), "stale\n")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("second start: exit %d\n%s\n%s", code, out, stderr)
	}
	check("second start")
}

// TestDirectorStartStartupNamesWhatIsInFlight starts a second session over a store with an unread question:
// its startup instructions list the recovering session and the message, and list a task waiting for its checks as the
// supervisor's, with no command for the Director to run. The store holds a retired handoff note and no notes, so
// startup shows neither the old note nor a notes section (D94).
func TestDirectorStartStartupNamesWhatIsInFlight(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first session: exit %d\n%s\n%s", code, out, stderr)
	}
	putRetiredHandoffNote(t, filepath.Join(dir, "director.store"))
	newTask(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app", "Sort", "t-2")
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		return s.SendMessage(store.Message{ID: "q-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-2", Type: "question", Text: "Which parser?"})
	})
	writeNext(t, filepath.Join(dir, "state"), "t-2", `{"wait":"checks","step":"validate"}`+"\n")
	if code, out, stderr := runDirectorCmd(t, "start", "fake", "--copy", dir); code != 0 {
		t.Fatalf("second session: exit %d\n%s\n%s", code, out, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-2", render.StartupFile))
	if err != nil {
		t.Fatal(err)
	}
	startup := string(b)
	for _, want := range []string{"You are starting up.", "recovering", "guildhall director ready", "guildhall messages list director"} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup.md lacks %q:\n%s", want, startup)
		}
	}
	for _, not := range []string{"## Notes", "andoff"} {
		if strings.Contains(startup, not) {
			t.Errorf("startup.md over a retired handoff note and no notes has %q:\n%s", not, startup)
		}
	}
	if !strings.Contains(startup, "checks: step validate waits for the checks of its published head; the session supervisor reads them again every checks_refresh, never more often than every 30 seconds, and wakes you when they finish; the session supervisor advances it") ||
		strings.Contains(startup, "guildhall task start --repo app t-2") {
		t.Errorf("startup.md does not list t-2's checks as the supervisor's:\n%s", startup)
	}
	if got := sessions(t, dir); len(got) != 2 || got[0].State != store.DirectorStopped || got[1].State != store.DirectorStopped {
		t.Errorf("sessions = %+v", got)
	}
}

// TestDirectorStartRequiresGuildhallOnPATH refuses before writing the copy, even though the fake runtime
// resolves to the running executable without PATH. Both start and --check must perform this check.
func TestDirectorStartRequiresGuildhallOnPATH(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(fmt.Sprintf("check=%t", check), func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
			before, err := os.ReadFile(filepath.Join(dir, "director.store"))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			args := []string{"start", "--copy", dir}
			if check {
				args = append(args, "--check")
			}
			code, out, stderr := runDirectorCmd(t, args...)
			if code != 1 || !strings.Contains(out, "guildhall: refused") || !strings.Contains(out+stderr, "PATH") {
				t.Errorf("exit %d\n%s\n%s; want guildhall PATH refusal", code, out, stderr)
			}
			after, err := os.ReadFile(filepath.Join(dir, "director.store"))
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("refusal changed store: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "director")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("refusal wrote session directory: %v", err)
			}
		})
	}
}

func TestDirectorStartCheckRecordsNothing(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	for _, want := range []string{"config: ", "runtime: fake", "guildhall: ", "store: ", "skills: "} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if got := sessions(t, dir); len(got) != 0 {
		t.Errorf("--check recorded %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "director")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--check wrote the session directory: %v", err)
	}
}

// TestDirectorStartCheckWarnsOfDeprecatedSettings: a config that sets the retired quality_audit_sample is admitted,
// and the preflight prints one config warning saying it has no effect and naming its replacement; a config that sets
// nothing prints no warning.
func TestDirectorStartCheckWarnsOfDeprecatedSettings(t *testing.T) {
	const warning = "config: warning: local quality_audit_sample is deprecated and has no effect"
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 0 || strings.Contains(out, "warning") {
		t.Fatalf("a config that sets nothing: exit %d, want 0 and no warning\n%s\n%s", code, out, stderr)
	}
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, bytes.Replace(b, []byte("[local]\n"), []byte("[local]\nquality_audit_sample = \"0\"\n"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 0 || strings.Count(out, warning) != 1 || !strings.Contains(out, "[director] retrospective_after_merges") ||
		!strings.Contains(out, "config: admitted") {
		t.Errorf("exit %d, want 0, the config admitted and one line %q naming its replacement\n%s\n%s", code, warning, out, stderr)
	}
}

// TestDirectorStartCheckNamesArtisanPermissions: the preflight's artisans line names the Director and the config
// file whose profiles grant the Artisans' tools, each claude profile's effective grant by source (the copy's list,
// the shipped default, or a note that a profile of another name gets none, then each repository's additions and the
// rule Guildhall adds), and
// the permissions print mode lacks for a build's checks and commits, without refusing.
func TestDirectorStartCheckNamesArtisanPermissions(t *testing.T) {
	claudetest.Install(t)
	dir := directorCopy(t, "[repository.profiles.reviewer]\nallowed_tools = [\"Bash(go vet:*)\", \"Bash(git log:*)\"]\n\n[director]\nadapter = \"fake\"\n\n"+
		"[profile.builder]\nadapter = \"claude\"\nallowed_tools = [\"Bash(go test:*)\", \"Bash(git commit:*)\"]\n\n[profile.reviewer]\nadapter = \"claude\"\n\n[profile.worker]\nadapter = \"claude\"\n", "/exit")
	code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "artisans: ") {
			line = l
		}
	}
	const guildhall = ", plus from Guildhall Bash(guildhall ci log:*);"
	reviewer := "profile reviewer grants the shipped default " + strings.Join(claude.ShippedDefaultGrants["reviewer"], ", ") + ", plus for repository app Bash(go vet:*)" + guildhall
	for _, want := range []string{"Director alpha", filepath.Join(dir, "config.toml"), "profile builder grants from the copy Bash(go test:*), Bash(git commit:*)" + guildhall,
		reviewer, "profile worker gets no shipped default" + guildhall, "edits only", "Bash", "docs/adapters.md#artisan-permissions",
		"copy/profile/repository reference, never raw rule flags"} {
		if !strings.Contains(line, want) {
			t.Errorf("artisans line %q lacks %q:\n%s", line, want, out)
		}
	}
	if strings.Contains(line, "refused") {
		t.Errorf("artisans line %q refuses", line)
	}
}

// TestDirectorStartRefusesAllowedToolsARuntimeCannotGrant: a profile whose adapter grants no tools refuses the
// preflight's artisans check, naming it, rather than silently granting nothing; a repository grant on such a profile
// is refused too, already as the config loads.
func TestDirectorStartRefusesAllowedToolsARuntimeCannotGrant(t *testing.T) {
	for name, c := range map[string]struct{ grant, check string }{
		"profile":    {"[profile.builder]\nadapter = \"fake\"\nallowed_tools = [\"Bash(go test:*)\"]\n", "artisans: refused"},
		"repository": {"[profile.builder]\nadapter = \"fake\"\n\n[repository.profiles.builder]\nallowed_tools = [\"Bash(go test:*)\"]\n", "config: refused"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
			cfg := filepath.Join(dir, "config.toml")
			b, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// The grant's builder replaces the fixture's.
			writeFile(t, cfg, strings.Replace(string(b), "[profile.builder]\nadapter = \"fake\"\n", "", 1)+"\n"+c.grant)
			code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
			if code != 1 || !strings.Contains(out, c.check) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "builder") || !strings.Contains(stderr, "fake") {
				t.Errorf("exit %d\n%s\n%s; want %q naming profile builder and its adapter", code, out, stderr, c.check)
			}
		})
	}
}

func TestDirectorStartRefusals(t *testing.T) {
	cases := []struct {
		name, table string
		args        []string
		tab         bool
		setup       func(t *testing.T, dir string)
		code        int
		want        string
	}{
		{name: "no adapter", table: "[director]\nmodel = \"opus\"\n", code: 1, want: "the adapters are claude, fake"},
		{name: "no director table", code: 1, want: "the adapters are claude, fake"},
		{name: "unknown runtime", table: "[director]\nadapter = \"fake\"\n", args: []string{"codex"}, code: 1, want: `unknown runtime adapter "codex": the adapters are claude, fake`},
		{name: "managed copy", table: "[director]\nadapter = \"fake\"\n", code: 1, want: "director enrol",
			setup: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "managed.toml"), "") }},
		{name: "inline managed", table: "[director]\nadapter = \"fake\"\n\n[managed.stall_interval]\nvalue = \"5m\"\n", code: 1, want: "[managed]"},
		{name: "no store", table: "[director]\nadapter = \"fake\"\n", code: 1, want: "guildhall store init",
			setup: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "director.store")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "runtime not on PATH", table: "[director]\nadapter = \"claude\"\n", code: 1, want: "claude: not found on PATH",
			setup: func(t *testing.T, dir string) { t.Setenv("PATH", t.TempDir()) }},
		{name: "a tab without a herdr session", table: "[director]\nadapter = \"fake\"\n", tab: true, code: 1, want: "herdr_session"},
		{name: "a pane binding for a tab", table: "[director]\nadapter = \"fake\"\n", tab: true, code: 2, want: "--tab",
			args: []string{"--herdr-session", "lab", "--herdr-workspace", "w1", "--herdr-tab", "w1:t2", "--herdr-pane", "w1:p2"}},
		{name: "foreground is the default, not a flag", table: "[director]\nadapter = \"fake\"\n", args: []string{"--foreground"}, code: 2, want: "foreground"},
		{name: "half a pane binding", table: "[director]\nadapter = \"fake\"\n", args: []string{"--herdr-session", "lab", "--herdr-tab", "w1:t2"}, code: 2, want: "--herdr-pane"},
		{name: "two runtimes", table: "[director]\nadapter = \"fake\"\n", args: []string{"fake", "claude"}, code: 2, want: "one runtime"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := directorCopy(t, c.table, "/exit")
			if c.setup != nil {
				c.setup(t, dir)
			}
			args := []string{"start", "--copy", dir}
			if c.tab {
				args = append(args, "--tab")
			}
			args = append(args, c.args...)
			code, out, stderr := runDirectorCmd(t, args...)
			if code != c.code || !strings.Contains(out+stderr, c.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d naming %q", code, out, stderr, c.code, c.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "director.store")); err == nil {
				if got := sessions(t, dir); len(got) != 0 {
					t.Errorf("refused start recorded %+v", got)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "director")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("refused start wrote the session directory: %v", err)
			}
		})
	}
}

// TestDirectorStartRecordsACrashedRuntimeStopped sees a runtime that exits nonzero end its session stopped
// and the supervisor exit 1.
func TestDirectorStartRecordsACrashedRuntimeStopped(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "exit 3")
	code, out, stderr := runDirectorCmd(t, "start", "--copy", dir)
	if code != 1 || !strings.Contains(stderr, "status 3") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 naming status 3", code, out, stderr)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || !got[0].ReadyAt.IsZero() {
		t.Errorf("sessions = %+v; want session 1 stopped, never ready", got)
	}
}

// recoveringCopy is a Director copy over testdata/run whose newest session is recovering, with task t-1 and no attempt.
func recoveringCopy(t *testing.T) string {
	t.Helper()
	dir := directorCopy(t, "")
	newTask(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app", "Sort", "t-1")
	recoverSession(t, dir)
	return dir
}

// recoverSession records session 1, recovering, as director start does before its runtime runs.
func recoverSession(t *testing.T, dir string) {
	t.Helper()
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		return s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
			Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
	})
}

func withTestStore(t *testing.T, dir string, fn func(*store.Store) (store.Receipt, error)) {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fn(s)
	if err := errors.Join(err, s.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestDirectorReady(t *testing.T) {
	dir := directorCopy(t, "")
	if code, out, stderr := runDirectorCmd(t, "ready", "--copy", dir); code != 1 || !strings.Contains(stderr, "director start") {
		t.Fatalf("no session: exit %d, stdout %q, stderr %q; want exit 1 naming director start", code, out, stderr)
	}
	recoverSession(t, dir)
	for range 2 {
		code, out, stderr := runDirectorCmd(t, "ready", "--copy", dir)
		if code != 0 {
			t.Fatalf("ready: exit %d, %s", code, stderr)
		}
		var d store.DirectorSession
		if err := json.Unmarshal([]byte(out), &d); err != nil || d.Number != 1 || d.State != store.DirectorRunning || d.ReadyAt.IsZero() {
			t.Fatalf("ready printed %q, %v; want session 1 running", out, err)
		}
	}
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		d, err := s.LatestDirectorSession()
		if err != nil {
			return store.Receipt{}, err
		}
		return s.SetDirectorState(d.Number, store.DirectorStopped, d.StateSequence)
	})
	if code, _, stderr := runDirectorCmd(t, "ready", "--copy", dir); code != 1 || !strings.Contains(stderr, "stopped") {
		t.Fatalf("stopped session: exit %d, stderr %q; want exit 1 naming stopped", code, stderr)
	}
	if code, _, stderr := runDirectorCmd(t, "ready", "--copy", dir, "extra"); code != 2 || !strings.Contains(stderr, `unexpected argument "extra"`) {
		t.Fatalf("argument: exit %d, stderr %q", code, stderr)
	}
}

// TestRecoveringSessionStartsNoNewWork refuses task new and task start of a task with no attempt while the
// newest session is recovering, naming director ready, and admits them after it; reads are unaffected.
func TestRecoveringSessionStartsNoNewWork(t *testing.T) {
	dir := recoveringCopy(t)
	storePath, cfg := filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml")
	code, _, stderr := runTask(t, "new", "--store", storePath, "--config", cfg, "--repo", "app", "--title", "Early", "t-2")
	if code != 1 || !strings.Contains(stderr, "guildhall director ready") || hasTask(t, dir, "t-2") {
		t.Fatalf("task new while recovering: exit %d, stderr %q; want exit 1 naming director ready", code, stderr)
	}
	markCopy(t, dir)
	code, out, stderr := runIn(t, dir, "task", "start", "--repo", "app", "t-1")
	if code != 1 || !strings.Contains(stderr, "guildhall director ready") {
		t.Fatalf("task start while recovering: exit %d, stdout %q, stderr %q; want exit 1 naming director ready", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "app", "t-1", "next.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refused task start advanced the task: %v", err)
	}
	if code, _, stderr := runIn(t, dir, "task", "list", "--json"); code != 0 {
		t.Errorf("task list while recovering: exit %d, %s", code, stderr)
	}
	if code, _, stderr := runIn(t, dir, "messages", "list", "director"); code != 0 {
		t.Errorf("messages list while recovering: exit %d, %s", code, stderr)
	}
	if code, _, stderr := runDirectorCmd(t, "ready", "--copy", dir); code != 0 {
		t.Fatalf("ready: exit %d, %s", code, stderr)
	}
	if code, _, stderr := runTask(t, "new", "--store", storePath, "--config", cfg, "--repo", "app", "--title", "Late", "t-2"); code != 0 {
		t.Fatalf("task new after ready: exit %d, %s", code, stderr)
	}
}

// TestDirectorStatusListsARecoveringSessionFirst pins the needs_me item a recovering session adds.
func TestDirectorStatusListsARecoveringSessionFirst(t *testing.T) {
	dir := recoveringCopy(t)
	code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d, %s", code, stderr)
	}
	var st directorStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.NeedsMe) == 0 || st.NeedsMe[0].Kind != "recovering" || st.NeedsMe[0].Command != "guildhall director ready" {
		t.Fatalf("needs_me = %+v; want the recovering session first", st.NeedsMe)
	}
}
