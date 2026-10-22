package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// runStdin runs the CLI in this process with stdin as its standard input.
func runStdin(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.InstallSkills = func(string) error { return nil }
	app.InstallBaseline = baseline.Install
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

func noteFile(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func latestNote(t *testing.T, path string) (store.HandoffNote, error) {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	return s.LatestHandoffNote()
}

func hexDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// startNextSession records session n+1 for this process over the newest session n.
func startNextSession(t *testing.T, path string) {
	t.Helper()
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d, err := s.LatestDirectorSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDirectorSession(store.DirectorSession{Number: d.Number + 1, State: store.DirectorRecovering,
		Identity: store.ProcessIdentity(self), Release: "test", ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
}

// TestDirectorHandoffRecordsTheNote: a note from a file or standard input is normalised as a decision answer is,
// written under the document directory and recorded with its digest for the newest session; every refusal
// records nothing.
func TestDirectorHandoffRecordsTheNote(t *testing.T) {
	d := newRunDirector(t)
	flags := []string{"--store", d.store, "--state", d.state}
	handoff := func(stdin string, extra ...string) (int, string, string) {
		return runStdin(t, stdin, append(append([]string{"director", "handoff"}, extra...), flags...)...)
	}
	file := noteFile(t, "\ufeffNext: sort the list.\r\nOpen: ask whether ids sort numerically.\r\n")
	if code, _, stderr := handoff("", "--note-file", file); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Fatalf("handoff with no session: exit %d, stderr %s", code, stderr)
	}
	recordSession(t, d.store, false, store.DirectorRunning)
	code, out, stderr := handoff("", "--note-file", file)
	var n store.HandoffNote
	if err := json.Unmarshal([]byte(out), &n); code != 0 || err != nil {
		t.Fatalf("handoff: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	want := "Next: sort the list.\nOpen: ask whether ids sort numerically.\n"
	if n.Session != 1 || n.SHA256 != hexDigest(want) || n.Path != store.HandoffPath(n.Sequence) || n.RecordedAt.IsZero() {
		t.Fatalf("note = %+v, want session 1 with the LF note's digest", n)
	}
	if b, err := os.ReadFile(filepath.Join(d.state, "documents", filepath.FromSlash(n.Path))); err != nil || string(b) != want {
		t.Fatalf("note file = %q, %v; want %q", b, err, want)
	}
	if got, err := latestNote(t, d.store); err != nil || got != n {
		t.Fatalf("latest note = %+v, %v; want %+v", got, err, n)
	}

	code, out, stderr = handoff("Second note.\n", "--note-file", "-")
	var second store.HandoffNote
	if err := json.Unmarshal([]byte(out), &second); code != 0 || err != nil || second.SHA256 != hexDigest("Second note.\n") || second.Sequence <= n.Sequence {
		t.Fatalf("handoff from stdin: exit %d, %q (%v), stderr %s", code, out, err, stderr)
	}
	if code, _, stderr := handoff(strings.Repeat("a", store.MaxHandoffNoteBytes), "--note-file", "-"); code != 0 {
		t.Fatalf("a note of exactly the limit: exit %d, stderr %s", code, stderr)
	}
	limit, err := latestNote(t, d.store)
	if err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"utf-16":    "\xff\xfeN\x00o\x00",
		"blank":     " \n\t\n",
		"too large": strings.Repeat("a", store.MaxHandoffNoteBytes+1),
	} {
		if code, _, stderr := handoff("", "--note-file", noteFile(t, content)); code != 1 {
			t.Errorf("%s note: exit %d, stderr %s; want 1", name, code, stderr)
		}
	}
	if code, _, _ := handoff("", "--note-file", filepath.Join(t.TempDir(), "missing.md")); code != 1 {
		t.Errorf("missing note file: exit %d, want 1", code)
	}
	if got, err := latestNote(t, d.store); err != nil || got != limit {
		t.Errorf("latest note after refusals = %+v, %v; want %+v", got, err, limit)
	}
	for name, args := range map[string][]string{"no note file": nil, "argument": {"--note-file", "-", "extra"}} {
		if code, _, _ := handoff("", args...); code != 2 {
			t.Errorf("%s: exit %d, want usage error 2", name, code)
		}
	}
}

// TestDirectorReload: a plain reload is recorded with or without a live session, to be applied by the next one;
// --now needs a note recorded by the newest session, which is not stopped, and upgrades the pending request.
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
	if code, _, stderr := reload("--now"); code != 1 || !strings.Contains(stderr, "director handoff") {
		t.Fatalf("--now with no note: exit %d, stderr %s; want it to name director handoff", code, stderr)
	}
	if code, _, stderr := runCLI(t, append([]string{"director", "handoff", "--note-file", noteFile(t, "Note one.\n")}, flags...)...); code != 0 {
		t.Fatalf("handoff: exit %d, stderr %s", code, stderr)
	}
	startNextSession(t, d.store)
	if code, _, stderr := reload("--now"); code != 1 || !strings.Contains(stderr, "session 1") {
		t.Fatalf("--now with an earlier session's note: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, append([]string{"director", "handoff", "--note-file", noteFile(t, "Note two.\n")}, flags...)...); code != 0 {
		t.Fatalf("handoff: exit %d, stderr %s", code, stderr)
	}
	code, now, stderr := reload("--now")
	if code != 0 || !now.Now || now.Sequence != plain.Sequence || now.State != store.ReloadRequested {
		t.Fatalf("--now: exit %d, %+v, stderr %s; want the pending request upgraded", code, now, stderr)
	}
	if code, again, _ := reload(); code != 0 || again != now {
		t.Fatalf("plain reload after --now: exit %d, %+v; want %+v", code, again, now)
	}
	if code, _, _ := runCLI(t, "director", "reload", "extra", "--store", d.store, "--state", d.state); code != 2 {
		t.Errorf("reload with an argument: exit %d, want 2", code)
	}
}

// TestDirectorStatusNamesTheNoteAndTheReload: the status feed's session section carries the newest handoff note
// and the newest reload request.
func TestDirectorStatusNamesTheNoteAndTheReload(t *testing.T) {
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
		t.Fatalf("session before any note or reload = %s", s)
	}
	code, out, _ := runCLI(t, append([]string{"director", "handoff", "--note-file", noteFile(t, "A note.\n")}, flags...)...)
	var n store.HandoffNote
	if err := json.Unmarshal([]byte(out), &n); code != 0 || err != nil {
		t.Fatalf("handoff: exit %d, %q", code, out)
	}
	code, out, _ = runCLI(t, append([]string{"director", "reload"}, flags...)...)
	var r store.DirectorReload
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil {
		t.Fatalf("reload: exit %d, %q", code, out)
	}
	s := status()
	var gotNote store.HandoffNote
	var gotReload store.DirectorReload
	if err := json.Unmarshal(s["handoff_note"], &gotNote); err != nil || gotNote != n {
		t.Errorf("status handoff_note = %s, want %+v", s["handoff_note"], n)
	}
	if err := json.Unmarshal(s["reload"], &gotReload); err != nil || gotReload != r {
		t.Errorf("status reload = %s, want %+v", s["reload"], r)
	}
}

// TestDirectorReloadNowWaitsForANoteAfterTheStage: while a Director stage attempt runs, --now is refused unless
// the newest note was recorded after the attempt started, so a reload never lands mid-grill.
func TestDirectorReloadNowWaitsForANoteAfterTheStage(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	flags := []string{"--store", d.store, "--state", d.state}
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, _, stderr := runCLI(t, append([]string{"director", "handoff", "--note-file", noteFile(t, "Before the grill.\n")}, flags...)...); code != 0 {
		t.Fatalf("handoff: exit %d, stderr %s", code, stderr)
	}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, _, stderr := runCLI(t, append([]string{"director", "reload", "--now"}, flags...)...); code != 1 || !strings.Contains(stderr, "grill") {
		t.Fatalf("--now mid-grill with an older note: exit %d, stderr %s; want it refused naming the grill", code, stderr)
	}
	if code, _, stderr := runCLI(t, append([]string{"director", "handoff", "--note-file", noteFile(t, "Mid-grill: questions 2 and 3 open.\n")}, flags...)...); code != 0 {
		t.Fatalf("handoff: exit %d, stderr %s", code, stderr)
	}
	if code, out, stderr := runCLI(t, append([]string{"director", "reload", "--now"}, flags...)...); code != 0 {
		t.Fatalf("--now with a note after the grill started: exit %d, %q, stderr %s", code, out, stderr)
	}
}

// TestDirectorTurnEndedNeverExitsTwo: a Claude Code Stop hook reads exit 2 as "keep going", so turn-ended
// reports a usage error and no supervisor with exit 1.
func TestDirectorTurnEndedNeverExitsTwo(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	for name, args := range map[string][]string{"no copy": nil, "unknown flag": {"--bogus"}, "argument": {"--copy", dir, "extra"}} {
		if code, _, _ := runCLI(t, append([]string{"director", "turn-ended"}, args...)...); code != 1 {
			t.Errorf("%s: exit %d, want 1", name, code)
		}
	}
	if code, _, stderr := runCLI(t, "director", "turn-ended", "--copy", dir); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Errorf("no session: exit %d, stderr %s", code, stderr)
	}
	recordSession(t, filepath.Join(dir, "director.store"), false, store.DirectorRunning)
	if code, _, stderr := runCLI(t, "director", "turn-ended", "--copy", dir); code != 1 || !strings.Contains(stderr, "no supervisor") {
		t.Errorf("no supervisor listening: exit %d, stderr %s", code, stderr)
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
	time.Sleep(time.Second)
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
// whose startup instructions name the note, and records the reload applied from session 1 to 2.
func TestReloadNowRestartsTheSessionAtTheTurnEnd(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	copyRun(t, dir, "director", "handoff", "--note-file", noteFile(t, "Carry on with t-1.\n"))
	copyRun(t, dir, "director", "reload", "--now")
	holdsOne(t, dir, 1)
	note, err := latestNote(t, filepath.Join(dir, "director.store"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || !strings.Contains(string(startup), filepath.Join(dir, "state", "documents", filepath.FromSlash(note.Path))) {
		t.Errorf("session 2 startup.md = %s, %v; want it to name the note", startup, err)
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
	copyRun(t, dir, "director", "handoff", "--note-file", noteFile(t, "Carry on.\n"))
	copyRun(t, dir, "director", "reload", "--now")
	waitSessions(t, dir, 2)
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
}

// TestPlainReloadWaitsForAQuietPoint: a plain reload is applied only once a turn has ended, whether the turn
// ended after the request or before it.
func TestPlainReloadWaitsForAQuietPoint(t *testing.T) {
	dir, _ := supervisedCopy(t)
	done := supervise(t, dir, time.Hour)
	copyRun(t, dir, "director", "reload")
	holdsOne(t, dir, 1)
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	copyRun(t, dir, "director", "turn-ended")
	copyRun(t, dir, "director", "reload")
	waitSessions(t, dir, 3)
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
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
	grace := time.Minute
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
		{"plain reload mid-stage", endInput{Session: running, Reload: plain, Hook: true, Idle: true, TurnEnded: true, StageRunning: true}, keepRunning},
		{"plain reload with a wake pending", endInput{Session: running, Reload: plain, Hook: true, TurnEnded: true}, keepRunning},
		{"plain reload with no hook after the grace", endInput{Session: running, Reload: plain, Waited: grace}, endForReload},
		{"plain reload with no hook mid-stage after the grace", endInput{Session: running, Reload: plain, Waited: grace, StageRunning: true}, keepRunning},
		{"plain reload with a hook, busy, after the grace", endInput{Session: running, Reload: plain, Hook: true, Waited: grace}, keepRunning},
	}
	for _, c := range cases {
		c.in.Grace = grace
		if got := decideEnd(c.in); got != c.want {
			t.Errorf("%s: decideEnd = %d, want %d", c.name, got, c.want)
		}
	}
}
