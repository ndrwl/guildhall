package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
)

// hookOutput is the Claude Code PostToolUse hook document messages hook prints.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// TestSteeringHookCommandIsTheCLIs: the command the Claude Artisan's settings register is messages hook.
func TestSteeringHookCommandIsTheCLIs(t *testing.T) {
	if want := "guildhall messages " + steeringHookCommand; claude.SteeringHookCommand != want {
		t.Fatalf("claude.SteeringHookCommand = %q, want %q", claude.SteeringHookCommand, want)
	}
}

// TestSteeringHook: inside an attempt with an unacknowledged steering message, messages hook prints one PostToolUse
// hook document whose additional context holds the message in its frame, with nothing on stderr; once the message
// is acknowledged, and outside any attempt, it prints nothing. It always exits 0.
func TestSteeringHook(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	if code, out, stderr := c.run(t, env, "messages", "hook"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("hook with no message: exit %d, %q, %q; want nothing", code, out, stderr)
	}
	c.steer(t, "steer-1", "Rebase first.\x1b[2J")
	code, out, stderr := c.run(t, env, "messages", "hook")
	if code != 0 || stderr != "" {
		t.Fatalf("hook: exit %d, stderr %q", code, stderr)
	}
	var doc hookOutput
	if err := json.Unmarshal([]byte(out), &doc); err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("hook stdout %q is not one JSON document: %v", out, err)
	}
	ctx := doc.HookSpecificOutput.AdditionalContext
	if doc.HookSpecificOutput.HookEventName != "PostToolUse" ||
		!strings.HasPrefix(ctx, "Guildhall: 1 unacknowledged steering message(s) to this attempt.") ||
		!strings.Contains(ctx, "==== guildhall: steering message steer-1 from director (note) ====\n| Rebase first.[2J\n") ||
		!strings.Contains(ctx, "guildhall messages ack steer-1") || strings.ContainsRune(ctx, '\x1b') {
		t.Fatalf("hook document = %+v", doc)
	}
	if code, _, stderr := c.run(t, env, "messages", "ack", "steer-1"); code != 0 {
		t.Fatalf("ack: exit %d, %s", code, stderr)
	}
	if code, out, stderr := c.run(t, env, "messages", "hook"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("hook after the ack: exit %d, %q, %q; want nothing", code, out, stderr)
	}
	if code, out, stderr := c.run(t, "", "messages", "hook"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("hook outside an attempt: exit %d, %q, %q; want nothing", code, out, stderr)
	}
}

// TestBrokenSteeringHookChangesNothing: a hook that cannot read the store, run outside the copy, with a malformed
// attempt or with stray arguments, exits 0 with nothing on stdout, so Claude Code adds nothing and blocks nothing;
// the result is still refused at check and accepted after the ack, as with no hook at all.
func TestBrokenSteeringHookChangesNothing(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	draft := c.draft(t, goodSpec)
	c.steer(t, "steer-1", "Rebase first.")
	elsewhere := resultCopy{dir: c.dir, work: t.TempDir(), key: c.key}
	for name, run := range map[string]func() (int, string, string){
		"outside the copy":  func() (int, string, string) { return elsewhere.run(t, env, "messages", "hook") },
		"malformed attempt": func() (int, string, string) { return c.run(t, "not-an-attempt", "messages", "hook") },
		"stray arguments":   func() (int, string, string) { return elsewhere.run(t, env, "messages", "hook", "--store", "x") },
	} {
		code, out, stderr := run()
		if code != 0 || out != "" || !strings.HasPrefix(stderr, "guildhall: warning: ") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 0, no output and a warning", name, code, out, stderr)
		}
	}
	if code, out, _ := c.run(t, env, "result", "check", draft); code != 1 || !strings.Contains(out, "unacknowledged steering message to this attempt: steer-1") {
		t.Fatalf("check with a broken hook: exit %d, %q; want the steering refusal", code, out)
	}
	if code, _, stderr := c.run(t, env, "messages", "ack", "steer-1"); code != 0 {
		t.Fatalf("ack: exit %d, %s", code, stderr)
	}
	if code, out, _ := c.run(t, env, "result", "check", draft); code != 0 || out != "ok\n" {
		t.Fatalf("check after the ack: exit %d, %q", code, out)
	}
}
