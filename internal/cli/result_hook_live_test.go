package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// claudeNudgeLive opens TestLiveClaudeNudged, which makes a real Claude Code session; its runner's grant may allow
// `go test` but not an environment assignment before it.
var claudeNudgeLive = flag.Bool("claude-live", false, "run TestLiveClaudeNudged, which runs one real Claude Code session")

// claudeNudgeProgram is the real Claude Code TestLiveClaudeNudged runs. The test binary's claude guard
// (testenv.InstallClaudeGuard) shadows every `claude` on PATH, so the test needs the program's absolute path.
var claudeNudgeProgram = flag.String("claude-program", "", "absolute path of the real claude for TestLiveClaudeNudged")

// livePrompt tells the model to stop at first without a result, then to obey the steering message Guildhall sends.
const livePrompt = `# Guildhall stop hook check

This attempt checks that Guildhall reminds an Artisan that stops without a result.
Do not write, check or submit any result yet, and run no command: reply with the single word "stopping" and end your turn now.
Only after Guildhall sends you a steering message, follow it exactly: acknowledge it, then write the result to the draft file and submit it, with status "succeeded", summary "nudged" and one output, {"path": "spec.md", "description": "spec"}.
The file spec.md is already written in the working directory; do not change it.
`

// TestLiveClaudeNudged is the submit nudge's live check (docs/evidence/claude-stop-hook-nudge-2026-11-05.txt): one
// real `claude --print --output-format stream-json` Artisan session through the wrapper, in a copy whose claude
// profile grants the Guildhall rules, with this test binary as guildhall on PATH for the Stop hook. The model is told
// to stop without a result; the Stop hook (claude.ResultHookCommand) sends the one nudge and blocks, the session
// goes on with the nudge as its reason, acknowledges it and submits, and the wrapper publishes the result. Run it by
// hand: go test ./internal/cli -run TestLiveClaudeNudged -v -count=1 -claude-live -claude-program "$(command -v claude)"
func TestLiveClaudeNudged(t *testing.T) {
	if !*claudeNudgeLive {
		t.Skip("set -claude-live and -claude-program to run a real Claude Code session; it makes model calls")
	}
	if !filepath.IsAbs(*claudeNudgeProgram) {
		t.Fatalf("-claude-program %q must be the absolute path of the real claude", *claudeNudgeProgram)
	}
	c := newResultCopy(t)
	config := filepath.Join(c.dir, "config.toml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "[profile.demo]", "[profile.live]\nadapter = \"claude\"\n\n[profile.demo]", 1))
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, release.BinaryName(runtime.GOOS))); err != nil {
		t.Fatal(err)
	}
	t.Setenv(roleEnv, "guildhall")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(artisan.AttemptEnv, c.key.String())
	t.Chdir(c.work)
	attempt := c.key.Dir(filepath.Join(c.dir, "state"))
	if err := os.MkdirAll(attempt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(c.work, "spec.md"), goodSpec)
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	writeFile(t, promptFile, livePrompt)
	a := claude.Args{Identity: adapter.Identity{TaskID: "t1", AttemptID: "plan-1"}, PromptFile: promptFile,
		ResultFile: filepath.Join(attempt, artisan.ResultFile), Claude: *claudeNudgeProgram, Copy: c.dir, Profile: "live", Repository: "app"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out lockedBuffer
	runErr := claude.Run(ctx, a, &out, &out)
	t.Logf("wrapper output:\n%s", out.String())
	if settings, err := os.ReadFile(claude.ArtisanSettingsPath(a.ResultFile)); err != nil || !strings.Contains(string(settings), `"command": "`+claude.ResultHookCommand+`"`) {
		t.Errorf("settings %s, %v; want the Stop hook %q", settings, err, claude.ResultHookCommand)
	}
	m, ok := c.messagesTo(t)[nudgeID]
	t.Logf("nudge %s: present %v, sender %s, type %s, acknowledged %v", nudgeID, ok, m.Sender, m.Type, m.Acknowledged)
	if !ok || m.Sender != store.SenderGuildhall || m.Type != store.MessageNudge {
		t.Errorf("no nudge from guildhall; the Stop hook did not send one")
	}
	if runErr != nil {
		t.Fatalf("claude.Run = %v; want the nudged session to submit", runErr)
	}
	result, err := os.ReadFile(a.ResultFile)
	if err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ParseResult(result, a.Identity)
	t.Logf("published result: %+v, %v", r, err)
	if err != nil || r.Status != adapter.StatusSucceeded || !m.Acknowledged {
		t.Errorf("want a published succeeded result after the nudge was acknowledged")
	}
}
