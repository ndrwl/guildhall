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
	spec := adapter.SessionSpec{Dir: "/c/director/session-2", Copy: "/c", RolePrompt: "/c/director/session-2/role.md",
		FirstMessage: "You are starting up.", Model: "opus", Effort: "high", Env: []string{"A=1"}}
	cmd, err := claude.Adapter{}.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--append-system-prompt-file", spec.RolePrompt, "--add-dir", "/c", "--model", "opus", "You are starting up."}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.Dir || !slices.Equal(cmd.Env, []string{"GUILDHALL_COPY=/c", "A=1"}) {
		t.Errorf("command = %+v, want %q in %s", cmd, want, spec.Dir)
	}
	spec.Model = ""
	if cmd, err := (claude.Adapter{Claude: "/opt/claude"}).Session(spec); err != nil || !slices.Equal(cmd.Argv, []string{"/opt/claude", "--append-system-prompt-file", spec.RolePrompt, "--add-dir", "/c", "You are starting up."}) {
		t.Errorf("named claude, no model: %+v, %v", cmd, err)
	}
	if cmd, err := (claude.Adapter{Claude: "bin/claude"}).Session(spec); err == nil || len(cmd.Argv) > 0 {
		t.Errorf("relative claude: %+v, %v", cmd, err)
	}
	a := claude.Adapter{}
	if a.Program() != "claude" || a.SkillsDir() != ".claude/skills" {
		t.Errorf("Program, SkillsDir = %q, %q", a.Program(), a.SkillsDir())
	}
}

// TestSessionSettingsGolden pins the session's Claude Code settings: guildhall and read-only git allowed, and
// no edit of the copy's config.toml or managed.toml.
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
			glob := strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*")
			if regexp.MustCompile("^" + glob + "$").MatchString(command) {
				return true
			}
		}
		return false
	}
	return !matches(s.Permissions.Deny) && matches(s.Permissions.Allow)
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
