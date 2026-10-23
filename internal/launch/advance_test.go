package launch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// advanceEnv makes the test binary play `guildhall step done` and `guildhall task start` for Run's advance:
// it appends what it saw (advanceSeen) to the record file the JSON advanceDouble in the variable names, prints a
// line on each output and exits with the double's code for its run: the n-th run (from 0) exits Exits[n], and a run
// past the list exits with its last code, or 0 for none. With Until set it first waits up to 30 seconds, as long
// as the real command waits for the task lock, for that file to exist.
const advanceEnv = "GUILDHALL_LAUNCH_TEST_ADVANCE"

type advanceDouble struct {
	Record, Dir, Store, Until string
	Exits                     []int
}

// advanceSeen is one advance child's view: its arguments, the attempt's outcome.json state, and the store's
// attempt outcome and ownership state, read as the child starts.
type advanceSeen struct {
	Argv         []string             `json:"argv"`
	Decided      artisan.OutcomeState `json:"decided"`
	Outcome      store.AttemptOutcome `json:"outcome"`
	Owner        store.OwnershipState `json:"owner"`
	ObservedErrs []string             `json:"errors,omitempty"`
	// Unblocked is whether the Until file appeared in time.
	Unblocked bool `json:"unblocked,omitempty"`
}

func mainIfAdvance() {
	v := os.Getenv(advanceEnv)
	if v == "" || len(os.Args) < 2 || (os.Args[1] != "step" && os.Args[1] != "task" && os.Args[1] != "quality") {
		return
	}
	var d advanceDouble
	if err := json.Unmarshal([]byte(v), &d); err != nil {
		os.Exit(1)
	}
	seen := advanceSeen{Argv: os.Args[1:]}
	for end := time.Now().Add(deadline); d.Until != "" && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(d.Until); err == nil {
			seen.Unblocked = true
			break
		}
	}
	o, err := artisan.ReadOutcome(d.Dir)
	seen.Decided = o.State
	if err != nil {
		seen.ObservedErrs = append(seen.ObservedErrs, err.Error())
	}
	if s, err := store.Open(d.Store, store.Options{ReadOnly: true, LockTimeout: storeWait}); err != nil {
		seen.ObservedErrs = append(seen.ObservedErrs, err.Error())
	} else {
		if as, err := s.Attempts(key.Repository, key.TaskID); err == nil && len(as) == 1 {
			seen.Outcome = as[0].Outcome
			if own, err := s.Ownership(as[0].Repository, as[0].TaskID, as[0].Step, as[0].Number); err == nil {
				seen.Owner = own.State
			}
		}
		_ = s.Close()
	}
	b, _ := json.Marshal(seen)
	prior, _ := os.ReadFile(d.Record)
	run := strings.Count(string(prior), "\n")
	f, err := os.OpenFile(d.Record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	_, _ = os.Stdout.WriteString("advance double stdout\n")
	_, _ = fmt.Fprintf(os.Stderr, "advance double stderr\nadvance double run %d failed\n", run)
	if len(d.Exits) == 0 {
		os.Exit(0)
	}
	os.Exit(d.Exits[min(run, len(d.Exits)-1)])
}

// advanced sets the advance double for a over a new record file, its runs exiting with exits, and returns the
// record file and the Advance argv: the test binary with a copy option.
func advanced(t *testing.T, a launch.Attempt, exits ...int) (string, []string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "advances.jsonl")
	b, err := json.Marshal(advanceDouble{Record: record, Dir: a.Dir(), Store: a.Store, Exits: exits})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(advanceEnv, string(b))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return record, []string{exe, "--copy", "/the/copy"}
}

// advances reads the record file: one entry per advance child that ran.
func advances(t *testing.T, record string) []advanceSeen {
	t.Helper()
	f, err := os.Open(record)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []advanceSeen
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var s advanceSeen
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// advanceEvents reads the advance events of a's events.jsonl.
func advanceEvents(t *testing.T, a launch.Attempt) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.Dir(), artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for line := range strings.Lines(string(b)) {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e["event"] == "advance" {
			out = append(out, e)
		}
	}
	return out
}

func startAdvancing(a launch.Attempt, cmd adapter.Command, advance []string) (<-chan ran, context.CancelFunc) {
	return startAdvancingTo(a, cmd, advance, nil)
}

// startAdvancingTo is startAdvancing with the launcher's outbox.
func startAdvancingTo(a launch.Attempt, cmd adapter.Command, advance []string, outbox interp.Outbox) (<-chan ran, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ran, 1)
	go func() {
		r, err := launch.Run(ctx, launch.Config{Attempt: a, Command: cmd, Advance: advance, Ladder: fastLadder, ResultGrace: time.Second, HeartbeatInterval: 100 * time.Millisecond,
			Outbox: outbox})
		done <- ran{r, err}
	}()
	return done, cancel
}

var stepDone = []string{"step", "done", "--copy", "/the/copy", "--repo", "app", "--task", "t-1", "--step", "build", "--attempt", "1"}
var taskStart = []string{"task", "start", "--copy", "/the/copy", "--repo", "app", "t-1"}

// checkOneAdvance checks the advance ran once, with argv, after the attempt was decided state, recorded outcome
// and released, logging an advance event and its output to the launcher log.
func checkOneAdvance(t *testing.T, a launch.Attempt, record string, argv []string, state artisan.OutcomeState, outcome store.AttemptOutcome) {
	t.Helper()
	got := advances(t, record)
	if len(got) != 1 {
		t.Fatalf("advances %+v, want one", got)
	}
	if !slices.Equal(got[0].Argv, argv) {
		t.Errorf("advance argv %q\nwant %q", got[0].Argv, argv)
	}
	if got[0].Decided != state || got[0].Outcome != outcome || got[0].Owner != store.OwnerReleased {
		t.Errorf("the advance saw %+v; want outcome.json %s, the store outcome %s and the ownership released", got[0], state, outcome)
	}
	if ev := advanceEvents(t, a); len(ev) != 1 || ev[0]["command"] != argv[0]+" "+argv[1] {
		t.Errorf("advance events %v, want one naming %s %s", ev, argv[0], argv[1])
	}
	log, err := os.ReadFile(filepath.Join(a.Dir(), launch.LogFile))
	if err != nil || !strings.Contains(string(log), "advance double stdout") || !strings.Contains(string(log), "advance double stderr") {
		t.Errorf("launcher log %q, %v; want the advance's output", log, err)
	}
}

// TestRunAdvancesAFinishedAttemptWithItsResult: after a finished attempt with a valid result, succeeded or failed,
// the launcher runs step done for it once, with its result file, after recording and releasing it.
func TestRunAdvancesAFinishedAttemptWithItsResult(t *testing.T) {
	for _, status := range []adapter.Status{adapter.StatusSucceeded, adapter.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			a := newAttempt(t)
			record, advance := advanced(t, a, 0)
			cmd, _ := worker(t, a, artisantest.Worker{Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), status), ResultAfter: 200 * time.Millisecond})
			done, cancel := startAdvancing(a, cmd, advance)
			defer cancel()
			r := wait(t, done)
			if r.err != nil || r.r.AttemptOutcome != store.AttemptOutcome(status) {
				t.Fatalf("Run() = %+v, %v", r.r, r.err)
			}
			checkOneAdvance(t, a, record, append(slices.Clone(stepDone), "--result", a.Files().ResultFile), artisan.Finished, store.AttemptOutcome(status))
		})
	}
}

// TestRunAdvancesAJudgeAttemptWithQualityJudged: a judge attempt of a delivered task, whatever its end, is advanced
// once with quality judged for its attempt, never with step done or task start, which a done task refuses.
func TestRunAdvancesAJudgeAttemptWithQualityJudged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		w       func(a launch.Attempt) artisantest.Worker
		state   artisan.OutcomeState
		outcome store.AttemptOutcome
	}{
		{"succeeded", func(a launch.Attempt) artisantest.Worker {
			return artisantest.Worker{Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), adapter.StatusSucceeded), ResultAfter: 200 * time.Millisecond}
		}, artisan.Finished, store.AttemptSucceeded},
		{"no result", func(launch.Attempt) artisantest.Worker { return artisantest.Worker{Lifetime: 200 * time.Millisecond} },
			artisan.Failed, store.AttemptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newJudgeAttempt(t)
			record, advance := advanced(t, a, 0)
			cmd, _ := worker(t, a, tc.w(a))
			done, cancel := startAdvancing(a, cmd, advance)
			defer cancel()
			if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != tc.outcome {
				t.Fatalf("Run() = %+v, %v", r.r, r.err)
			}
			judged := []string{"quality", "judged", "--copy", "/the/copy", "--repo", "app", "--task", "t-1", "--attempt", "1"}
			checkOneAdvance(t, a, record, judged, tc.state, tc.outcome)
		})
	}
}

// TestRunAdvancesAFailedAttemptWithTaskStart: a worker exiting without a result fails the attempt, and the
// launcher runs task start so the engine sends attempt-failed.
func TestRunAdvancesAFailedAttemptWithTaskStart(t *testing.T) {
	a := newAttempt(t)
	record, advance := advanced(t, a, 0)
	cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 200 * time.Millisecond})
	done, cancel := startAdvancing(a, cmd, advance)
	defer cancel()
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
		t.Fatalf("Run() = %+v, %v", r.r, r.err)
	}
	checkOneAdvance(t, a, record, taskStart, artisan.Failed, store.AttemptFailed)
}

// TestRunAdvancesACancelledAttemptOnlyAfterItsDecision: a stop through the endpoint is advanced with task start,
// never before the marker decision is published and the stopped outcome recorded.
func TestRunAdvancesACancelledAttemptOnlyAfterItsDecision(t *testing.T) {
	a := newAttempt(t)
	record, advance := advanced(t, a, 0)
	cmd, _ := worker(t, a, artisantest.Worker{Grandchildren: 1, CancelMarker: a.Files().ResultFile + ".cancelled"})
	done, cancel := startAdvancing(a, cmd, advance)
	defer cancel()
	running(t, a)
	if got := advances(t, record); len(got) != 0 {
		t.Fatalf("advanced a running attempt: %+v", got)
	}
	if _, err := launch.Stop(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptStopped {
		t.Fatalf("Run() = %+v, %v", r.r, r.err)
	}
	checkOneAdvance(t, a, record, taskStart, artisan.Cancelled, store.AttemptStopped)
}

// TestRunAdvanceFailingTwiceIsToldToTheDirector (F35): an advance that fails is run once more, and when that run
// fails too the launcher sends the Director one advance-failed message naming the command and its last error,
// through its outbox with no task lock; both runs are logged, with their exit status in the events and their output
// in the launcher log, and neither changes Run's result nor the recorded, released attempt.
func TestRunAdvanceFailingTwiceIsToldToTheDirector(t *testing.T) {
	a := newAttempt(t)
	record, advance := advanced(t, a, 3)
	cmd, _ := worker(t, a, artisantest.Worker{Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), adapter.StatusSucceeded), ResultAfter: 200 * time.Millisecond})
	done, cancel := startAdvancingTo(a, cmd, advance, storeOutbox(a.Store))
	defer cancel()
	r := wait(t, done)
	if r.err != nil || r.r.AttemptOutcome != store.AttemptSucceeded || storedOutcome(t, a) != store.AttemptSucceeded {
		t.Fatalf("Run() = %+v, %v; stored %s; want succeeded with no error", r.r, r.err, storedOutcome(t, a))
	}
	if o := ownership(t, a); o.State != store.OwnerReleased {
		t.Errorf("ownership %+v, want released", o)
	}
	argv := append(slices.Clone(stepDone), "--result", a.Files().ResultFile)
	got := advances(t, record)
	if len(got) != 2 || !slices.Equal(got[0].Argv, argv) || !slices.Equal(got[1].Argv, argv) {
		t.Fatalf("advances %+v, want two of %q", got, argv)
	}
	ev := advanceEvents(t, a)
	if len(ev) != 2 {
		t.Fatalf("advance events %v, want two", ev)
	}
	for _, e := range ev {
		if e["command"] != "step done" || e["exit"] != float64(3) || e["error"] == "" || e["error"] == nil {
			t.Errorf("advance event %v, want step done, exit 3 and the error", e)
		}
	}
	msgs := inbox(t, a)
	if len(msgs) != 1 {
		t.Fatalf("Director messages %+v, want one advance-failed", msgs)
	}
	m := msgs[0]
	if m.ID != "app.t-1.advance-failed.build-1" || m.Type != store.MessageAdvanceFailed || m.Repository != "app" || m.TaskID != "t-1" ||
		!strings.Contains(m.Text, "step done") || !strings.Contains(m.Text, "advance double run 1 failed") {
		t.Errorf("message %+v; want advance-failed for build-1 naming step done and the second run's last error", m)
	}
}

// TestRunAdvanceRetriedOnceSendsNothing: an advance that fails once and then succeeds is run twice, and the Director
// is sent nothing.
func TestRunAdvanceRetriedOnceSendsNothing(t *testing.T) {
	a := newAttempt(t)
	record, advance := advanced(t, a, 1, 0)
	cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 200 * time.Millisecond})
	done, cancel := startAdvancingTo(a, cmd, advance, storeOutbox(a.Store))
	defer cancel()
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
		t.Fatalf("Run() = %+v, %v", r.r, r.err)
	}
	if got := advances(t, record); len(got) != 2 || !slices.Equal(got[1].Argv, taskStart) {
		t.Errorf("advances %+v, want two of %q", got, taskStart)
	}
	if ev := advanceEvents(t, a); len(ev) != 2 || ev[0]["exit"] != float64(1) || ev[1]["exit"] != float64(0) {
		t.Errorf("advance events %v, want exit 1 then 0", ev)
	}
	if msgs := inbox(t, a); len(msgs) != 0 {
		t.Errorf("Director messages %+v, want none", msgs)
	}
}

// inbox returns the Director's unread messages in a's store.
func inbox(t *testing.T, a launch.Attempt) []store.Message {
	t.Helper()
	var out []store.Message
	withStore(t, a, func(s *store.Store) {
		msgs, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		out = msgs
	})
	return out
}

// TestRunWithoutAdvanceRunsNone: a nil Advance, as `artisan start` run by hand without --advance, runs nothing.
func TestRunWithoutAdvanceRunsNone(t *testing.T) {
	a := newAttempt(t)
	record, _ := advanced(t, a, 0)
	cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 200 * time.Millisecond})
	done, cancel := startAdvancing(a, cmd, nil)
	defer cancel()
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
		t.Fatalf("Run() = %+v, %v", r.r, r.err)
	}
	if got := advances(t, record); len(got) != 0 {
		t.Errorf("advances %+v, want none", got)
	}
}

// TestProcessLaunchReturnsOnceItsStoppedLauncherReleases is a review regression written red first: a Process
// from a copy whose launcher does not record its endpoint within StartTimeout stops that launcher and returns
// once it releases the stopped attempt, not once it exits. The launcher's advance, task start, waits for the
// task lock Launch's caller holds, which the double plays by waiting for a file the test writes after Launch
// returns; Launch waiting for the launcher to exit stalled until the advance gave up.
func TestProcessLaunchReturnsOnceItsStoppedLauncherReleases(t *testing.T) {
	dir, err := os.MkdirTemp("", "lc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := newProcess(t)
	p.Store, p.StateDir, p.StartTimeout = filepath.Join(dir, "director.store"), filepath.Join(dir, "state"), time.Millisecond
	spec := prepared(t, p, fixtureSpec(t, instructions[interptest.Hangs]))
	spec.Copy = dir
	k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
	record, unlocked := filepath.Join(t.TempDir(), "advances.jsonl"), filepath.Join(t.TempDir(), "unlocked")
	b, err := json.Marshal(advanceDouble{Record: record, Dir: k.Dir(p.StateDir), Store: p.Store, Until: unlocked})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(advanceEnv, string(b))
	begun := time.Now()
	_, err = p.Launch(context.Background(), spec)
	took := time.Since(begun)
	if werr := os.WriteFile(unlocked, nil, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if err == nil {
		t.Fatal("Launch succeeded past its StartTimeout")
	}
	if took > testenv.Ceiling(10*time.Second) {
		t.Errorf("Launch took %v to stop its launcher; want no wait for the launcher's advance", took)
	}
	s, err := store.Open(p.Store, store.Options{ReadOnly: true, LockTimeout: storeWait})
	if err != nil {
		t.Fatal(err)
	}
	o, oerr := s.Ownership(k.Repository, k.TaskID, k.Step, k.Attempt)
	as, aerr := s.Attempts(k.Repository, k.TaskID)
	_ = s.Close()
	if oerr != nil || aerr != nil || o.State != store.OwnerReleased || len(as) != 1 || as[0].Outcome != store.AttemptStopped {
		t.Fatalf("ownership %+v, %v; attempts %+v, %v; want released and stopped", o, oerr, as, aerr)
	}
	id := artisan.Identity(o.LauncherIdentity)
	for end := time.Now().Add(deadline); ; time.Sleep(50 * time.Millisecond) {
		if lv, err := artisan.Probe(id); err == nil && lv == artisan.Gone {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("launcher %d did not exit within %v", id.PID, deadline)
		}
	}
	got := advances(t, record)
	want := []string{"task", "start", "--copy", dir, "--repo", "app", "t7"}
	if len(got) != 1 || !slices.Equal(got[0].Argv, want) || !got[0].Unblocked {
		t.Errorf("advances %+v; want one %q run after Launch returned", got, want)
	}
}
