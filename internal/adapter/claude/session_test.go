package claude_test

import (
	"encoding/json"
	"flag"
	"os"
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
		FirstMessage: "You are starting up.", Model: "opus", Effort: "high", Env: []string{"A=1"}}
	cmd, err := claude.Adapter{}.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--append-system-prompt-file", spec.RolePrompt, "--add-dir", c, "--model", "opus", "You are starting up."}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.Dir || !slices.Equal(cmd.Env, []string{"GUILDHALL_COPY=" + c, "A=1"}) {
		t.Errorf("command = %+v, want %q in %s", cmd, want, spec.Dir)
	}
	spec.Model = ""
	named := filepath.Join(c, "claude")
	if cmd, err := (claude.Adapter{Claude: named}).Session(spec); err != nil || !slices.Equal(cmd.Argv, []string{named, "--append-system-prompt-file", spec.RolePrompt, "--add-dir", c, "You are starting up."}) {
		t.Errorf("named claude, no model: %+v, %v", cmd, err)
	}
	if cmd, err := (claude.Adapter{Claude: "bin/claude"}).Session(spec); err == nil || len(cmd.Argv) > 0 {
		t.Errorf("relative claude: %+v, %v", cmd, err)
	}
	a := claude.Adapter{}
	if a.Program() != "claude" || a.SkillsDir() != ".claude/skills" || !a.TurnEndHook() {
		t.Errorf("Program, SkillsDir, TurnEndHook = %q, %q, %v; want the Stop hook", a.Program(), a.SkillsDir(), a.TurnEndHook())
	}
}

// TestSessionSettingsGolden pins the session's Claude Code settings: guildhall and read-only git allowed, no
// edit of the copy's config.toml or managed.toml, the UserPromptSubmit hook running guildhall director
// turn-started, the Stop and StopFailure hooks running guildhall director turn-ended and the status line running
// guildhall director context-report.
func TestSessionSettingsGolden(t *testing.T) {
	files := claude.Adapter{}.SessionFiles("/home/g/copy")
	if len(files) != 1 {
		t.Fatalf("SessionFiles = %v, want only %s", files, claude.SettingsFile)
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
// (StopFailure).
func TestSessionSettingsSignalEveryTurn(t *testing.T) {
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(claude.Settings("/home/g/copy"), &settings); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"UserPromptSubmit": claude.TurnStartCommand, "Stop": claude.TurnEndCommand, "StopFailure": claude.TurnEndCommand}
	if len(settings.Hooks) != len(want) {
		t.Errorf("hook events = %v, want %v", settings.Hooks, want)
	}
	for event, command := range want {
		m := settings.Hooks[event]
		if len(m) != 1 || len(m[0].Hooks) != 1 || m[0].Hooks[0].Type != "command" || m[0].Hooks[0].Command != command {
			t.Errorf("%s hooks = %+v, want the one command %q", event, m, command)
		}
	}
	if claude.TurnStartCommand != "guildhall director turn-started" || claude.TurnEndCommand != "guildhall director turn-ended" {
		t.Errorf("hook commands = %q, %q", claude.TurnStartCommand, claude.TurnEndCommand)
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
