package claude_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// exitGraceEnv shortens claude.ExitGrace in a wrapper this test binary runs as `guildhall claude-artisan`, as the
// launcher-level test does; it is read only by TestMain.
const exitGraceEnv = "GUILDHALL_TEST_CLAUDE_EXIT_GRACE"

// stillRunning is the diagnostic line the wrapper writes when it stops a Claude that outlived its result.
const stillRunning = "after its result (trigger: "

// shortenResultWaits sets claude.ExitGrace and claude.QuietLimit for t.
func shortenResultWaits(t *testing.T, exit, quiet time.Duration) {
	t.Helper()
	oldExit, oldQuiet := claude.ExitGrace, claude.QuietLimit
	claude.ExitGrace, claude.QuietLimit = exit, quiet
	t.Cleanup(func() { claude.ExitGrace, claude.QuietLimit = oldExit, oldQuiet })
}

// lingerRun is a claude.Run started on a stub playing b, its error output, and its end.
type lingerRun struct {
	spec   adapter.Spec
	stderr *bytes.Buffer
	cancel context.CancelFunc
	done   chan error
}

func startLinger(t *testing.T, b adaptertest.Behaviour) *lingerRun {
	t.Helper()
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(b)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &lingerRun{spec: spec, stderr: &bytes.Buffer{}, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- claude.Run(ctx, args(spec), &bytes.Buffer{}, r.stderr) }()
	// A failed test must not leave the stub lingering until HangLimit.
	t.Cleanup(func() {
		cancel()
		if err := <-r.done; err != nil {
			t.Logf("claude.Run after cleanup cancel: %v", err)
		}
	})
	return r
}

// wait returns Run's error, failing t if Run has not returned within ceiling, which is well under
// claudetest.HangLimit, so a wrapper that waits for the lingering stub to give up fails.
func (r *lingerRun) wait(t *testing.T, ceiling time.Duration) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err
		return err
	case <-time.After(testenv.Ceiling(ceiling)):
		t.Fatalf("Run still waiting for a Claude that outlived its result; stderr:\n%s", r.stderr.String())
		return nil
	}
}

// TestRunStopsClaudeLingeringAfterItsResult: a Claude that has written a valid draft but does not exit is stopped,
// and its draft is still published with the usage its result event reported, if any, so the attempt ends promptly
// with its result rather than when Claude finally exits (d100).
func TestRunStopsClaudeLingeringAfterItsResult(t *testing.T) {
	claudetest.Install(t)
	for _, tc := range []struct {
		b              adaptertest.Behaviour
		exit, quiet    time.Duration
		usage          adapter.Usage
		trigger, never string
	}{
		{claudetest.Linger, 200 * time.Millisecond, time.Hour, claudetest.Usage, "result-event", "quiet"},
		{claudetest.LingerSilent, time.Hour, 500 * time.Millisecond, adapter.UnknownUsage, "quiet", "result-event"},
	} {
		t.Run(string(tc.b), func(t *testing.T) {
			shortenResultWaits(t, tc.exit, tc.quiet)
			r := startLinger(t, tc.b)
			if err := r.wait(t, 30*time.Second); err != nil {
				t.Errorf("Run = %v, want nil: a result-ready stop is not a failure", err)
			}
			draft, err := os.ReadFile(claude.DraftFile(r.spec.ResultFile))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(r.spec.ResultFile); err != nil || !bytes.Equal(got, draft) {
				t.Errorf("result file = %q, %v; want the draft %q", got, err, draft)
			}
			if res, err := adapter.ReadResult(r.spec.ResultFile, r.spec.Identity); err != nil || res.Status != adapter.StatusSucceeded {
				t.Errorf("ReadResult = %+v, %v", res, err)
			}
			if got, err := adapter.ReadUsage(r.spec.ResultFile); err != nil || got != tc.usage {
				t.Errorf("usage = %+v, %v; want %+v", got, err, tc.usage)
			}
			if s := r.stderr.String(); !strings.Contains(s, "Claude still running ") || !strings.Contains(s, stillRunning+tc.trigger+")") || strings.Contains(s, stillRunning+tc.never) {
				t.Errorf("stderr = %q; want one still-running line with trigger %s", s, tc.trigger)
			}
		})
	}
}

// TestRunLeavesClaudeRunningWithAnInvalidDraft: a draft that is not a valid result for the attempt never cuts Claude
// off, however long it lingers; ending ctx still stops it, and nothing is published.
func TestRunLeavesClaudeRunningWithAnInvalidDraft(t *testing.T) {
	claudetest.Install(t)
	for _, b := range []adaptertest.Behaviour{claudetest.LingerWrongIdentity, claudetest.LingerMalformed} {
		t.Run(string(b), func(t *testing.T) {
			shortenResultWaits(t, 50*time.Millisecond, 100*time.Millisecond)
			r := startLinger(t, b)
			testenv.WaitFor(t, "the lingering stub's draft", func() bool {
				_, err := os.Stat(claude.DraftFile(r.spec.ResultFile))
				return err == nil
			})
			// timing:allow negative check: many ExitGrace and QuietLimit periods pass without the wrapper stopping Claude.
			for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
				select {
				case err := <-r.done:
					t.Fatalf("Run = %v before ctx ended; stderr:\n%s", err, r.stderr.String())
				default:
				}
			}
			r.cancel()
			if err := r.wait(t, 30*time.Second); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context cancellation", err)
			}
			if _, err := os.Stat(r.spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("an invalid lingering draft was published: %v", err)
			}
			if s := r.stderr.String(); strings.Contains(s, stillRunning) {
				t.Errorf("stderr = %q; want no still-running line", s)
			}
		})
	}
}

// TestRunPublishesNothingWhenCancelledWhileLingering: the cancel marker wins over a result-ready stop, whether it
// appears while Claude lingers or as the wrapper stops Claude, just before publishing.
func TestRunPublishesNothingWhenCancelledWhileLingering(t *testing.T) {
	claudetest.Install(t)
	t.Run("marker while lingering", func(t *testing.T) {
		shortenResultWaits(t, time.Hour, time.Hour)
		r := startLinger(t, claudetest.Linger)
		testenv.WaitFor(t, "the lingering stub's draft", func() bool {
			_, err := os.Stat(claude.DraftFile(r.spec.ResultFile))
			return err == nil
		})
		if err := adapter.MarkCancelled(r.spec.ResultFile, r.spec.Identity); err != nil {
			t.Fatal(err)
		}
		if err := r.wait(t, 30*time.Second); err != nil {
			t.Errorf("Run = %v, want nil: the marker is not an error", err)
		}
		if _, err := os.Stat(r.spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a cancelled attempt has a result: %v", err)
		}
	})
	t.Run("marker at publish", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the stub cannot catch the wrapper's termination on Windows to write the marker as it stops")
		}
		shortenResultWaits(t, 100*time.Millisecond, time.Hour)
		r := startLinger(t, claudetest.LingerCancelOnStop)
		if err := r.wait(t, 30*time.Second); !errors.Is(err, adapter.ErrAttemptCancelled) {
			t.Errorf("Run = %v, want ErrAttemptCancelled", err)
		}
		if _, err := os.Stat(r.spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a cancelled attempt has a result: %v", err)
		}
	})
}

// TestRunExitingClaudeIsNotStopped: a Claude that exits on its own after its result is never reported as lingering.
func TestRunExitingClaudeIsNotStopped(t *testing.T) {
	claudetest.Install(t)
	for _, b := range []adaptertest.Behaviour{adaptertest.Succeed, adaptertest.ExitErrorAfterResult, claudetest.Denied} {
		t.Run(string(b), func(t *testing.T) {
			r := startLinger(t, b)
			_ = r.wait(t, 30*time.Second)
			if _, err := adapter.ReadResult(r.spec.ResultFile, r.spec.Identity); err != nil {
				t.Errorf("ReadResult = %v", err)
			}
			if s := r.stderr.String(); strings.Contains(s, stillRunning) {
				t.Errorf("stderr = %q; want no still-running line", s)
			}
		})
	}
}

// TestLauncherFinishesLingeringClaude: through the owned launcher, a Claude that outlives its result ends the
// attempt Finished with its result accepted, not cancelled, and long before the stub would give up by itself.
func TestLauncherFinishesLingeringClaude(t *testing.T) {
	s := subject(t)
	t.Setenv(exitGraceEnv, "200ms")
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(claudetest.Linger)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.Runtime.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	u, err := artisan.NewUnit("claude-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	dir := filepath.Dir(spec.ResultFile)
	l, err := artisan.NewLauncher(artisan.LauncherConfig{
		Dir:   dir,
		Unit:  u,
		Files: launch.Files{ResultFile: spec.ResultFile, Identity: spec.Identity},
		Key:   artisan.AttemptKey{Repository: "app", TaskID: spec.TaskID, Step: "build", Attempt: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	type ran struct {
		out artisan.Outcome
		err error
	}
	done := make(chan ran, 1)
	start := time.Now()
	go func() {
		out, err := l.Run(c, nil)
		done <- ran{out, err}
	}()
	var got ran
	select {
	case got = <-done:
	case <-time.After(testenv.Ceiling(60 * time.Second)):
		_, _ = l.Stop()
		<-done
		t.Fatal("the launcher still waits for a Claude that outlived its result")
	}
	// On slow CI the ceiling can stretch to HangLimit, when the stub might give up by itself; only the wrapper's
	// stop ends the attempt well before that.
	if elapsed := time.Since(start); elapsed >= claudetest.HangLimit/2 {
		t.Errorf("the attempt took %v; want the wrapper to end it well before HangLimit (%v)", elapsed, claudetest.HangLimit)
	}
	if got.err != nil || got.out.State != artisan.Finished {
		t.Errorf("launcher Run = %+v, %v; want Finished", got.out, got.err)
	}
	if out, err := artisan.ReadOutcome(dir); err != nil || out.State != artisan.Finished || out.LateResultRejected {
		t.Errorf("ReadOutcome = %+v, %v; want Finished", out, err)
	}
	f, err := os.Open(filepath.Join(dir, artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	// The wrapper exits as soon as it publishes, so the launcher may accept the result as it polls (result-accepted)
	// or as the worker exits; either way the attempt ends Finished, and nothing cancels it.
	var events []string
	var ended artisan.OutcomeState
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e struct {
			Event   string          `json:"event"`
			Outcome artisan.Outcome `json:"outcome"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e.Event)
			if e.Event == "ended" {
				ended = e.Outcome.State
			}
		}
	}
	if ended != artisan.Finished || slices.Contains(events, "cancel-marker") || slices.Contains(events, "worker-outlived-result") {
		t.Errorf("events = %q, ended %q; want the attempt ended Finished with no cancellation", events, ended)
	}
}
