package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// ownedAttempt is attempt app/t-1/build/1, running in a new store, with a
// short state directory and a prompt.
type ownedAttempt struct {
	store, state, prompt, workdir string
}

func newOwnedAttempt(t *testing.T, prompt string) ownedAttempt {
	t.Helper()
	state, err := os.MkdirTemp("", "gc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	work := t.TempDir()
	p := filepath.Join(work, "prompt.md")
	if err := os.WriteFile(p, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	return ownedAttempt{store: messagingStorePath(t), state: state, prompt: p, workdir: work}
}

// args are the flags naming the attempt, for start, stop and status.
func (o ownedAttempt) args(cmd string, extra ...string) []string {
	a := []string{"artisan", cmd, "--store", o.store, "--state", o.state, "--repository", "app", "--task", "t-1", "--step", "build", "--attempt", "1"}
	if cmd == "start" {
		a = append(a, "--adapter", "fake", "--prompt", o.prompt, "--workdir", o.workdir)
	}
	return append(a, extra...)
}

func (o ownedAttempt) ownership(t *testing.T) store.Ownership {
	t.Helper()
	s, err := store.Open(o.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	own, err := s.Ownership("app", "t-1", "build", 1)
	if err != nil {
		t.Fatal(err)
	}
	return own
}

// guildhall runs the CLI in a separate process of the test binary within a
// bound, returning its exit code and output.
func guildhall(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(testExecutable(t), args...)
	cmd.Env = append(os.Environ(), roleEnv+"=guildhall")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	// The exit code, not Wait's error, is the result.
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("guildhall %q did not finish within 30s; stderr %s", args, stderr.String())
	}
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

type runReport struct {
	Attempt string `json:"attempt"`
	Outcome struct {
		State string `json:"state"`
		Rung  int    `json:"rung"`
	} `json:"outcome"`
	AttemptOutcome string `json:"attempt_outcome"`
	Result         *struct {
		Status string `json:"status"`
	} `json:"result"`
}

type classification struct {
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Launcher string `json:"launcher"`
}

func TestArtisanStartOwnsTheAttemptAndExitsByOutcome(t *testing.T) {
	cases := []struct {
		prompt, extra string
		code          int
		outcome       string
		// status is the classification afterwards: the launcher's decision.
		status string
	}{
		{"Do it.\n", "", 0, "succeeded", "finished"},
		{"guildhall-fake: fault=exit-error\n", "", 0, "succeeded", "finished"},
		{"guildhall-fake: outcome=failed\n", "", 4, "failed", "finished"},
		{"guildhall-fake: fault=no-result\n", "", 4, "failed", "failed"},
		{"guildhall-fake: fault=malformed\n", "", 4, "failed", "failed"},
		{"guildhall-fake: fault=wrong-identity\n", "", 4, "failed", "failed"},
		{"guildhall-fake: delay=5m\n", "300ms", 5, "stopped", "cancelled"},
	}
	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.prompt), func(t *testing.T) {
			o := newOwnedAttempt(t, tc.prompt)
			var extra []string
			if tc.extra != "" {
				extra = []string{"--timeout", tc.extra}
			}
			code, stdout, stderr := guildhall(t, "", o.args("start", extra...)...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stdout %q; stderr %q", code, tc.code, stdout, stderr)
			}
			r := decodeJSON[runReport](t, lastLine(stdout))
			if r.Attempt != "app/t-1/build/1" || r.AttemptOutcome != tc.outcome {
				t.Errorf("report %+v; want attempt outcome %s", r, tc.outcome)
			}
			if tc.outcome == "succeeded" && (r.Result == nil || r.Result.Status != "succeeded") {
				t.Errorf("report %+v carries no succeeded result", r)
			}
			if own := o.ownership(t); own.State != store.OwnerReleased {
				t.Errorf("ownership %+v not released", own)
			}
			code, stdout, _ = guildhall(t, "", o.args("status")...)
			if c := decodeJSON[classification](t, stdout); code != 0 || c.State != tc.status {
				t.Errorf("status exit %d: %+v; want %s", code, c, tc.status)
			}
		})
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndex(s, "\n")+1:]
}

func TestArtisanOwnedCommandUsage(t *testing.T) {
	o := newOwnedAttempt(t, "x")
	for _, bad := range [][]string{
		{"artisan", "start", "--adapter", "fake"},
		o.args("start", "--result", filepath.Join(o.workdir, "r.json")),
		o.args("start", "--adapter", "claude"),
		o.args("start", "--timeout", "-1s"),
		append(o.args("status")[:12], "0"),
		append(o.args("stop")[:12], "one"),
		{"artisan", "status", "--store", o.store},
		o.args("status", "extra"),
		o.args("stop", "--prompt", o.prompt),
	} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(bad); code != 2 {
			t.Errorf("%q: exit %d, want 2; stderr %q", bad, code, stderr)
		}
	}
	if code, _, stderr := guildhall(t, "", o.args("status", "--store", filepath.Join(o.workdir, "missing.store"))...); code != 1 {
		t.Errorf("status of a missing store: exit %d, stderr %q", code, stderr)
	}
}

func TestArtisanStopWithoutAnOwner(t *testing.T) {
	o := newOwnedAttempt(t, "x")
	code, stdout, stderr := guildhall(t, "", o.args("stop")...)
	var rep struct {
		Endpoint       string         `json:"endpoint"`
		Classification classification `json:"classification"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil || code != 0 || rep.Classification.State != "absent" {
		t.Errorf("exit %d: %q (%v); stderr %q", code, stdout, err, stderr)
	}
}

// TestArtisanStartCustody is the launcher and store custody acceptance (P6):
// while a launcher process owns a running fake worker, other processes read
// its status, send a message, answer a decision and are refused a competing
// run, each within a bound and without changing the owner; then a stop
// through the endpoint ends the attempt.
func TestArtisanStartCustody(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: delay=5m\n")
	run := exec.Command(testExecutable(t), o.args("start")...)
	run.Env = append(os.Environ(), roleEnv+"=guildhall")
	var runOut, runErr bytes.Buffer
	run.Stdout, run.Stderr = &runOut, &runErr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = run.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = run.Process.Kill()
			t.Errorf("the launcher did not exit; stderr %s", runErr.String())
		}
	})

	var c classification
	var owner store.Ownership
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("waited for running with recorded containment; classification %+v; ownership %+v; launcher stderr %s", c, owner, runErr.String())
		}
		if _, out, _ := guildhall(t, "", o.args("status")...); out != "" {
			c = decodeJSON[classification](t, out)
			if c.State == "running" {
				owner = o.ownership(t)
				if owner.State == store.OwnerRunning && owner.Containment.Kind != "" {
					break
				}
			}
		}
	}
	if owner.LauncherIdentity.PID != run.Process.Pid || owner.State != store.OwnerRunning || owner.Endpoint == "" || owner.Containment.Kind == "" {
		t.Fatalf("ownership %+v; want the launcher process %d running with its endpoint and containment", owner, run.Process.Pid)
	}

	bounded := func(name string, wantCode int, wantErr, stdin string, args ...string) {
		t.Helper()
		begun := time.Now()
		code, stdout, stderr := guildhall(t, stdin, args...)
		if code != wantCode || !strings.Contains(stderr, wantErr) {
			t.Errorf("%s: exit %d, want %d; stdout %q; stderr %q", name, code, wantCode, stdout, stderr)
		}
		if d := time.Since(begun); d > 10*time.Second {
			t.Errorf("%s took %v while the launcher runs", name, d)
		}
	}
	bounded("send a message", 0, "", envelope("q-1", "director"), "messages", "send", "--store", o.store, "director", "-")
	bounded("answer a decision", 0, "", "Yes", "decide", "answer", "--store", o.store, "--repo", "app", "--task", "t-1", "--decision", "accept-plan", "--answer-file", "-")
	bounded("competing run", 1, "owned by a live launcher", "", o.args("start")...)
	if now := o.ownership(t); now != owner {
		t.Errorf("ownership changed from %+v to %+v", owner, now)
	}

	code, stdout, stderr := guildhall(t, "", o.args("stop")...)
	if code != 0 || !strings.Contains(stdout, `"endpoint":"stopped"`) || !strings.Contains(stdout, `"state":"cancelled"`) {
		t.Errorf("stop: exit %d; stdout %q; stderr %q", code, stdout, stderr)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("the launcher did not exit after the stop; stderr %s", runErr.String())
	}
	if code := run.ProcessState.ExitCode(); code != 5 {
		t.Errorf("launcher exit %d, want 5; stdout %q; stderr %q", code, runOut.String(), runErr.String())
	}
	if r := decodeJSON[runReport](t, lastLine(runOut.String())); r.AttemptOutcome != "stopped" || r.Outcome.State != "cancelled" {
		t.Errorf("launcher report %+v", r)
	}
}
