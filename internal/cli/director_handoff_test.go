package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// putRetiredHandoffNote writes handoff-645, a handoff note from before notes replaced them, into the store at path,
// as stores that ran an earlier release hold one.
func putRetiredHandoffNote(t *testing.T, path string) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	note, err := json.Marshal(store.HandoffNote{Session: 1, Sequence: 645, Path: "director/handoff-645.md", SHA256: strings.Repeat("0", 64), RecordedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		d, err := tx.CreateBucketIfNotExists([]byte("director"))
		if err != nil {
			return err
		}
		b, err := d.CreateBucketIfNotExists([]byte("handoffs"))
		if err != nil {
			return err
		}
		return b.Put([]byte("00000000000000000645"), note)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDirectorHandoffIsGone: `director handoff` is no longer a command (docs/decisions/director-reload-and-notes.md).
func TestDirectorHandoffIsGone(t *testing.T) {
	d := newRunDirector(t)
	if code, _, stderr := runCLI(t, "director", "handoff", "--note-file", "-", "--store", d.store, "--state", d.state); code != 2 || !strings.Contains(stderr, "the commands are") {
		t.Fatalf("director handoff: exit %d, stderr %s; want a usage error", code, stderr)
	}
}

// TestDirectorReload: a plain reload is recorded with or without a live session, to be applied by the next one;
// --now needs only a newest session that is not stopped, with no note, and upgrades the pending request.
func TestDirectorReload(t *testing.T) {
	d := newRunDirector(t)
	flags := []string{"--store", d.store, "--state", d.state}
	reload := func(extra ...string) (int, store.DirectorReload, string) {
		t.Helper()
		code, out, stderr := runCLI(t, append(append([]string{"director", "reload"}, extra...), flags...)...)
		var r store.DirectorReload
		if code == 0 {
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatalf("reload printed %q: %v", out, err)
			}
		}
		return code, r, stderr
	}
	code, plain, stderr := reload()
	if code != 0 || plain.State != store.ReloadRequested || plain.Now || plain.Sequence == 0 {
		t.Fatalf("plain reload with no session: exit %d, %+v, stderr %s", code, plain, stderr)
	}
	if code, _, stderr := reload("--now"); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Fatalf("--now with no session: exit %d, stderr %s", code, stderr)
	}
	recordSession(t, d.store, false, store.DirectorRunning)
	code, now, stderr := reload("--now")
	if code != 0 || !now.Now || now.Sequence != plain.Sequence || now.State != store.ReloadRequested {
		t.Fatalf("--now with no notes: exit %d, %+v, stderr %s; want the pending request upgraded", code, now, stderr)
	}
	if code, again, _ := reload(); code != 0 || again != now {
		t.Fatalf("plain reload after --now: exit %d, %+v; want %+v", code, again, now)
	}
	if code, _, _ := runCLI(t, "director", "reload", "extra", "--store", d.store, "--state", d.state); code != 2 {
		t.Errorf("reload with an argument: exit %d, want 2", code)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		cur, err := s.DirectorSession(1)
		if err != nil {
			return store.Receipt{}, err
		}
		return s.SetDirectorState(1, store.DirectorStopped, cur.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := reload("--now"); code != 1 || !strings.Contains(stderr, "session 1 is stopped") {
		t.Fatalf("--now for a stopped session: exit %d, stderr %s", code, stderr)
	}
}

// TestDirectorStatusNamesTheReload: the status feed's session section carries the newest reload request, and no
// handoff note or notes.
func TestDirectorStatusNamesTheReload(t *testing.T) {
	d := newRunDirector(t)
	flags := []string{"--store", d.store, "--state", d.state}
	recordSession(t, d.store, false, store.DirectorRunning)
	status := func() map[string]json.RawMessage {
		t.Helper()
		code, out, stderr := runCLI(t, append([]string{"director", "status", "--json"}, flags...)...)
		var doc struct {
			Session map[string]json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil {
			t.Fatalf("status: exit %d, %q (%v), stderr %s", code, out, err, stderr)
		}
		return doc.Session
	}
	if s := status(); s["handoff_note"] != nil || s["reload"] != nil {
		t.Fatalf("session before any reload = %s", s)
	}
	if code, _, stderr := runCLI(t, "director", "note", "add", "--kind", "plan", "A note.", "--store", d.store); code != 0 {
		t.Fatalf("note add: exit %d, stderr %s", code, stderr)
	}
	code, out, _ := runCLI(t, append([]string{"director", "reload"}, flags...)...)
	var r store.DirectorReload
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil {
		t.Fatalf("reload: exit %d, %q", code, out)
	}
	s := status()
	var gotReload store.DirectorReload
	if err := json.Unmarshal(s["reload"], &gotReload); err != nil || gotReload != r {
		t.Errorf("status reload = %s, want %+v", s["reload"], r)
	}
	if s["handoff_note"] != nil || s["notes"] != nil {
		t.Errorf("status session = %s; want no handoff note or notes", s)
	}
}

// TestDirectorReloadNowDuringAStage: --now is admitted while a Director stage attempt runs; the next session
// restarts the grill (docs/decisions/director-reload-and-notes.md).
func TestDirectorReloadNowDuringAStage(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	flags := []string{"--store", d.store, "--state", d.state}
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := runCLI(t, append([]string{"director", "reload", "--now"}, flags...)...); code != 0 {
		t.Fatalf("--now mid-grill: exit %d, %q, stderr %s", code, out, stderr)
	}
}

// TestPlainReloadAppliesWithAGrillOpen: a grill waiting on the Guildmaster does not hold a plain reload, which
// applies at the next quiet point and leaves the grill attempt unfinished and handed for the next session to
// restart (docs/decisions/director-reload-and-notes.md).
func TestPlainReloadAppliesWithAGrillOpen(t *testing.T) {
	dir, d := supervisedCopy(t)
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(b), `workflow = "fake-straight"`, `workflow = "fake-grill"`, 1))
	// fake-grill has no validate step, so it is below the review floor; t-1 was created on fake-straight by
	// supervisedCopy.
	approveRepo(t, d.store, cfg, "app")
	newTask(t, d.store, cfg, "app", "Grill first", "t-2")
	done := supervise(t, dir, 0)
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-2")...)...)
	if code != 0 || !strings.Contains(out, `"wait":"director-stage"`) {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	var started struct {
		Step    string `json:"step"`
		Attempt int    `json:"attempt"`
	}
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	copyRun(t, dir, "director", "reload")
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	if r := readReload(t, d.store); r.State != store.ReloadApplied || r.OldSession != 1 || r.NewSession != 2 || r.Now {
		t.Errorf("reload = %+v; want a plain reload applied from session 1 to 2", r)
	}
	s, err := store.Open(d.store, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.DirectorView()
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, at := range v.Attempts {
		if at.TaskID == "t-2" && at.Step == started.Step && at.Number == started.Attempt {
			found = true
			if at.Outcome != "" {
				t.Errorf("grill attempt = %+v; want it unfinished", at)
			}
		}
	}
	if !found {
		t.Errorf("attempts = %+v; want attempt %d of %s of t-2", v.Attempts, started.Attempt, started.Step)
	}
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-2", Step: started.Step, Attempt: started.Attempt}
	if handed, err := interp.HandedToDirector(k.Dir(d.state)); err != nil || !handed {
		t.Errorf("grill attempt handed = %v, %v; want still handed to the Director", handed, err)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
}

// TestDirectorTurnSignalsNeverExitTwo: a Claude Code Stop hook reads exit 2 as "keep going" and a
// UserPromptSubmit hook as "block the prompt", so turn-ended and turn-started report a usage error and no
// supervisor with exit 1.
func TestDirectorTurnSignalsNeverExitTwo(t *testing.T) {
	for _, cmd := range []string{"turn-ended", "turn-started"} {
		dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
		for name, args := range map[string][]string{"no copy": nil, "unknown flag": {"--bogus"}, "argument": {"--copy", dir, "extra"}} {
			if code, _, _ := runCLI(t, append([]string{"director", cmd}, args...)...); code != 1 {
				t.Errorf("%s %s: exit %d, want 1", cmd, name, code)
			}
		}
		if code, _, stderr := runCLI(t, "director", cmd, "--copy", dir); code != 1 || !strings.Contains(stderr, "no Director session") {
			t.Errorf("%s no session: exit %d, stderr %s", cmd, code, stderr)
		}
		recordSession(t, filepath.Join(dir, "director.store"), false, store.DirectorRunning)
		if code, _, stderr := runCLI(t, "director", cmd, "--copy", dir); code != 1 || !strings.Contains(stderr, "no supervisor") {
			t.Errorf("%s no supervisor listening: exit %d, stderr %s", cmd, code, stderr)
		}
	}
}

func readReload(t *testing.T, path string) store.DirectorReload {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	r, err := s.LatestDirectorReload()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// waitSessions waits until the copy's newest session is number n and running.
func waitSessions(t *testing.T, dir string, n int) []store.DirectorSession {
	t.Helper()
	for end := time.Now().Add(2 * time.Minute); ; time.Sleep(50 * time.Millisecond) {
		got := sessions(t, dir)
		if len(got) == n && got[n-1].State == store.DirectorRunning {
			return got
		}
		if len(got) > n || time.Now().After(end) {
			t.Fatalf("sessions = %+v; want session %d running", got, n)
		}
	}
}

// holdsOne sees, after some of the supervisor's polls, that the copy still has n sessions.
func holdsOne(t *testing.T, dir string, n int) {
	t.Helper()
	time.Sleep(time.Second) // timing:allow negative observation only; session transitions use waitSessions, and grace logic uses an injected clock.
	if got := sessions(t, dir); len(got) != n || got[n-1].State != store.DirectorRunning {
		t.Fatalf("sessions = %+v; want session %d still running", got, n)
	}
}

func copyRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if code, out, stderr := runCLI(t, append(args, "--copy", dir)...); code != 0 {
		t.Fatalf("%s: exit %d, %q, stderr %s", strings.Join(args, " "), code, out, stderr)
	}
}

// TestReloadNowRestartsTheSessionAtTheTurnEnd: with an hour's grace, reload --now waits for the turn-ended
// signal, then the supervisor ends the runtime once, records the session stopped as reloaded and starts session 2,
// whose startup instructions list the note, and records the reload applied from session 1 to 2.
func TestReloadNowRestartsTheSessionAtTheTurnEnd(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	copyRun(t, dir, "director", "note", "add", "--kind", "plan", "Carry on with t-1.")
	copyRun(t, dir, "director", "reload", "--now")
	holdsOne(t, dir, 1)
	copyRun(t, dir, "director", "turn-ended")
	got := waitSessions(t, dir, 2)
	if got[0].State != store.DirectorStopped || got[0].EndReason != "reloaded" {
		t.Errorf("session 1 = %+v; want stopped as reloaded", got[0])
	}
	r := readReload(t, filepath.Join(dir, "director.store"))
	if r.State != store.ReloadApplied || r.OldSession != 1 || r.NewSession != 2 || !r.Now {
		t.Errorf("reload = %+v; want --now applied from session 1 to 2", r)
	}
	startup, err := os.ReadFile(filepath.Join(dir, "director", "session-2", render.StartupFile))
	if err != nil || !strings.Contains(string(startup), "## Notes\n\n### plan\n- n1 (s1, 0m): Carry on with t-1.\n\n## In flight") {
		t.Errorf("session 2 startup.md = %s, %v; want it to list the note", startup, err)
	}
	holdsOne(t, dir, 2)
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if got := sessions(t, dir); len(got) != 2 || got[1].State != store.DirectorStopped {
		t.Errorf("sessions = %+v; want two, the second stopped", got)
	}
}

// TestReloadNowFallsBackToTheGrace: a runtime with no hook that never signals a turn end is ended once the grace
// has passed.
func TestReloadNowFallsBackToTheGrace(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Nanosecond)
	copyRun(t, dir, "director", "reload", "--now")
	waitSessions(t, dir, 2)
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
}

// TestPlainReloadWaitsForAQuietPoint: a plain reload is applied only once a turn has ended, whether the turn
// ended after the request or before it, and a reload during a turn the captain started waits for that turn's end.
func TestPlainReloadWaitsForAQuietPoint(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	copyRun(t, dir, "director", "reload")
	holdsOne(t, dir, 1)
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	// The new session is busy already. Do not send a turn end before the
	// start: the endpoint acknowledges delivery, not supervisor consumption,
	// so a reload could otherwise be observed in that intervening idle state.
	copyRun(t, dir, "director", "turn-started")
	copyRun(t, dir, "director", "reload")
	holdsOne(t, dir, 2)
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 3)
	copyRun(t, dir, "director", "turn-ended")
	copyRun(t, dir, "director", "reload")
	waitSessions(t, dir, 4)
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
}

// TestRuntimeEndingByItselfGivesTheTerminalBack: a runtime that exits by itself while a plain reload waits for a
// quiet point ends the supervisor with the session stopped, not reloaded, and the reload left for the next
// director start.
func TestRuntimeEndingByItselfGivesTheTerminalBack(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done, typing := superviseTyping(t, dir, time.Hour)
	copyRun(t, dir, "director", "reload")
	holdsOne(t, dir, 1)
	if _, err := typing.WriteString("/exit\n"); err != nil {
		t.Fatal(err)
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason == "reloaded" {
		t.Errorf("sessions = %+v; want session 1 stopped, not reloaded, and no other", got)
	}
	if r := readReload(t, filepath.Join(dir, "director.store")); r.State != store.ReloadRequested {
		t.Errorf("reload = %+v; want it still requested", r)
	}
}

// TestStopWhileEndingForAReloadWins: a stop recorded after the supervisor ended the runtime for a reload, before
// the runtime exited, records the session stopped as a stop does, not reloaded.
func TestStopWhileEndingForAReloadWins(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	path := filepath.Join(dir, "director.store")
	recordSession(t, path, false, store.DirectorRunning, store.DirectorStopping)
	if stopped, err := reloadSession(path, 1); err != nil || !stopped {
		t.Fatalf("reloadSession of a stopping session = %v, %v; want it stopped", stopped, err)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason == "reloaded" {
		t.Errorf("sessions = %+v; want session 1 stopped, not reloaded", got)
	}
	running := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	recordSession(t, filepath.Join(running, "director.store"), false, store.DirectorRunning)
	if stopped, err := reloadSession(filepath.Join(running, "director.store"), 1); err != nil || stopped {
		t.Fatalf("reloadSession of a running session = %v, %v; want it reloaded", stopped, err)
	}
	if got := sessions(t, running); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason != "reloaded" {
		t.Errorf("sessions = %+v; want session 1 stopped as reloaded", got)
	}
}

// TestReloadTakesTheCurrentRuntime: a reload resolves the runtime again from the copy's config, so one changed to
// a runtime not on PATH ends the supervisor with an error once the old session is recorded stopped as reloaded.
func TestReloadTakesTheCurrentRuntime(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(b), "[director]\nadapter = \"fake\"", "[director]\nadapter = \"claude\"", 1))
	t.Setenv("PATH", t.TempDir())
	copyRun(t, dir, "director", "turn-ended")
	copyRun(t, dir, "director", "reload")
	if code := ended(t, done); code != 1 {
		t.Errorf("director start exited %d, want 1", code)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason != "reloaded" {
		t.Errorf("sessions = %+v; want session 1 stopped as reloaded and no other", got)
	}
}

// hookedWatch records session 1 running in a new copy and returns a watch over it with a hook, a minute's grace,
// five minutes' idle_after and a clock the test moves.
func hookedWatch(t *testing.T) (*sessionWatch, *time.Time, string) {
	t.Helper()
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	path := filepath.Join(dir, "director.store")
	recordSession(t, path, false, store.DirectorRunning)
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	w := &sessionWatch{storePath: path, n: 1, grace: time.Minute, hook: true,
		idleAfter: 5 * time.Minute, now: func() time.Time { return clock }}
	return w, &clock, path
}

// ask records a stop, a reload --now or a plain reload of session 1 in the store at path.
func ask(t *testing.T, path, what string) {
	t.Helper()
	s, err := store.Open(path, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	switch what {
	case "stop":
		var d store.DirectorSession
		if d, err = s.DirectorSession(1); err == nil {
			_, err = s.RequestDirectorStop(1, false, d.StateSequence)
		}
	default:
		_, err = s.RequestDirectorReload(what == "reload --now")
	}
	if cerr := s.Close(); err != nil || cerr != nil {
		t.Fatal(err, cerr)
	}
}

var askedEnd = map[string]endAction{"stop": endForStop, "reload --now": endForReload, "reload": endForReload}

// TestTurnStartKeepsAHookedRuntimeBusy: with a hook, a stop, a reload --now past the grace and a plain reload wait
// for the end of a turn that started after the last turn end.
func TestTurnStartKeepsAHookedRuntimeBusy(t *testing.T) {
	for what, want := range askedEnd {
		w, clock, path := hookedWatch(t)
		if got := w.decide(session.TurnEnded); got != keepRunning {
			t.Fatalf("%s: turn end with nothing asked = %d", what, got)
		}
		w.decide(session.TurnStarted)
		ask(t, path, what)
		w.decide("")
		*clock = clock.Add(2 * time.Minute)
		if got := w.decide(""); got != keepRunning {
			t.Errorf("%s: past the grace mid-turn = %d, want the runtime kept running", what, got)
		}
		if got := w.decide(session.TurnEnded); got != want {
			t.Errorf("%s: at the turn end = %d, want %d", what, got, want)
		}
	}
}

// TestInterruptedTurnGoesIdleAfterIdleAfter: a turn the captain interrupts sends turn-started and no turn-ended,
// so the runtime counts as idle once idle_after has passed with no turn signal or wake, and a stop, a reload --now
// or a plain reload then ends it.
func TestInterruptedTurnGoesIdleAfterIdleAfter(t *testing.T) {
	for what, want := range askedEnd {
		w, clock, path := hookedWatch(t)
		w.decide(session.TurnEnded)
		w.decide(session.TurnStarted)
		ask(t, path, what)
		w.decide("")
		*clock = clock.Add(5*time.Minute - time.Second)
		if got := w.decide(""); got != keepRunning {
			t.Errorf("%s: before idle_after = %d, want the runtime kept running", what, got)
		}
		*clock = clock.Add(time.Second)
		if got := w.decide(""); got != want {
			t.Errorf("%s: at idle_after = %d, want %d", what, got, want)
		}
	}
}

func TestDecideEnd(t *testing.T) {
	running := store.DirectorSession{Number: 1, State: store.DirectorRunning}
	stopping := store.DirectorSession{Number: 1, State: store.DirectorStopping}
	stopNow := stopping
	stopNow.StopNow = true
	paused := store.DirectorSession{Number: 1, State: store.DirectorPaused}
	plain := &store.DirectorReload{Sequence: 9, State: store.ReloadRequested}
	now := &store.DirectorReload{Sequence: 9, State: store.ReloadRequested, Now: true}
	grace, idleAfter := time.Minute, 5*time.Minute
	cases := []struct {
		name string
		in   endInput
		want endAction
	}{
		{"nothing asked", endInput{Session: running, TurnEnded: true, Idle: true, Waited: time.Hour}, keepRunning},
		{"stop --now", endInput{Session: stopNow}, endForStop},
		{"stop mid-turn", endInput{Session: stopping, Hook: true, Waited: time.Second}, keepRunning},
		{"stop at the turn end", endInput{Session: stopping, Hook: true, TurnEnded: true}, endForStop},
		{"stop with no hook after the grace", endInput{Session: stopping, Waited: grace}, endForStop},
		{"stop with a hook, busy, after the grace", endInput{Session: stopping, Hook: true, Waited: grace}, keepRunning},
		{"stop with a hook, idle, after the grace", endInput{Session: stopping, Hook: true, Idle: true, Waited: grace}, endForStop},
		{"reload --now mid-turn", endInput{Session: running, Reload: now, Hook: true, Idle: true, Waited: time.Second}, keepRunning},
		{"reload --now at the turn end", endInput{Session: running, Reload: now, Hook: true, TurnEnded: true}, endForReload},
		{"reload --now with no hook after the grace", endInput{Session: running, Reload: now, Waited: grace}, endForReload},
		{"stop wins over reload", endInput{Session: stopping, Reload: now, TurnEnded: true}, endForStop},
		{"plain reload when idle", endInput{Session: running, Reload: plain, Hook: true, Idle: true}, endForReload},
		{"plain reload when idle and paused", endInput{Session: paused, Reload: plain, Hook: true, Idle: true}, endForReload},
		{"plain reload with a wake pending", endInput{Session: running, Reload: plain, Hook: true, TurnEnded: true}, keepRunning},
		{"plain reload with no hook after the grace", endInput{Session: running, Reload: plain, Waited: grace}, endForReload},
		{"plain reload with a hook, busy, after the grace", endInput{Session: running, Reload: plain, Hook: true, Waited: grace}, keepRunning},
		{"stop with a hook, quiet for idle_after, after the grace", endInput{Session: stopping, Hook: true, Waited: grace, Quiet: idleAfter}, endForStop},
		{"stop with a hook, quiet for idle_after, in the grace", endInput{Session: stopping, Hook: true, Quiet: idleAfter}, keepRunning},
		{"reload --now with a hook, quiet for idle_after, after the grace", endInput{Session: running, Reload: now, Hook: true, Waited: grace, Quiet: idleAfter}, endForReload},
		{"plain reload with a hook, quiet for idle_after", endInput{Session: running, Reload: plain, Hook: true, Quiet: idleAfter}, endForReload},
		{"plain reload with a hook, quiet for less than idle_after", endInput{Session: running, Reload: plain, Hook: true, Quiet: idleAfter - time.Second}, keepRunning},
	}
	for _, c := range cases {
		c.in.Grace, c.in.IdleAfter = grace, idleAfter
		if got := decideEnd(c.in); got != c.want {
			t.Errorf("%s: decideEnd = %d, want %d", c.name, got, c.want)
		}
	}
}
