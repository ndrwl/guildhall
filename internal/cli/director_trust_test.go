package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
)

// A saved approval for one stable workspace must cover successive fresh sessions.
// The strict stub exits at its trust gate before consuming the role script otherwise.
func TestDirectorClaudeReusesTrustedWorkspace(t *testing.T) {
	claudetest.Install(t)
	dir := directorCopy(t, "[director]\nadapter = \"claude\"", "/exit")
	stable := filepath.Join(dir, "director", "claude")
	t.Setenv("GUILDHALL_TEST_CLAUDE_TRUSTED_DIR", stable)
	for n := 1; n <= 2; n++ {
		code, out, stderr := runDirectorCmd(t, "start", "--copy", dir)
		if code != 0 {
			t.Fatalf("start %d: exit %d\n%s\n%s", n, code, out, stderr)
		}
		if strings.Contains(out+stderr, "Is this a project") {
			t.Fatal("trust dialog after saved approval")
		}
	}
	if got := sessions(t, dir); len(got) != 2 {
		t.Fatalf("sessions = %+v", got)
	}
	for _, rel := range []string{"session-1/startup.md", "session-2/startup.md", "claude/.claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(dir, "director", filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectorClaudeReloadReusesTrustedWorkspace(t *testing.T) {
	claudetest.Install(t)
	dir, _ := supervisedCopy(t)
	cfg := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(data), `adapter = "fake"`, `adapter = "claude"`, 1))
	t.Setenv("GUILDHALL_TEST_CLAUDE_TRUSTED_DIR", filepath.Join(dir, "director", "claude"))
	done := supervise(t, dir, time.Nanosecond)
	copyRun(t, dir, "director", "handoff", "--note-file", noteFile(t, "Continue from the saved note.\n"))
	copyRun(t, dir, "director", "reload", "--now")
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	holdsOne(t, dir, 2)
	copyRun(t, dir, "director", "stop", "--now")
	copyRun(t, dir, "director", "turn-ended")
	if code := ended(t, done); code != 0 {
		t.Fatalf("supervisor exit %d", code)
	}
}

func TestDirectorClaudePreflightNamesTrust(t *testing.T) {
	claudetest.Install(t)
	dir := directorCopy(t, "[director]\nadapter = \"claude\"", "/exit")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 0 || !strings.Contains(out, "trust:") || !strings.Contains(out, filepath.Join(dir, "director", "claude")) || !strings.Contains(out, "not confirmed") {
		t.Fatalf("preflight: exit %d\n%s\n%s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "director")); !os.IsNotExist(err) {
		t.Fatalf("check wrote workspace: %v", err)
	}
}
