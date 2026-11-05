package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/grant"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// nudgeID is the nudge the result hook sends the fixture's plan attempt 1.
const nudgeID = "t1.plan-1.nudge.submit"

// hookDecision is the Claude Code Stop hook document result hook prints.
type hookDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// hook runs guildhall result hook in the working directory with GUILDHALL_ATTEMPT env and stdin as its hook input.
func (c resultCopy) hook(t *testing.T, env, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return c.work, nil }
	app.Getenv = func(name string) string {
		if name == artisan.AttemptEnv {
			return env
		}
		return ""
	}
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"result", "hook"}, args...))
	return code, stdout.String(), stderr.String()
}

// messagesTo returns every message to the fixture's attempt, acknowledged or not, by id.
func (c resultCopy) messagesTo(t *testing.T) map[string]store.Message {
	t.Helper()
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	out := map[string]store.Message{}
	for _, id := range []string{nudgeID} {
		if m, err := s.Message(id); err == nil {
			out[id] = m
		}
	}
	msgs, _, err := s.Inbox(attemptRecipient(c.key), 0, store.MaxInboxLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		out[m.ID] = m
	}
	return out
}

// TestResultHookNudgesOnce: with no draft, result hook sends the attempt one nudge from guildhall and prints a block
// decision whose reason frames it; run again, or with stop_hook_active, it prints nothing and sends nothing more. A
// submission is then refused for the unacknowledged nudge without using a resubmission, and accepted after the ack.
func TestResultHookNudgesOnce(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	code, out, stderr := c.hook(t, env, `{"hook_event_name":"Stop","stop_hook_active":false}`)
	if code != 0 || stderr != "" {
		t.Fatalf("hook: exit %d, stderr %q", code, stderr)
	}
	var doc hookDecision
	if err := json.Unmarshal([]byte(out), &doc); err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("hook stdout %q is not one JSON document: %v", out, err)
	}
	draft := filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.ModelDir, artisan.DraftFile)
	if doc.Decision != "block" ||
		!strings.Contains(doc.Reason, "==== guildhall: steering message "+nudgeID+" from guildhall (nudge) ====\n") ||
		!strings.Contains(doc.Reason, "| 1. Acknowledge this message first: guildhall messages ack "+nudgeID) ||
		!strings.Contains(doc.Reason, "guildhall result submit "+draft) ||
		!strings.Contains(doc.Reason, "status failed") {
		t.Fatalf("hook decision = %+v", doc)
	}
	msgs := c.messagesTo(t)
	m, ok := msgs[nudgeID]
	if len(msgs) != 1 || !ok || m.Type != store.MessageNudge || m.Sender != store.SenderGuildhall || m.Acknowledged ||
		len(m.Text) > store.MaxMessageTextBytes {
		t.Fatalf("messages after the hook = %+v", msgs)
	}
	before := storeBytes(t, c.dir)
	for _, stdin := range []string{"", `{"stop_hook_active":true}`} {
		if code, out, stderr := c.hook(t, env, stdin); code != 0 || out != "" || stderr != "" {
			t.Errorf("hook again with %q: exit %d, %q, %q; want nothing", stdin, code, out, stderr)
		}
	}
	if storeBytes(t, c.dir) != before {
		t.Error("a second hook changed the store")
	}
	path := c.draft(t, goodSpec)
	code, out, _ = c.run(t, env, "result", "submit", path)
	if code != 1 || !strings.Contains(out, "unacknowledged steering message to this attempt: "+nudgeID) ||
		!strings.HasSuffix(out, "0 of 2 resubmissions used, none for this; acknowledge the messages and submit again\n") {
		t.Fatalf("submit before the ack: exit %d, %q", code, out)
	}
	if code, _, stderr := c.run(t, env, "messages", "ack", nudgeID); code != 0 {
		t.Fatalf("ack: exit %d, %s", code, stderr)
	}
	if code, out, stderr := c.run(t, env, "result", "submit", path); code != 0 || out != "ok\n" {
		t.Fatalf("submit after the ack: exit %d, %q, %s", code, out, stderr)
	}
	if code, out, stderr := c.hook(t, env, ""); code != 0 || out != "" || stderr != "" {
		t.Errorf("hook after a submission: exit %d, %q, %q; want nothing", code, out, stderr)
	}
}

// TestResultHookStopHookActive: a stop already continuing because of a stop hook is not nudged.
func TestResultHookStopHookActive(t *testing.T) {
	c := newResultCopy(t)
	if code, out, stderr := c.hook(t, c.key.String(), `{"stop_hook_active":true}`); code != 0 || out != "" || stderr != "" {
		t.Fatalf("hook: exit %d, %q, %q; want nothing", code, out, stderr)
	}
	if msgs := c.messagesTo(t); len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none", msgs)
	}
}

// TestResultHookNudgesMalformedDraft: a draft that is not a result for the attempt is no result: the hook nudges.
func TestResultHookNudgesMalformedDraft(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":     `{"schema": "guildhall.artisan-result", "version": 1, "task_id": `,
		"other attempt": `{"schema":"guildhall.artisan-result","version":1,"task_id":"t1","attempt_id":"plan-2","status":"failed","summary":"x","outputs":[]}`,
		"empty":         "",
	} {
		t.Run(name, func(t *testing.T) {
			c := newResultCopy(t)
			if err := os.WriteFile(c.draftFile(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := c.hook(t, c.key.String(), "")
			var doc hookDecision
			if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &doc) != nil || doc.Decision != "block" {
				t.Fatalf("hook: exit %d, %q, %q; want a block", code, out, stderr)
			}
			if msgs := c.messagesTo(t); len(msgs) != 1 {
				t.Fatalf("messages = %+v, want the nudge", msgs)
			}
		})
	}
}

// TestResultHookLeavesAValidDraft: an accepted submission, a valid result written directly and the 3rd rejection's
// draft each count as the attempt's result, so the hook prints nothing and does not write the store.
func TestResultHookLeavesAValidDraft(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, c resultCopy){
		"submitted": func(t *testing.T, c resultCopy) {
			if code, out, stderr := c.run(t, c.key.String(), "result", "submit", c.draft(t, goodSpec)); code != 0 {
				t.Fatalf("submit: exit %d, %q, %s", code, out, stderr)
			}
		},
		"written directly": func(t *testing.T, c resultCopy) {
			data, err := os.ReadFile(c.draft(t, goodSpec))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.draftFile(), data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"3rd rejection": func(t *testing.T, c resultCopy) {
			path := c.draft(t, preambleSpec)
			for range Resubmissions + 1 {
				if code, _, _ := c.run(t, c.key.String(), "result", "submit", path); code != 1 {
					t.Fatalf("submit of a bad spec: exit %d", code)
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newResultCopy(t)
			prepare(t, c)
			if _, err := os.Stat(c.draftFile()); err != nil {
				t.Fatalf("no draft: %v", err)
			}
			before := storeBytes(t, c.dir)
			if code, out, stderr := c.hook(t, c.key.String(), `{"stop_hook_active":false}`); code != 0 || out != "" || stderr != "" {
				t.Fatalf("hook: exit %d, %q, %q; want nothing", code, out, stderr)
			}
			if storeBytes(t, c.dir) != before {
				t.Error("the hook changed the store")
			}
		})
	}
}

// TestResultHookSkipsACancelledAttempt: a cancelled attempt is not nudged.
func TestResultHookSkipsACancelledAttempt(t *testing.T) {
	c := newResultCopy(t)
	result := filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.ResultFile)
	if err := adapter.MarkCancelled(result, adapter.Identity{TaskID: "t1", AttemptID: "plan-1"}); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := c.hook(t, c.key.String(), ""); code != 0 || out != "" || stderr != "" {
		t.Fatalf("hook: exit %d, %q, %q; want nothing", code, out, stderr)
	}
	if msgs := c.messagesTo(t); len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none", msgs)
	}
}

// TestBrokenResultHookChangesNothing: every failure is one warning line on stderr, nothing on stdout and exit 0, so
// Claude Code neither blocks the stop nor keeps going, and no nudge is sent.
func TestBrokenResultHookChangesNothing(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	elsewhere := resultCopy{dir: c.dir, work: t.TempDir(), key: c.key}
	finished := artisan.AttemptKey{Repository: "app", TaskID: "t1", Step: "plan", Attempt: 2}
	for name, run := range map[string]func() (int, string, string){
		"no attempt":        func() (int, string, string) { return c.hook(t, "", "") },
		"malformed attempt": func() (int, string, string) { return c.hook(t, "not-an-attempt", "") },
		"outside the copy":  func() (int, string, string) { return elsewhere.hook(t, env, "") },
		"stray arguments":   func() (int, string, string) { return c.hook(t, env, "", "--copy", c.dir) },
		"bad stdin":         func() (int, string, string) { return c.hook(t, env, "not json") },
		"attempt not in it": func() (int, string, string) { return c.hook(t, finished.String(), "") },
	} {
		code, out, stderr := run()
		if code != 0 || out != "" || !strings.HasPrefix(stderr, "guildhall: warning: result hook: ") || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 0, no output and one warning", name, code, out, stderr)
		}
	}
	if msgs := c.messagesTo(t); len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none", msgs)
	}
}

// TestResultHookWarnsOnALockedOrBrokenStore: a store held past the copy's lock wait, or one that cannot be opened, is
// a warning and no nudge.
func TestResultHookWarnsOnALockedOrBrokenStore(t *testing.T) {
	c := newResultCopy(t)
	cfg := filepath.Join(c.dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(b), "[local]", "[local]\nlock_wait = \"50ms\"", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, stderr := c.hook(t, c.key.String(), "")
	took := time.Since(start)
	_ = holder.Close()
	if took > 10*time.Second {
		t.Errorf("the hook waited %v, not the copy's 50ms lock wait", took)
	}
	if code != 0 || out != "" || !strings.HasPrefix(stderr, "guildhall: warning: result hook: nudge to attempt app/t1/plan/1 not sent: ") {
		t.Fatalf("locked store: exit %d, %q, %q", code, out, stderr)
	}
	if msgs := c.messagesTo(t); len(msgs) != 0 {
		t.Fatalf("messages = %+v, want none", msgs)
	}
	if err := os.WriteFile(filepath.Join(c.dir, directorStoreFile), []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = c.hook(t, c.key.String(), "")
	if code != 0 || out != "" || !strings.HasPrefix(stderr, "guildhall: warning: result hook: nudge to attempt app/t1/plan/1 not sent: ") ||
		strings.Count(stderr, "\n") != 1 {
		t.Fatalf("broken store: exit %d, %q, %q", code, out, stderr)
	}
}

// TestResultHookCommandIsNotGranted: the model's Bash grant covers result check and submit only, so a model cannot
// make Guildhall send it a nudge.
func TestResultHookCommandIsNotGranted(t *testing.T) {
	for _, rule := range grant.GuildhallRules {
		if strings.Contains(rule, "result "+resultHookCommand) || strings.Contains(rule, "result:*") || strings.Contains(rule, "guildhall:*") {
			t.Errorf("rule %q grants guildhall result %s", rule, resultHookCommand)
		}
	}
}

// TestFakeNudged: a fake Artisan with the nudge directive runs guildhall result hook from PATH inside the attempt
// before it stops. Nudged, it acknowledges the nudge and writes its result, which intake accepts; with
// fault=no-result it stops without a result anyway, leaving one unacknowledged nudge from guildhall in the store.
func TestFakeNudged(t *testing.T) {
	for _, noResult := range []bool{false, true} {
		t.Run(map[bool]string{false: "rescued", true: "still exits"}[noResult], func(t *testing.T) {
			c := newResultCopy(t)
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
			prompt := "guildhall-fake: output=spec.md:" + strings.ReplaceAll(goodSpec, "\n", `\n`) + "\nguildhall-fake: nudge=true\n"
			if noResult {
				prompt += "guildhall-fake: fault=no-result\n"
			}
			promptFile := filepath.Join(t.TempDir(), "prompt.md")
			writeFile(t, promptFile, prompt)
			var out strings.Builder
			args := fake.Args{Identity: adapter.Identity{TaskID: "t1", AttemptID: "plan-1"}, PromptFile: promptFile, ResultFile: c.draftFile()}
			if err := fake.Run(context.Background(), args, &out, func(context.Context, time.Duration) error { return nil }); err != nil {
				t.Fatalf("fake: %v\n%s", err, out.String())
			}
			t.Log(out.String())
			m, ok := c.messagesTo(t)[nudgeID]
			if !ok || m.Sender != store.SenderGuildhall || m.Type != store.MessageNudge || m.Acknowledged != !noResult {
				t.Fatalf("nudge = %+v (%v); want one from guildhall, acknowledged %v", m, ok, !noResult)
			}
			if noResult {
				if _, err := os.Stat(c.draftFile()); !os.IsNotExist(err) {
					t.Fatalf("the fake left a draft: %v", err)
				}
				return
			}
			if a := c.takeDraft(t); a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "" {
				t.Errorf("attempt = %+v, want succeeded", a)
			}
		})
	}
}
