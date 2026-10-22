package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/skills"
)

// recordSession records a Director session in the store at path, run by this process unless gone, and moves
// it through states.
func recordSession(t *testing.T, path string, gone bool, states ...store.DirectorState) {
	t.Helper()
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	if gone {
		self.Token = "a supervisor that has exited"
	}
	s, err := store.Open(path, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d := store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity(self), Release: "test",
		ConfigDigest: "sha256:" + strings.Repeat("c", 64)}
	if _, err := s.StartDirectorSession(d); err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		d, err := s.DirectorSession(1)
		if err != nil {
			t.Fatal(err)
		}
		if st == store.DirectorStopping {
			_, err = s.RequestDirectorStop(1, false, d.StateSequence)
		} else {
			_, err = s.SetDirectorState(1, st, d.StateSequence)
		}
		if err != nil {
			t.Fatalf("move to %s: %v", st, err)
		}
	}
}

// latestSession reads the newest session and the store's newest receipt sequence.
func latestSession(t *testing.T, path string) (store.DirectorSession, uint64) {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	var d store.DirectorSession
	if v.Session != nil {
		d = *v.Session
	}
	return d, v.AsOf.Sequence
}

// TestDirectorPauseResumeStop is the state table of the three commands: each move, what each prints, and every
// refusal, which leaves the store unchanged.
func TestDirectorPauseResumeStop(t *testing.T) {
	type want struct {
		code    int
		state   store.DirectorState
		now     bool
		reason  string
		message string // in stderr when refused
	}
	running := []store.DirectorState{store.DirectorRunning}
	paused := []store.DirectorState{store.DirectorRunning, store.DirectorPaused}
	stopping := []store.DirectorState{store.DirectorRunning, store.DirectorStopping}
	stopped := []store.DirectorState{store.DirectorStopped}
	cases := []struct {
		name    string
		none    bool
		gone    bool
		path    []store.DirectorState
		command []string
		want    want
	}{
		{"pause with no session", true, false, nil, []string{"pause"}, want{code: 1, message: "no Director session"}},
		{"resume with no session", true, false, nil, []string{"resume"}, want{code: 1, message: "no Director session"}},
		{"stop with no session", true, false, nil, []string{"stop"}, want{code: 1, message: "no Director session"}},
		{"pause recovering", false, false, nil, []string{"pause"}, want{code: 1, state: store.DirectorRecovering, message: "director ready"}},
		{"resume recovering", false, false, nil, []string{"resume"}, want{code: 1, state: store.DirectorRecovering, message: "director ready"}},
		{"stop recovering", false, false, nil, []string{"stop"}, want{state: store.DirectorStopping}},
		{"pause running", false, false, running, []string{"pause"}, want{state: store.DirectorPaused}},
		{"resume running", false, false, running, []string{"resume"}, want{state: store.DirectorRunning}},
		{"stop running", false, false, running, []string{"stop"}, want{state: store.DirectorStopping}},
		{"stop running now", false, false, running, []string{"stop", "--now"}, want{state: store.DirectorStopping, now: true}},
		{"pause paused", false, false, paused, []string{"pause"}, want{state: store.DirectorPaused}},
		{"resume paused", false, false, paused, []string{"resume"}, want{state: store.DirectorRunning}},
		{"stop paused", false, false, paused, []string{"stop"}, want{state: store.DirectorStopping}},
		{"pause stopping", false, false, stopping, []string{"pause"}, want{code: 1, state: store.DirectorStopping, message: "stopping"}},
		{"resume stopping", false, false, stopping, []string{"resume"}, want{code: 1, state: store.DirectorStopping, message: "stopping"}},
		{"stop stopping", false, false, stopping, []string{"stop"}, want{state: store.DirectorStopping}},
		{"stop stopping now", false, false, stopping, []string{"stop", "--now"}, want{state: store.DirectorStopping, now: true}},
		{"pause stopped", false, false, stopped, []string{"pause"}, want{code: 1, state: store.DirectorStopped, message: "stopped"}},
		{"resume stopped", false, false, stopped, []string{"resume"}, want{code: 1, state: store.DirectorStopped, message: "stopped"}},
		{"stop stopped", false, false, stopped, []string{"stop"}, want{code: 1, state: store.DirectorStopped, message: "stopped"}},
		{"pause gone", false, true, running, []string{"pause"}, want{code: 1, state: store.DirectorRunning, message: "director stop"}},
		{"resume gone", false, true, paused, []string{"resume"}, want{code: 1, state: store.DirectorPaused, message: "director stop"}},
		{"stop gone", false, true, running, []string{"stop"}, want{state: store.DirectorStopped, reason: "superseded"}},
		{"stop gone now", false, true, paused, []string{"stop", "--now"}, want{state: store.DirectorStopped, reason: "superseded"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := emptyStore(t)
			state := t.TempDir()
			if !c.none {
				recordSession(t, path, c.gone, c.path...)
			}
			before, seq := latestSession(t, path)
			args := append([]string{"director"}, c.command...)
			args = append(args, "--store", path)
			if c.command[0] != "pause" {
				args = append(args, "--state", state)
			}
			code, out, stderr := runCLI(t, args...)
			after, afterSeq := latestSession(t, path)
			if code != c.want.code {
				t.Fatalf("exit %d, want %d\nstdout %s\nstderr %s", code, c.want.code, out, stderr)
			}
			if after.State != c.want.state || after.StopNow != c.want.now || after.EndReason != c.want.reason {
				t.Fatalf("session = %+v; want state %q, stop_now %v, end reason %q", after, c.want.state, c.want.now, c.want.reason)
			}
			if code != 0 {
				if !strings.Contains(stderr, c.want.message) {
					t.Errorf("stderr %q does not name %q", stderr, c.want.message)
				}
				if after != before || afterSeq != seq {
					t.Errorf("a refusal changed the store: %+v at %d -> %+v at %d", before, seq, after, afterSeq)
				}
				return
			}
			var printed store.DirectorSession
			if c.command[0] == "resume" {
				var doc struct {
					Session store.DirectorSession `json:"session"`
					NeedsMe []needsMeItem         `json:"needs_me"`
				}
				if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.NeedsMe == nil {
					t.Fatalf("resume printed %q (%v); want a session and needs_me", out, err)
				}
				printed = doc.Session
			} else if err := json.Unmarshal([]byte(out), &printed); err != nil {
				t.Fatalf("printed %q: %v", out, err)
			}
			if printed != after || strings.Count(out, "\n") != 1 {
				t.Errorf("printed %q; want the session %+v as one line", out, after)
			}
		})
	}
}

// runCLI runs the CLI in this process with the test binary as the guildhall executable.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.InstallSkills = func(string) error { return nil }
	app.InstallBaseline = baseline.Install
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

// TestDirectorResumeListsHeldTasks: a task whose last advance was held waits paused in its next.json, and both
// resume and the status feed list it with the task start that starts its held attempt.
func TestDirectorResumeListsHeldTasks(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning, store.DirectorPaused)
	if err := os.MkdirAll(filepath.Join(d.state, "app", "t-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d.state, "app", "t-1", "next.json"), `{"wait":"paused","step":"build"}`+"\n")
	want := needsMeItem{Kind: "paused", Repository: "app", TaskID: "t-1", Reason: "the last advance was held while the Director was paused",
		Command: "guildhall task start --repo app t-1"}
	code, out, stderr := runCLI(t, "director", "status", "--json", "--store", d.store, "--state", d.state)
	var status directorStatus
	if err := json.Unmarshal([]byte(out), &status); code != 0 || err != nil {
		t.Fatalf("status: exit %d, %v\n%s", code, err, stderr)
	}
	if len(status.NeedsMe) != 1 || status.NeedsMe[0] != want {
		t.Errorf("status needs_me = %+v, want [%+v]", status.NeedsMe, want)
	}
	code, out, stderr = runCLI(t, "director", "resume", "--store", d.store, "--state", d.state)
	var doc struct {
		NeedsMe []needsMeItem `json:"needs_me"`
	}
	if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil {
		t.Fatalf("resume: exit %d, %v\n%s", code, err, stderr)
	}
	if len(doc.NeedsMe) != 1 || doc.NeedsMe[0] != want {
		t.Errorf("resume needs_me = %+v, want [%+v]", doc.NeedsMe, want)
	}
}

// TestTaskStartWhilePausedLaunchesNothing: while the Director is paused, task start and step retry print
// {"wait":"paused"} and record nothing; after resume, task start launches the task's first attempt.
func TestTaskStartWhilePausedLaunchesNothing(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, out, stderr := d.run(t, "director", "pause", "--store", d.store); code != 0 {
		t.Fatalf("pause: exit %d, %s %s", code, out, stderr)
	}
	_, seq := latestSession(t, d.store)
	for _, args := range [][]string{
		append([]string{"task", "start"}, d.flags("t-1")...),
		append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1")...),
	} {
		if code, out, stderr := d.run(t, args...); code != 0 || out != `{"wait":"paused"}`+"\n" {
			t.Fatalf("%s %s while paused: exit %d, %q, stderr %s", args[0], args[1], code, out, stderr)
		}
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("attempts while paused = %+v", got)
	}
	if _, after := latestSession(t, d.store); after != seq {
		t.Fatalf("the store moved from sequence %d to %d while paused", seq, after)
	}
	if _, err := os.Stat(filepath.Join(d.state, "app", "t-1", "next.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("next.json while paused: %v, want none", err)
	}
	if code, out, stderr := d.run(t, "director", "resume", "--store", d.store, "--state", d.state); code != 0 {
		t.Fatalf("resume: exit %d, %s %s", code, out, stderr)
	}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start after resume: exit %d, %q, stderr %s", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 1 {
		t.Fatalf("attempts after resume = %+v", got)
	}
	d.waitResult(t, "plan", 1)
}

// TestDecisionAnsweredWhilePausedIsFed: while the Director is paused, task start --decision still feeds the
// Guildmaster's answer but starts no next attempt; next.json waits paused, needs_me lists the task, and after
// resume its task start starts the held build.
func TestDecisionAnsweredWhilePausedIsFed(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning)
	step := func(args ...string) string {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 {
			t.Fatalf("guildhall %q: exit %d, %s", args, code, stderr)
		}
		return out
	}
	step(append([]string{"task", "start"}, d.flags("t-1")...)...)
	d.waitResult(t, "plan", 1)
	step(append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)...)
	step("director", "pause", "--store", d.store)
	answer := filepath.Join(t.TempDir(), "answer")
	writeFile(t, answer, "approved\n")
	step("decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", "accept-1", "--answer-file", answer)
	const held = `{"wait":"paused","step":"build"}` + "\n"
	if out := step(append([]string{"task", "start"}, d.flags("--decision", "accept-1", "t-1")...)...); out != held {
		t.Fatalf("task start --decision while paused = %q, want %q", out, held)
	}
	if got := d.attempts(t); len(got) != 1 {
		t.Fatalf("attempts while paused = %+v, want plan-1 alone", got)
	}
	if b, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "next.json")); err != nil || string(b) != held {
		t.Fatalf("next.json = %q, %v; want %q", b, err, held)
	}
	var doc struct {
		NeedsMe []needsMeItem `json:"needs_me"`
	}
	if err := json.Unmarshal([]byte(step("director", "resume", "--store", d.store, "--state", d.state)), &doc); err != nil ||
		!slices.ContainsFunc(doc.NeedsMe, func(i needsMeItem) bool { return i.Kind == "paused" && i.TaskID == "t-1" }) {
		t.Fatalf("resume needs_me = %+v, %v; want t-1 paused", doc.NeedsMe, err)
	}
	if out := step(append([]string{"task", "start"}, d.flags("t-1")...)...); out != `{"wait":"attempt","step":"build","attempt":1}`+"\n" {
		t.Fatalf("task start after resume = %q", out)
	}
	d.waitResult(t, "build", 1)
}

// TestHeldWhilePausedOrStopping: the advance's hold holds new attempts only while the newest session is paused
// or stopping.
func TestHeldWhilePausedOrStopping(t *testing.T) {
	for _, c := range []struct {
		path []store.DirectorState
		held bool
	}{
		{nil, false},
		{[]store.DirectorState{store.DirectorRunning}, false},
		{[]store.DirectorState{store.DirectorRunning, store.DirectorPaused}, true},
		{[]store.DirectorState{store.DirectorRunning, store.DirectorStopping}, true},
		{[]store.DirectorState{store.DirectorStopped}, false},
	} {
		path := emptyStore(t)
		recordSession(t, path, false, c.path...)
		got, err := withStore(path, true, held)
		if err != nil || got != c.held {
			t.Errorf("after %v: held = %v, %v; want %v", c.path, got, err, c.held)
		}
	}
	got, err := withStore(emptyStore(t), true, held)
	if err != nil || got {
		t.Errorf("no session: held = %v, %v; want false", got, err)
	}
}

// supervisedCopy is a short-pathed Director copy whose fake Director says it is ready and then waits on its
// input, with task app/t-1 recorded and its plan stage running until it is stopped.
func supervisedCopy(t *testing.T) (string, runDirector) {
	t.Helper()
	long := directorCopy(t, "[director]\nadapter = \"fake\"\n", "guildhall director ready")
	dir, err := os.MkdirTemp("", "gd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(long)); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	guide, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, plan, string(guide)+"guildhall-fake: result=never\nguildhall-fake: lifetime=1m\n")
	cfg := filepath.Join(dir, "config.toml")
	if appRepo != "/srv/app" {
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, cfg, strings.Replace(string(b), `path = "/srv/app"`, "path = '"+appRepo+"'", 1))
	}
	ws := gittest.NewWorkspaces()
	ws.SetDefault("git@github.com:example/app.git", strings.Repeat("d", 40))
	d := runDirector{store: filepath.Join(dir, "director.store"), config: cfg, state: filepath.Join(dir, "state"), workspaces: ws}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(dir)
	})
	newTask(t, d.store, cfg, "app", "Sort the task list", "t-1")
	return dir, d
}

// supervise runs `director start` over dir in this process with grace as its stop grace, its
// runtime reading a pipe that stays open, and returns its exit status once it ends.
func supervise(t *testing.T, dir string, grace time.Duration) <-chan int {
	t.Helper()
	return superviseWith(t, dir, grace, nil)
}

// superviseWith is supervise with the App configured by configure, when it is set.
func superviseWith(t *testing.T, dir string, grace time.Duration, configure func(*App)) <-chan int {
	t.Helper()
	stdin, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = typing.Close() })
	app, _, _ := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.LookPath = exec.LookPath
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	app.Stdin = stdin
	app.Stdout, app.Stderr = io.Discard, io.Discard
	app.SessionPoll, app.StopGrace = 100*time.Millisecond, grace
	if configure != nil {
		configure(app)
	}
	done := make(chan int, 1)
	go func() { done <- app.Run([]string{"director", "start", "--copy", dir}) }()
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(50 * time.Millisecond) {
		if got := sessions(t, dir); len(got) == 1 && got[0].State == store.DirectorRunning {
			return done
		}
		if time.Now().After(end) {
			t.Fatalf("the session never became ready: %+v", sessions(t, dir))
		}
	}
}

// ended waits for a supervisor's exit status.
func ended(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(testenv.Ceiling(2 * time.Minute)):
		t.Fatal("director start did not end after director stop")
		return 0
	}
}

// TestDirectorStopEndsTheSessionAndItsAttempts: with a live supervisor and a running plan attempt, director stop
// records the session stopping and stops the attempt; the supervisor ends its runtime, records the session
// stopped and exits 0, and no new attempt starts.
func TestDirectorStopEndsTheSessionAndItsAttempts(t *testing.T) {
	dir, d := supervisedCopy(t)
	done := supervise(t, dir, time.Nanosecond)
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(20 * time.Millisecond) {
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		o, err := s.Ownership("app", "t-1", "plan", 1)
		_ = s.Close()
		if err == nil && o.State == store.OwnerRunning && o.Containment.Worker != (store.ProcessIdentity{}) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("plan-1 worker never ran: ownership %+v, %v", o, err)
		}
	}
	code, out, stderr := d.run(t, "director", "stop", "--copy", dir)
	var printed store.DirectorSession
	if err := json.Unmarshal([]byte(out), &printed); code != 0 || err != nil || printed.State != store.DirectorStopping || printed.StopNow {
		t.Fatalf("director stop: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(20 * time.Millisecond) {
		got := d.attempts(t)
		if len(got) == 1 && got[0].Outcome == store.AttemptStopped {
			break
		}
		if len(got) != 1 || time.Now().After(end) {
			t.Fatalf("attempts after director stop = %+v; want plan-1 stopped", got)
		}
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason != "" {
		t.Errorf("sessions = %+v; want session 1 stopped", got)
	}
}

// TestDirectorStopNowEndsTheRuntimeAtOnce: stop --now ends the runtime at the supervisor's next read, well before
// the hour's grace a plain stop would give it.
func TestDirectorStopNowEndsTheRuntimeAtOnce(t *testing.T) {
	dir, d := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	if code, out, stderr := d.run(t, "director", "stop", "--now", "--copy", dir); code != 0 {
		t.Fatalf("director stop --now: exit %d, %s %s", code, out, stderr)
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || !got[0].StopNow {
		t.Errorf("sessions = %+v; want session 1 stopped at once", got)
	}
}

// pausedInPane records session 1 in the store at path, run by this process, bound to pane w1:p1 and paused.
func pausedInPane(t *testing.T, path string) {
	t.Helper()
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	_, err = withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		r, err := s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity(self), Release: "test",
			ConfigDigest: "sha256:" + strings.Repeat("c", 64), Pane: store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t1", Pane: "w1:p1"}})
		if err == nil {
			r, err = s.SetDirectorState(1, store.DirectorRunning, r.Sequence)
		}
		if err == nil {
			r, err = s.SetDirectorState(1, store.DirectorPaused, r.Sequence)
		}
		return r, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDirectorResumeWakesQueuedWork: a note sent while the Director was paused is queued, and a task was held; a
// resume run from another terminal types one line of what waits into the Director's pane and prints that it woke
// it, leaving the note's queued wake as it was; a repeated resume of the running session types nothing more.
func TestDirectorResumeWakesQueuedWork(t *testing.T) {
	d := newRunDirector(t)
	pausedInPane(t, d.store)
	panes := &typedPanes{}
	d.panes = panes
	app, _, stderr := newTestApp(nil, nil)
	app.DirectorPane = panes
	app.Stdin = strings.NewReader(`{"schema":"guildhall.message/1","id":"n-1","recipient":"director","repository":"app","task":"t-1","type":"note","text":"Please do the queued work."}`)
	if code := app.Run([]string{"messages", "send", "--store", d.store, "director", "-"}); code != 0 {
		t.Fatalf("messages send: exit %d, %s", code, stderr)
	}
	writeNext(t, d.state, "t-1", `{"wait":"paused","step":"build"}`+"\n")
	queued := readWakes(t, d.store)
	if len(queued) != 1 || queued[0].Decision != store.WakeQueued || queued[0].Reason != "paused" || len(panes.lines) != 0 {
		t.Fatalf("while paused: wakes %+v, typed %q", queued, panes.lines)
	}
	code, out, se := d.run(t, "director", "resume", "--store", d.store, "--state", d.state)
	var doc struct {
		Session store.DirectorSession `json:"session"`
		NeedsMe []needsMeItem         `json:"needs_me"`
		Wake    *messaging.ResumeWake `json:"wake"`
	}
	if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil || doc.Session.State != store.DirectorRunning {
		t.Fatalf("resume: exit %d, %q (%v), stderr %s", code, out, err, se)
	}
	if doc.Wake == nil || *doc.Wake != (messaging.ResumeWake{Decision: store.WakeWoke}) {
		t.Errorf("resume printed wake %+v, want woke", doc.Wake)
	}
	if want := []string{"w1:p1 guildhall: resumed, 2 waiting (messages 1, paused 1)"}; !slices.Equal(panes.lines, want) {
		t.Errorf("typed %q, want %q", panes.lines, want)
	}
	if got := readWakes(t, d.store); !slices.Equal(got, queued) {
		t.Errorf("wakes after resume = %+v, want the queued wake unchanged", got)
	}
	code, out, se = d.run(t, "director", "resume", "--store", d.store, "--state", d.state)
	doc.Wake = nil
	if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil || doc.Wake != nil {
		t.Fatalf("repeated resume: exit %d, %q (%v), stderr %s; want no wake", code, out, err, se)
	}
	if len(panes.lines) > 1 {
		t.Errorf("a repeated resume typed %q", panes.lines[1:])
	}
}

// TestDirectorResumeWithNothingWaitingTypesNothing: a resume with nothing in needs_me types nothing and prints
// that it queued nothing waiting.
func TestDirectorResumeWithNothingWaitingTypesNothing(t *testing.T) {
	d := newRunDirector(t)
	pausedInPane(t, d.store)
	panes := &typedPanes{}
	d.panes = panes
	code, out, se := d.run(t, "director", "resume", "--store", d.store, "--state", d.state)
	var doc struct {
		Wake *messaging.ResumeWake `json:"wake"`
	}
	if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil {
		t.Fatalf("resume: exit %d, %q (%v), stderr %s", code, out, err, se)
	}
	if doc.Wake == nil || *doc.Wake != (messaging.ResumeWake{Decision: store.WakeQueued, Reason: "nothing waiting"}) || len(panes.lines) != 0 {
		t.Errorf("resume printed wake %+v and typed %q; want queued: nothing waiting", doc.Wake, panes.lines)
	}
}
