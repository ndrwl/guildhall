package cli

import (
	"encoding/json"
	"fmt"
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
	writeFile(t, cfg, strings.Replace(string(data), "[director]\nadapter = \"fake\"", "[director]\nadapter = \"claude\"", 1))
	t.Setenv("GUILDHALL_TEST_CLAUDE_TRUSTED_DIR", filepath.Join(dir, "director", "claude"))
	done := supervise(t, dir, time.Nanosecond)
	updated := strings.Replace(string(data), "[director]\nadapter = \"fake\"", "[director]\nadapter = \"claude\"", 1)
	updated = strings.Replace(updated, "[local]\n", "[local]\nclaude_skills_director = '{\"preset\":\"director-v1\",\"account_sync\":true}'\n", 1)
	writeFile(t, cfg, updated)
	copyRun(t, dir, "director", "handoff", "--note-file", noteFile(t, "Continue from the saved note.\n"))
	copyRun(t, dir, "director", "reload", "--now")
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	for n, wantSync := range []bool{false, true} {
		path := filepath.Join(dir, "director", "session-"+fmt.Sprint(n+1), ".claude", "settings.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			Sync bool `json:"syncClaudeAiSkills"`
		}
		if err := json.Unmarshal(b, &settings); err != nil {
			t.Fatal(err)
		}
		if settings.Sync != wantSync {
			t.Fatalf("session %d account sync %t, want %t", n+1, settings.Sync, wantSync)
		}
	}
	holdsOne(t, dir, 2)
	copyRun(t, dir, "director", "stop", "--now")
	copyRun(t, dir, "director", "turn-ended")
	if code := ended(t, done); code != 0 {
		t.Fatalf("supervisor exit %d", code)
	}
}
