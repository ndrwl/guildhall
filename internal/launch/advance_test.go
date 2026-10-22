package launch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// advanceEnv makes the test binary play `guildhall step done` and `guildhall task start` for Run's advance:
// it appends what it saw (advanceSeen) to the record file the JSON advanceDouble in the variable names, prints a
// line on each output and exits with the double's code.
const advanceEnv = "GUILDHALL_LAUNCH_TEST_ADVANCE"

type advanceDouble struct {
	Record, Dir, Store string
	Exit               int
}

// advanceSeen is one advance child's view: its arguments, the attempt's outcome.json state, and the store's
// attempt outcome and ownership state, read as the child starts.
type advanceSeen struct {
	Argv         []string             `json:"argv"`
	Decided      artisan.OutcomeState `json:"decided"`
	Outcome      store.AttemptOutcome `json:"outcome"`
	Owner        store.OwnershipState `json:"owner"`
	ObservedErrs []string             `json:"errors,omitempty"`
}

func mainIfAdvance() {
	v := os.Getenv(advanceEnv)
	if v == "" || len(os.Args) < 2 || (os.Args[1] != "step" && os.Args[1] != "task") {
		return
	}
	var d advanceDouble
	if err := json.Unmarshal([]byte(v), &d); err != nil {
		os.Exit(1)
	}
	seen := advanceSeen{Argv: os.Args[1:]}
	o, err := artisan.ReadOutcome(d.Dir)
	seen.Decided = o.State
	if err != nil {
		seen.ObservedErrs = append(seen.ObservedErrs, err.Error())
	}
	if s, err := store.Open(d.Store, store.Options{ReadOnly: true}); err != nil {
		seen.ObservedErrs = append(seen.ObservedErrs, err.Error())
	} else {
		if as, err := s.Attempts(key.Repository, key.TaskID); err == nil && len(as) == 1 {
			seen.Outcome = as[0].Outcome
		}
		if own, err := s.Ownership(key.Repository, key.TaskID, key.Step, key.Attempt); err == nil {
			seen.Owner = own.State
		}
		_ = s.Close()
	}
	b, _ := json.Marshal(seen)
	f, err := os.OpenFile(d.Record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	_, _ = os.Stdout.WriteString("advance double stdout\n")
	_, _ = os.Stderr.WriteString("advance double stderr\n")
	os.Exit(d.Exit)
}

// advanced sets the advance double for a over a new record file, exiting with exit, and returns the record file
// and the Advance argv: the test binary with a copy option.
func advanced(t *testing.T, a launch.Attempt, exit int) (string, []string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "advances.jsonl")
	b, err := json.Marshal(advanceDouble{Record: record, Dir: a.Dir(), Store: a.Store, Exit: exit})
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ran, 1)
	go func() {
		r, err := launch.Run(ctx, launch.Config{Attempt: a, Command: cmd, Advance: advance, Ladder: fastLadder, ResultGrace: time.Second, HeartbeatInterval: 100 * time.Millisecond})
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

// TestRunAdvanceFailureLeavesTheOutcome: an advance that fails is logged, with its exit status in the event and
// its output in the launcher log, and changes neither Run's result nor the recorded, released attempt.
func TestRunAdvanceFailureLeavesTheOutcome(t *testing.T) {
	a := newAttempt(t)
	record, advance := advanced(t, a, 3)
	cmd, _ := worker(t, a, artisantest.Worker{Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), adapter.StatusSucceeded), ResultAfter: 200 * time.Millisecond})
	done, cancel := startAdvancing(a, cmd, advance)
	defer cancel()
	r := wait(t, done)
	if r.err != nil || r.r.AttemptOutcome != store.AttemptSucceeded || storedOutcome(t, a) != store.AttemptSucceeded {
		t.Fatalf("Run() = %+v, %v; stored %s; want succeeded with no error", r.r, r.err, storedOutcome(t, a))
	}
	if o := ownership(t, a); o.State != store.OwnerReleased {
		t.Errorf("ownership %+v, want released", o)
	}
	checkOneAdvance(t, a, record, append(slices.Clone(stepDone), "--result", a.Files().ResultFile), artisan.Finished, store.AttemptSucceeded)
	if ev := advanceEvents(t, a); len(ev) != 1 || ev[0]["exit"] != float64(3) || ev[0]["error"] == "" || ev[0]["error"] == nil {
		t.Errorf("advance events %v, want exit 3 and the error", ev)
	}
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
