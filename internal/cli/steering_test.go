package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// steer stores a Director's steering message to the copy's plan attempt 1, as `messages send` from the Director's
// session records it.
func (c resultCopy) steer(t *testing.T, id, text string) {
	t.Helper()
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.SendMessage(store.Message{ID: id, Recipient: store.RecipientArtisan, Repository: "app", TaskID: "t1", Step: "plan", Attempt: 1,
		Type: "note", Text: text, Sender: store.SenderDirector}); err != nil {
		t.Fatal(err)
	}
}

// TestArtisanMessagesListAndAck: inside an attempt, messages lists only the attempt's own inbox, shows each unread
// message in a frame on stderr with control characters stripped while stdout is the Director's own list of that inbox,
// byte for byte, and ack acknowledges, a second time returning the first receipt.
func TestArtisanMessagesListAndAck(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	c.steer(t, "steer-1", "The base moved;\x1b[2J rebase first.\r\n\x1b]0;owned\aThen run the tests.")
	// A message to the Director's inbox is not the attempt's.
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendMessage(store.Message{ID: "q-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t1", Type: "question", Text: "Which?"})
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}

	code, out, stderr := c.run(t, env, "messages")
	if code != 0 {
		t.Fatalf("messages: exit %d, %s", code, stderr)
	}
	page := decodeJSON[messaging.Page](t, out)
	if len(page.Messages) != 1 || page.Messages[0].ID != "steer-1" {
		t.Fatalf("messages listed %s", out)
	}
	wantFrame := "==== guildhall: steering message steer-1 from director (note) ====\n" +
		"| The base moved;[2J rebase first.\n" +
		"| ]0;ownedThen run the tests.\n" +
		"==== act on it, then acknowledge it: guildhall messages ack steer-1 ====\n"
	if stderr != wantFrame {
		t.Fatalf("stderr = %q, want %q", stderr, wantFrame)
	}
	app, director, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"messages", "list", "--store", filepath.Join(c.dir, directorStoreFile), "artisan:" + env}); code != 0 || director.String() != out {
		t.Fatalf("the Artisan's stdout %q differs from the Director's list %q (exit %d)", out, director, code)
	}
	if _, list, _ := c.run(t, env, "messages", "list", "--limit", "5"); list != out {
		t.Errorf("messages list = %q, want %q", list, out)
	}

	code, out, stderr = c.run(t, env, "messages", "ack", "steer-1")
	if code != 0 {
		t.Fatalf("ack: exit %d, %s", code, stderr)
	}
	receipt := decodeJSON[store.Receipt](t, out)
	if receipt.MutationID != "message-ack:steer-1" {
		t.Fatalf("ack receipt = %s", out)
	}
	if code, again, stderr := c.run(t, env, "messages", "ack", "steer-1"); code != 0 || again != out || stderr != "" {
		t.Fatalf("repeated ack: exit %d, %q, %s; want %q", code, again, stderr, out)
	}
	if code, out, stderr := c.run(t, env, "messages"); code != 0 || out != "{\"messages\":[],\"more\":false}\n" || stderr != "" {
		t.Fatalf("messages after the ack: exit %d, %q, %q", code, out, stderr)
	}
	if code, _, stderr := c.run(t, env, "messages", "ack", "q-1"); code != 1 || !strings.Contains(stderr, "addressed to director") {
		t.Errorf("ack of the Director's message: exit %d, %s", code, stderr)
	}
}

// TestAttemptLockWaitIgnoresAnUnauthorizedManagedRevision: an in-attempt command on a copy whose managed.toml no
// reload authorized still succeeds, waits config.toml's lock wait rather than the managed lock_wait, and says so in
// one warning line; once a reload authorizes the digest, the managed lock_wait applies and nothing is printed.
func TestAttemptLockWaitIgnoresAnUnauthorizedManagedRevision(t *testing.T) {
	c := newResultCopy(t)
	d := writeManaged(t, c.dir, map[string]config.ManagedSetting{config.LockWaitKey: {Value: "2s"}})
	if !managedRun() { // A managed run's copy is already enrolled, with another digest authorized.
		recordEvent(t, c.dir, store.Enrolment{Event: store.Enrolled, Steward: "hq", Digest: d})
	}
	app, _, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return c.work, nil }
	app.Getenv = func(k string) string { return map[string]string{artisan.AttemptEnv: c.key.String()}[k] }
	if code := app.Run([]string{"messages"}); code != 0 {
		t.Fatalf("messages: exit %d, %s", code, stderr)
	}
	want := "guildhall: warning: managed settings not applied, using config.toml alone: managed.toml " + d + " is not authorized"
	if got := stderr.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, want) {
		t.Errorf("stderr = %q, want one line %q", got, want)
	}
	if r, err := app.attemptStore(c.key); err != nil || r.wait != storeWait {
		t.Errorf("unauthorized: wait %v (%v), want config.toml's %v", r.wait, err, storeWait)
	}
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceReload, Digest: d})
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	app, _, stderr = newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return c.work, nil }
	if r, err := app.attemptStore(c.key); err != nil || r.wait != 2*time.Second || stderr.Len() != 0 {
		t.Errorf("authorized: wait %v (%v), stderr %q; want the managed 2s", r.wait, err, stderr)
	}
}

// TestArtisanMessagesRefusals: inside an attempt, messages takes no store, copy or recipient, and has no send or read.
func TestArtisanMessagesRefusals(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	path := filepath.Join(c.dir, directorStoreFile)
	for name, args := range map[string][]string{
		"--store":           {"messages", "list", "--store", path},
		"--copy":            {"messages", "--copy", c.dir},
		"a recipient":       {"messages", "list", "director"},
		"another attempt":   {"messages", "list", "artisan:app/t1/plan/2"},
		"ack with a target": {"messages", "ack", "artisan:" + env, "steer-1"},
		"send":              {"messages", "send", "--store", path, "director", "-"},
		"read":              {"messages", "read", "steer-1"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, out, stderr := c.run(t, env, args...); code != 2 || out != "" || !strings.Contains(stderr, "inside an attempt") {
				t.Errorf("exit %d, stdout %q, stderr %q; want a usage refusal", code, out, stderr)
			}
		})
	}
}

// TestResultRefusedUntilSteeringAcknowledged: result check and submit show the unread message on stderr and refuse
// a succeeded result naming it, as intake does; submit spends no resubmission and writes no draft; once the
// message is acknowledged the result is accepted and nothing is framed.
func TestResultRefusedUntilSteeringAcknowledged(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	draft := c.draft(t, goodSpec)
	c.steer(t, "steer-1", "Rebase first.")
	reason := "rejected: unacknowledged steering message to this attempt: steer-1; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again\n"
	code, out, stderr := c.run(t, env, "result", "check", draft)
	if code != 1 || out != reason || !strings.Contains(stderr, "steering message steer-1 from director (note)") {
		t.Fatalf("check: exit %d, %q, %q", code, out, stderr)
	}
	for range 3 {
		code, out, _ = c.run(t, env, "result", "submit", draft)
		if code != 1 || out != reason+"0 of 2 resubmissions used, none for this; acknowledge the messages and submit again\n" {
			t.Fatalf("submit: exit %d, %q", code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.SubmissionsFile)); !os.IsNotExist(err) {
		t.Errorf("an unacknowledged-message rejection counted a submission: %v", err)
	}
	if _, err := os.Stat(c.draftFile()); !os.IsNotExist(err) {
		t.Errorf("an unacknowledged-message rejection wrote the draft: %v", err)
	}
	if code, _, stderr := c.run(t, env, "messages", "ack", "steer-1"); code != 0 {
		t.Fatalf("ack: exit %d, %s", code, stderr)
	}
	if code, out, stderr := c.run(t, env, "result", "submit", draft); code != 0 || out != "ok\n" || stderr != "" {
		t.Fatalf("submit after the ack: exit %d, %q, %q", code, out, stderr)
	}
	if a := c.takeDraft(t); a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "" {
		t.Errorf("attempt = %+v, want succeeded", a)
	}
}

// TestIntakeRefusesUnacknowledgedSteering: a draft written directly, bypassing submit, is refused by intake while a
// message to the attempt is unacknowledged, with the reason result check gives.
func TestIntakeRefusesUnacknowledgedSteering(t *testing.T) {
	c := newResultCopy(t)
	draft := c.draft(t, goodSpec)
	data, err := os.ReadFile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.draftFile(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.steer(t, "steer-1", "Rebase first.")
	_, out, _ := c.run(t, c.key.String(), "result", "check", draft)
	reason, _, _ := strings.Cut(strings.TrimPrefix(out, "rejected: "), "\n")
	if a := c.takeDraft(t); a.Outcome != store.AttemptFailed || a.IntakeRejection != reason || !strings.Contains(reason, "steer-1") {
		t.Errorf("attempt = %+v, want failed with %q", a, reason)
	}
}

// TestFakeAcksSteering: a fake Artisan that leaves a steering message unacknowledged has its succeeded result
// refused by intake; one whose prompt acks it, running guildhall messages ack from PATH inside the attempt as a
// model would, has the same result accepted.
func TestFakeAcksSteering(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "unacknowledged", true: "acknowledged"}[ack], func(t *testing.T) {
			c := newResultCopy(t)
			c.steer(t, "steer-1", "Rebase first.")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Named as on PATH for this OS, guildhall.exe on Windows, so exec.LookPath finds it as a model's shell would.
			bin := t.TempDir()
			if err := os.Symlink(exe, filepath.Join(bin, release.BinaryName(runtime.GOOS))); err != nil {
				t.Fatal(err)
			}
			t.Setenv(roleEnv, "guildhall")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(artisan.AttemptEnv, c.key.String())
			t.Chdir(c.work)
			prompt := "guildhall-fake: output=spec.md:" + strings.ReplaceAll(goodSpec, "\n", `\n`) + "\n"
			if ack {
				prompt += "guildhall-fake: ack=steer-1\n"
			}
			promptFile := filepath.Join(t.TempDir(), "prompt.md")
			writeFile(t, promptFile, prompt)
			var out strings.Builder
			args := fake.Args{Identity: adapter.Identity{TaskID: "t1", AttemptID: "plan-1"}, PromptFile: promptFile, ResultFile: c.draftFile()}
			if err := fake.Run(context.Background(), args, &out, func(context.Context, time.Duration) error { return nil }); err != nil {
				t.Fatalf("fake: %v\n%s", err, out.String())
			}
			t.Log(out.String())
			a := c.takeDraft(t)
			switch {
			case ack && (a.Outcome != store.AttemptSucceeded || a.IntakeRejection != ""):
				t.Errorf("attempt = %+v, want succeeded", a)
			case !ack && (a.Outcome != store.AttemptFailed || !strings.HasPrefix(a.IntakeRejection, "unacknowledged steering message to this attempt: steer-1;")):
				t.Errorf("attempt = %+v, want refused for steer-1", a)
			}
		})
	}
}

// TestTestsRunOutsideAnAttempt: the variable the test harness clears, so checks an Artisan runs inside its attempt
// behave as on CI, is the attempt marker.
func TestTestsRunOutsideAnAttempt(t *testing.T) {
	if testenv.AttemptEnv != artisan.AttemptEnv {
		t.Fatalf("testenv.AttemptEnv = %q, want %q", testenv.AttemptEnv, artisan.AttemptEnv)
	}
}

// TestSteeringWarnsWhenUnread: a store locked past the copy's lock wait, or no copy running the attempt, is a
// one-line warning on stderr and nothing else; it never stops the command.
func TestSteeringWarnsWhenUnread(t *testing.T) {
	c := newResultCopy(t)
	c.steer(t, "steer-1", "Rebase first.")
	cfg := filepath.Join(c.dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "[local]") {
		t.Fatal("the copy's config has no [local] table")
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(b), "[local]", "[local]\nlock_wait = \"50ms\"", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return c.work, nil }
	app.Getenv = func(k string) string { return map[string]string{artisan.AttemptEnv: c.key.String()}[k] }
	start := time.Now()
	app.showSteering()
	took := time.Since(start)
	_ = holder.Close()
	if took > 10*time.Second {
		t.Errorf("the read waited %v, not the copy's 50ms lock wait", took)
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || !strings.HasPrefix(stderr.String(), "guildhall: warning: steering messages to attempt app/t1/plan/1 not read: ") {
		t.Fatalf("locked store: stdout %q, stderr %q; want one warning line", stdout, stderr)
	}
	// Outside the copy, result check still runs and reports its own refusal after the warning.
	app, stdout, stderr = newTestApp(nil, nil)
	wd := t.TempDir()
	app.Getwd = func() (string, error) { return wd, nil }
	app.Getenv = func(k string) string { return map[string]string{artisan.AttemptEnv: c.key.String()}[k] }
	if code := app.Run([]string{"result", "check", "draft.json"}); code != 2 || stdout.Len() != 0 ||
		!strings.HasPrefix(stderr.String(), "guildhall: warning: steering messages to attempt app/t1/plan/1 not read: no Guildhall copy runs attempt") ||
		!strings.Contains(stderr.String(), noCopyLine(wd)) {
		t.Fatalf("outside a copy: exit %d, %q, %q", code, stdout, stderr)
	}
}
