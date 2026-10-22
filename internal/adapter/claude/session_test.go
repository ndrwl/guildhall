package claude_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
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
