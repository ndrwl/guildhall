package claude_test

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestClaudeSessionContract(t *testing.T) {
	adaptertest.RunSessionContract(t, func(t *testing.T) adaptertest.SessionSubject {
		claudetest.Install(t)
		return adaptertest.SessionSubject{Runtime: claude.Adapter{}, Script: claudetest.Script}
	})
}

func TestSessionCommand(t *testing.T) {
	c := t.TempDir()
	dir := filepath.Join(c, "director", "session-2")
	spec := adapter.SessionSpec{Dir: dir, Copy: c, RolePrompt: filepath.Join(dir, "role.md"),
		FirstMessage: claude.StartupMessage(filepath.Join(dir, "startup.md")), Model: "opus", Effort: "high", Env: []string{"A=1"}}
	cmd, err := claude.Adapter{}.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--append-system-prompt-file", spec.RolePrompt, "--add-dir", c, "--settings", filepath.Join(dir, claude.SettingsFile), "--plugin-dir", filepath.Join(dir, claude.PluginDir), "--model", "opus", "--effort", "high", "--", spec.FirstMessage}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.Dir || !slices.Equal(cmd.Env, []string{"A=1"}) {
		t.Errorf("command = %+v, want %q in %s with only the spec's environment", cmd, want, spec.Dir)
	}
	if !adapter.Under(spec.Copy, cmd.Dir) {
		t.Errorf("session directory %s does not lie under the copy %s", cmd.Dir, spec.Copy)
	}
	spec.Model, spec.Effort = "", ""
	named := filepath.Join(c, "claude")
	if cmd, err := (claude.Adapter{Claude: named}).Session(spec); err != nil || !slices.Equal(cmd.Argv, []string{named, "--append-system-prompt-file", spec.RolePrompt, "--add-dir", c, "--settings", filepath.Join(dir, claude.SettingsFile), "--plugin-dir", filepath.Join(dir, claude.PluginDir), "--", spec.FirstMessage}) {
		t.Errorf("named claude, no model: %+v, %v", cmd, err)
	}
	if cmd, err := (claude.Adapter{Claude: "bin/claude"}).Session(spec); err == nil || len(cmd.Argv) > 0 {
		t.Errorf("relative claude: %+v, %v", cmd, err)
	}
	for _, bad := range []adapter.Settings{{Model: "--bad"}, {Effort: "extreme"}, {Effort: "High"}} {
		spec.Model, spec.Effort = bad.Model, bad.Effort
		cmd, err := (claude.Adapter{}).Session(spec)
		if !errors.Is(err, adapter.ErrUnsupportedSetting) || len(cmd.Argv) != 0 {
			t.Errorf("Session with %+v = %+v, %v", bad, cmd, err)
		}
	}
	a := claude.Adapter{}
	if a.Program() != "claude" || a.SkillsDir() != ".claude/skills" || !a.TurnEndHook() {
		t.Errorf("Program, SkillsDir, TurnEndHook = %q, %q, %v; want the Stop hook", a.Program(), a.SkillsDir(), a.TurnEndHook())
	}
}

// TestVersionCommand pins the session's /version command: its frontmatter allows only guildhall version, it maps
// startup to --startup, asks for the output verbatim and nothing else, then has the model follow the startup file;
// the first message is that command with startup and the quoted absolute path.
func TestVersionCommand(t *testing.T) {
	text := string(claude.Adapter{}.SessionFiles("/home/g/copy")[claude.VersionCommandFile])
	front, body, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !strings.HasPrefix(text, "---\n") || !ok {
		t.Fatalf("%s has no frontmatter:\n%s", claude.VersionCommandFile, text)
	}
	for _, want := range []string{"description: ", "argument-hint: ", "allowed-tools: Bash(guildhall version:*)"} {
		if !strings.Contains(front, "\n"+want) && !strings.HasPrefix(front, want) {
			t.Errorf("frontmatter lacks %q:\n%s", want, front)
		}
	}
	for _, want := range []string{"$ARGUMENTS", "`startup`", "`guildhall version --startup`", "`guildhall version`", "only the command's output, verbatim, and no commentary", "read the file at that exact path and follow it"} {
		if !strings.Contains(body, want) {
			t.Errorf("command body lacks %q:\n%s", want, body)
		}
	}
	if got, want := claude.StartupMessage("/home/g/copy/director/session-3/startup.md"), `/version startup "/home/g/copy/director/session-3/startup.md"`; got != want {
		t.Errorf("StartupMessage = %q, want %q", got, want)
	}
}

// TestSessionStubRequiresMessageSeparator exercises the stub as a real process, independently of Session's argv.
func TestSessionStubRequiresMessageSeparator(t *testing.T) {
	claudetest.Install(t)
	for _, model := range []string{"", "opus"} {
		for _, separator := range []bool{false, true} {
			t.Run(fmt.Sprintf("model=%s/separator=%t", model, separator), func(t *testing.T) {
				dir := t.TempDir()
				role := filepath.Join(dir, "role.md")
				if err := os.WriteFile(role, []byte(claudetest.Script("/exit")), 0o600); err != nil {
					t.Fatal(err)
				}
				for name, data := range (claude.Adapter{}).SessionFiles(dir) {
					path := filepath.Join(dir, name)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"--append-system-prompt-file", role, "--add-dir", dir}
				if model != "" {
					args = append(args, "--model", model)
				}
				if separator {
					args = append(args, "--")
				}
				first := claude.StartupMessage(filepath.Join(dir, "startup.md"))
				args = append(args, first)
				cmd := exec.Command("claude", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if separator {
					if err != nil {
						t.Fatalf("valid argv: %v\n%s", err, out)
					}
					if got, err := os.ReadFile(filepath.Join(dir, claudetest.FirstMessageFile)); err != nil || string(got) != first {
						t.Errorf("recorded first message = %q, %v; want %q", got, err, first)
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "--") {
						t.Fatalf("old argv: %v\n%s; want exit 2 naming separator", err, out)
					}
				}
			})
		}
	}
}

// TestSessionSettingsGolden pins the session's Claude Code settings: guildhall and read-only git allowed, no
// edit of the copy's config.toml or managed.toml, the UserPromptSubmit hook running guildhall director
// turn-started, the Stop and StopFailure hooks running guildhall director turn-ended and the status line running
// guildhall director context-report.
func TestSessionSettingsGolden(t *testing.T) {
	files := claude.Adapter{}.SessionFiles("/home/g/copy")
	if len(files[claude.SkillPolicyFile]) == 0 {
		t.Fatalf("SessionFiles should contain settings and policy receipt")
	}
	for _, name := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js", "hooks/context.cjs", "lib/presentation.mjs"} {
		if len(files[claude.PluginDir+"/"+name]) == 0 {
			t.Errorf("plugin asset missing: %s", name)
		}
	}
	got := files[claude.SettingsFile]
	path := filepath.Join("testdata", "session-settings.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("settings differ from %s:\n%s", path, got)
	}
	if !json.Valid(got) {
		t.Error("settings are not JSON")
	}
}

// TestSessionSettingsWriteAWindowsCopyInPOSIXForm sees a drive-letter copy denied in the form Claude Code
// matches Windows paths in.
func TestSessionSettingsWriteAWindowsCopyInPOSIXForm(t *testing.T) {
	got := string(claude.Settings(`C:/Users/g/copy`))
	if !strings.Contains(got, `"Edit(//c/Users/g/copy/config.toml)"`) || !strings.Contains(got, `"Edit(//c/Users/g/copy/managed.toml)"`) {
		t.Errorf("settings for a Windows copy:\n%s", got)
	}
}

// allowed is whether Claude Code's settings let Bash run command without asking: a deny rule wins over an
// allow rule, and * in a rule matches any run of characters.
func allowed(t *testing.T, settings []byte, command string) bool {
	t.Helper()
	var s struct {
		Permissions struct{ Allow, Deny []string }
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		t.Fatal(err)
	}
	matches := func(rules []string) bool {
		for _, r := range rules {
			pattern, ok := strings.CutPrefix(r, "Bash(")
			if !ok {
				continue
			}
			pattern = strings.TrimSuffix(pattern, ")")
			if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
				if command == prefix || strings.HasPrefix(command, prefix+" ") {
					return true
				}
				continue
			}
			glob := strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*")
			if regexp.MustCompile("^" + glob + "$").MatchString(command) {
				return true
			}
		}
		return false
	}
	return !matches(s.Permissions.Deny) && matches(s.Permissions.Allow)
}

func TestSessionSettingsDenyInternalLaunches(t *testing.T) {
	settings := claude.Adapter{}.SessionFiles("/home/g/copy")[claude.SettingsFile]
	for _, command := range []string{
		"guildhall claude-artisan", "guildhall fake-artisan",
		"guildhall fake-director", "guildhall artisan start",
	} {
		for _, suffix := range []string{"", " --help", " --copy /other/director --profile builder", " --task t --attempt a --prompt /p --result /r --copy /other/director --profile builder"} {
			if allowed(t, settings, command+suffix) {
				t.Errorf("internal launch allowed: %s", command+suffix)
			}
		}
	}
	for _, command := range []string{
		"guildhall", "guildhall task new", "guildhall task start --repo app t",
		"guildhall step done --repo app --task t", "guildhall step retry",
		"guildhall step finish", "guildhall decide answer", "guildhall messages list",
		"guildhall artisan stop", "guildhall artisan status", "guildhall artisan resume",
		claude.TurnStartCommand, claude.TurnEndCommand,
	} {
		if !allowed(t, settings, command) {
			t.Errorf("Director command denied: %s", command)
		}
	}
}

// TestSessionSettingsKeepGitReadOnly sees read-only git allowed and git writing a file or changing a remote not.
func TestSessionSettingsKeepGitReadOnly(t *testing.T) {
	settings := claude.Settings("/home/g/copy")
	for command, want := range map[string]bool{
		"git diff HEAD~1":                            true,
		"git log --oneline -5":                       true,
		"git ls-files -o":                            true,
		"git remote -v":                              true,
		"guildhall director status":                  true,
		"git diff --output=../../config.toml HEAD~1": false,
		"git log --output ../../managed.toml":        false,
		"git show --output=/home/g/copy/config.toml": false,
		"git remote -v add evil git@example.com:x":   false,
		"git push": false,
	} {
		if got := allowed(t, settings, command); got != want {
			t.Errorf("%q allowed = %v, want %v", command, got, want)
		}
	}
}

// TestSessionSettingsSignalEveryTurn reads the settings as Claude Code does: a prompt runs the turn-start
// command, and a turn's end runs the turn-end command whether it ends normally (Stop) or in an API error
// (StopFailure); the session's start, every prompt and every normal turn end also arm the async rewake waiter
// with the 24-hour timeout, so an idle Director always has one (docs/decisions/director-wake-hook.md).
func TestSessionSettingsSignalEveryTurn(t *testing.T) {
	type hook struct {
		Type        string `json:"type"`
		Command     string `json:"command"`
		Async       bool   `json:"async"`
		AsyncRewake bool   `json:"asyncRewake"`
		Timeout     int    `json:"timeout"`
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []hook `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(claude.Settings("/home/g/copy"), &settings); err != nil {
		t.Fatal(err)
	}
	waiter := hook{Type: "command", Command: claude.WaitWakeCommand + " --copy '/home/g/copy'", Async: true, AsyncRewake: true, Timeout: 86400} // timing:allow the hook's timeout in seconds, a settings value, not a test wait.
	want := map[string][]hook{
		"SessionStart":     {waiter},
		"Notification":     {waiter},
		"UserPromptSubmit": {{Type: "command", Command: claude.TurnStartCommand + " --copy '/home/g/copy'"}, waiter},
		"Stop":             {{Type: "command", Command: claude.TurnEndCommand + " --copy '/home/g/copy'"}, waiter},
		"StopFailure":      {{Type: "command", Command: claude.TurnEndCommand + " --copy '/home/g/copy'"}},
	}
	if len(settings.Hooks) != len(want) {
		t.Errorf("hook events = %v, want %v", settings.Hooks, want)
	}
	for event, hooks := range want {
		m := settings.Hooks[event]
		if len(m) != 1 || !slices.Equal(m[0].Hooks, hooks) {
			t.Errorf("%s hooks = %+v, want %+v", event, m, hooks)
		}
	}
	if claude.WaitWakeCommand != "guildhall director wait-wake" {
		t.Errorf("waiter command = %q", claude.WaitWakeCommand)
	}
	if claude.TurnStartCommand != "guildhall director turn-started" || claude.TurnEndCommand != "guildhall director turn-ended" {
		t.Errorf("hook commands = %q, %q", claude.TurnStartCommand, claude.TurnEndCommand)
	}
}

// TestSessionSettingsNameTheCopy sees every hook and the status line name the copy single-quoted for a POSIX
// shell, so they do not depend on the directory Claude Code runs them in, even for a copy whose path has a space
// and a quote in it; the shell reads the quoted copy back as the one argument after --copy.
func TestSessionSettingsNameTheCopy(t *testing.T) {
	copyDir := "/home/g/my copy's"
	quoted := ` --copy '/home/g/my copy'\''s'`
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(claude.Settings(copyDir), &settings); err != nil {
		t.Fatal(err)
	}
	commands := []string{settings.StatusLine.Command}
	for _, m := range settings.Hooks {
		for _, h := range m[0].Hooks {
			commands = append(commands, h.Command)
		}
	}
	for _, c := range commands {
		if !strings.Contains(c, quoted) {
			t.Errorf("command %q does not name the copy as %q", c, quoted)
		}
	}
	if want := claude.ContextReportCommand + quoted + " --lanterns"; settings.StatusLine.Command != want {
		t.Errorf("status line = %q, want %q", settings.StatusLine.Command, want)
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		out, err := exec.Command(sh, "-c", `printf '%s\n' `+strings.TrimPrefix(claude.CopyCommand("", copyDir), " --copy ")).Output()
		if err != nil || string(out) != copyDir+"\n" {
			t.Errorf("sh reads the quoted copy as %q, %v; want %q", out, err, copyDir)
		}
	}
}

// TestSessionFilesNameTheCalmPreference sees calm.json beside the plugin name the copy's Calm preference.
func TestSessionFilesNameTheCalmPreference(t *testing.T) {
	copyDir := filepath.Join(t.TempDir(), "copy")
	var calm struct {
		Preference string `json:"preference"`
	}
	if err := json.Unmarshal(claude.Adapter{}.SessionFiles(copyDir)[claude.CalmFile], &calm); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(copyDir, "director", "lanterns-calm"); calm.Preference != want {
		t.Errorf("calm.json preference = %q, want %q", calm.Preference, want)
	}
	if claude.CalmFile != ".claude/plugins/lanterns/calm.json" {
		t.Errorf("CalmFile = %q", claude.CalmFile)
	}
}

// TestParseStatusLine reads the used context percent from recorded status-line input, reporting none before the
// first message and refusing input that is not a JSON object.
func TestParseStatusLine(t *testing.T) {
	for file, want := range map[string]struct {
		percent int
		ok      bool
	}{"statusline.json": {72, true}, "statusline-fresh.json": {0, false}} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if p, ok, err := claude.ParseStatusLine(b); err != nil || p != want.percent || ok != want.ok {
			t.Errorf("%s: ParseStatusLine = %d, %v, %v; want %d, %v", file, p, ok, err, want.percent, want.ok)
		}
	}
	for in, want := range map[string]int{
		`{"context_window":{"used_percentage":0}}`:     0,
		`{"context_window":{"used_percentage":49.5}}`:  50,
		`{"context_window":{"used_percentage":100.4}}`: 100,
		`{"context_window":{"used_percentage":130}}`:   100,
		`{"context_window":{"used_percentage":-2}}`:    0,
	} {
		if p, ok, err := claude.ParseStatusLine([]byte(in)); err != nil || !ok || p != want {
			t.Errorf("%s: ParseStatusLine = %d, %v, %v; want %d", in, p, ok, err, want)
		}
	}
	for _, in := range []string{`{}`, `{"context_window":null}`, `{"context_window":{}}`} {
		if _, ok, err := claude.ParseStatusLine([]byte(in)); err != nil || ok {
			t.Errorf("%s: ok %v, err %v; want no percent and no error", in, ok, err)
		}
	}
	for _, in := range []string{``, `[]`, `"text"`, `{"context_window":{"used_percentage":"high"}}`} {
		if _, _, err := claude.ParseStatusLine([]byte(in)); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}
