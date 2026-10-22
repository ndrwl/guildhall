package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sampleSession(n int) DirectorSession {
	return DirectorSession{Number: n, State: DirectorRecovering, Identity: ProcessIdentity{PID: 4242, Token: "boot-1:77"}, Release: "v0.4.0",
		ConfigDigest: sampleDigest('c'), Pane: DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"}}
}

func mustStartSession(t *testing.T, s *Store, d DirectorSession) Receipt {
	t.Helper()
	r, err := s.StartDirectorSession(d)
	if err != nil {
		t.Fatalf("StartDirectorSession(%d): %v", d.Number, err)
	}
	return r
}

func mustMove(t *testing.T, s *Store, n int, to DirectorState) Receipt {
	t.Helper()
	d, err := s.DirectorSession(n)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SetDirectorState(n, to, d.StateSequence)
	if err != nil {
		t.Fatalf("SetDirectorState(%d, %s): %v", n, to, err)
	}
	return r
}

func TestDirectorSessionStartsRecoveringAndSurvivesRestart(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.LatestDirectorSession(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest session of an empty store = %v, want ErrNotFound", err)
	}
	r := mustStartSession(t, s, sampleSession(1))
	if r.MutationID != "director-session:1" || r.Operation != "start-director-session" || r.Record != "director/session/1" || r.Sequence != 1 {
		t.Fatalf("receipt = %+v", r)
	}
	_ = s.Close()

	s = openTest(t, path)
	got, err := s.LatestDirectorSession()
	if err != nil {
		t.Fatal(err)
	}
	want := sampleSession(1)
	want.StartedAt, want.StateSequence = testTime, 1
	if got != want {
		t.Fatalf("session = %+v, want %+v", got, want)
	}
	if again, err := s.StartDirectorSession(sampleSession(1)); err != nil || again != r {
		t.Fatalf("replay = %+v, %v; want the original receipt", again, err)
	}
	if n := countReceipts(t, s); n != 1 {
		t.Fatalf("receipts after replay = %d", n)
	}
}

func TestDirectorSessionStartRefusals(t *testing.T) {
	s, _ := newStore(t)
	bad := map[string]func(*DirectorSession){
		"number 0":            func(d *DirectorSession) { d.Number = 0 },
		"skips a number":      func(d *DirectorSession) { d.Number = 2 },
		"starts running":      func(d *DirectorSession) { d.State = DirectorRunning },
		"unknown state":       func(d *DirectorSession) { d.State = "asleep" },
		"no pid":              func(d *DirectorSession) { d.Identity.PID = 0 },
		"no token":            func(d *DirectorSession) { d.Identity.Token = " " },
		"no release":          func(d *DirectorSession) { d.Release = "" },
		"bad config digest":   func(d *DirectorSession) { d.ConfigDigest = "md5:1" },
		"half a pane":         func(d *DirectorSession) { d.Pane.Tab = "" },
		"started at given":    func(d *DirectorSession) { d.StartedAt = testTime },
		"ready at given":      func(d *DirectorSession) { d.ReadyAt = testTime },
		"state sequence give": func(d *DirectorSession) { d.StateSequence = 9 },
	}
	for name, edit := range bad {
		d := sampleSession(1)
		edit(&d)
		if _, err := s.StartDirectorSession(d); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if n := countReceipts(t, s); n != 0 {
		t.Fatalf("refused starts wrote %d receipts", n)
	}
	// A session without a pane is valid: the Director runs in the current terminal.
	d := sampleSession(1)
	d.Pane = DirectorPane{}
	mustStartSession(t, s, d)
	if _, err := s.StartDirectorSession(sampleSession(1)); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("a different session 1 = %v, want ErrMutationConflict", err)
	}
}

func TestDirectorStateMoves(t *testing.T) {
	type move struct {
		to DirectorState
		ok bool
	}
	cases := []struct {
		name string
		path []DirectorState
		last move
	}{
		{"recovering to running", nil, move{DirectorRunning, true}},
		{"recovering to paused", nil, move{DirectorPaused, true}},
		{"recovering to stopping", nil, move{DirectorStopping, true}},
		{"recovering to stopped", nil, move{DirectorStopped, true}},
		{"recovering stays", nil, move{DirectorRecovering, false}},
		{"running to paused", []DirectorState{DirectorRunning}, move{DirectorPaused, true}},
		{"paused to running", []DirectorState{DirectorRunning, DirectorPaused}, move{DirectorRunning, true}},
		{"running back to recovering", []DirectorState{DirectorRunning}, move{DirectorRecovering, false}},
		{"running stays", []DirectorState{DirectorRunning}, move{DirectorRunning, false}},
		{"paused to stopping", []DirectorState{DirectorPaused}, move{DirectorStopping, true}},
		{"stopping to stopped", []DirectorState{DirectorStopping}, move{DirectorStopped, true}},
		{"stopping back to running", []DirectorState{DirectorStopping}, move{DirectorRunning, false}},
		{"stopped to running", []DirectorState{DirectorStopped}, move{DirectorRunning, false}},
		{"stopped to stopped", []DirectorState{DirectorStopped}, move{DirectorStopped, false}},
		{"unknown state", nil, move{"asleep", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustStartSession(t, s, sampleSession(1))
			for _, st := range c.path {
				mustMove(t, s, 1, st)
			}
			before, _ := s.DirectorSession(1)
			receipts := countReceipts(t, s)
			_, err := s.SetDirectorState(1, c.last.to, before.StateSequence)
			if c.last.ok != (err == nil) {
				t.Fatalf("move to %s: err = %v, want ok %v", c.last.to, err, c.last.ok)
			}
			if !c.last.ok {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("refused move err = %v, want ErrInvalid", err)
				}
				if after, _ := s.DirectorSession(1); after != before || countReceipts(t, s) != receipts {
					t.Fatalf("refused move changed the session: %+v -> %+v", before, after)
				}
			}
		})
	}
}

func TestDirectorStateRecordsTimesAndReplays(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	r := mustMove(t, s, 1, DirectorRunning)
	if r.MutationID != "director-state:1:running:1" || r.Operation != "set-director-state" || r.Record != "director/session/1" {
		t.Fatalf("receipt = %+v", r)
	}
	if again, err := s.SetDirectorState(1, DirectorRunning, 1); err != nil || again != r {
		t.Fatalf("replay = %+v, %v; want the original receipt", again, err)
	}
	mustMove(t, s, 1, DirectorPaused)
	mustMove(t, s, 1, DirectorRunning)
	// A move decided on a stale read is refused, not applied over the newer state.
	if _, err := s.SetDirectorState(1, DirectorStopping, r.Sequence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale move = %v, want ErrInvalid", err)
	}
	stop := mustMove(t, s, 1, DirectorStopped)
	_ = s.Close()

	s = openTest(t, path)
	d, err := s.DirectorSession(1)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DirectorStopped || d.ReadyAt != testTime || d.EndedAt != testTime || d.StateSequence != stop.Sequence {
		t.Fatalf("session = %+v", d)
	}
	if _, err := s.SetDirectorState(2, DirectorRunning, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move of a missing session = %v, want ErrNotFound", err)
	}
}

func TestSecondDirectorSessionSupersedesTheFirst(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	mustMove(t, s, 1, DirectorRunning)
	first, _ := s.DirectorSession(1)
	r := mustStartSession(t, s, sampleSession(2))

	old, err := s.DirectorSession(1)
	if err != nil {
		t.Fatal(err)
	}
	if old.State != DirectorStopped || old.EndedAt != testTime || old.StateSequence != r.Sequence {
		t.Fatalf("superseded session = %+v", old)
	}
	latest, err := s.LatestDirectorSession()
	if err != nil || latest.Number != 2 || latest.State != DirectorRecovering {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	// Only the newest session moves.
	if _, err := s.SetDirectorState(1, DirectorStopped, first.StateSequence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("move of a superseded session = %v, want ErrInvalid", err)
	}
	// A stopped first session is left as it ended.
	mustMove(t, s, 2, DirectorStopped)
	ended, _ := s.DirectorSession(2)
	mustStartSession(t, s, sampleSession(3))
	if again, _ := s.DirectorSession(2); again != ended {
		t.Fatalf("starting session 3 changed stopped session 2: %+v -> %+v", ended, again)
	}
}

func TestWakesAreRecordedOncePerMessageInOrder(t *testing.T) {
	s, path := messagingStore(t)
	mustSend(t, s, toDirector("q-1"))
	mustSend(t, s, toDirector("q-2"))
	mustSend(t, s, toArtisan("n-1"))

	woke := Wake{MessageID: "q-2", SenderType: "guildhall", Decision: WakeWoke}
	r2, err := s.RecordWake(woke)
	if err != nil {
		t.Fatal(err)
	}
	if r2.MutationID != "wake:q-2" || r2.Operation != "record-wake" || r2.Repository != "app" || r2.TaskID != "t-1" || r2.Record != "wake/q-2" {
		t.Fatalf("receipt = %+v", r2)
	}
	r1, err := s.RecordWake(Wake{MessageID: "q-1", SenderType: "local", Decision: WakeQueued, Reason: "no pane"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.RecordWake(woke); err != nil || again != r2 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.RecordWake(Wake{MessageID: "q-2", SenderType: "guildhall", Decision: WakeQueued, Reason: "stopped"}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("second decision for q-2 = %v, want ErrMutationConflict", err)
	}
	_ = s.Close()

	s = openTest(t, path)
	ws, more, err := s.Wakes(0, 10)
	if err != nil || more {
		t.Fatalf("Wakes = %v, more %v", err, more)
	}
	want := []Wake{
		{Sequence: r2.Sequence, MessageID: "q-2", SenderType: "guildhall", Decision: WakeWoke, Time: testTime},
		{Sequence: r1.Sequence, MessageID: "q-1", SenderType: "local", Decision: WakeQueued, Reason: "no pane", Time: testTime},
	}
	if fmt.Sprint(ws) != fmt.Sprint(want) {
		t.Fatalf("wakes = %+v, want %+v", ws, want)
	}
	if page, more, err := s.Wakes(r2.Sequence, 1); err != nil || more || len(page) != 1 || page[0].MessageID != "q-1" {
		t.Fatalf("page after %d = %+v, %v, %v", r2.Sequence, page, more, err)
	}
	if page, more, _ := s.Wakes(0, 1); len(page) != 1 || !more {
		t.Fatalf("first page of 1 = %+v, more %v", page, more)
	}
	if w, err := s.WakeFor("q-1"); err != nil || w != want[1] {
		t.Fatalf("WakeFor(q-1) = %+v, %v", w, err)
	}
	if _, err := s.WakeFor("q-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("WakeFor(q-3) = %v, want ErrNotFound", err)
	}
}

func TestRecordWakeRefusals(t *testing.T) {
	s, _ := messagingStore(t)
	mustSend(t, s, toDirector("q-1"))
	mustSend(t, s, toArtisan("n-1"))
	long := make([]byte, MaxWakeReasonBytes+1)
	for i := range long {
		long[i] = 'x'
	}
	for name, w := range map[string]Wake{
		"missing message":     {MessageID: "q-9", SenderType: "local", Decision: WakeWoke},
		"artisan message":     {MessageID: "n-1", SenderType: "local", Decision: WakeWoke},
		"bad sender type":     {MessageID: "q-1", SenderType: "Local!", Decision: WakeWoke},
		"unknown decision":    {MessageID: "q-1", SenderType: "local", Decision: "maybe"},
		"queued no reason":    {MessageID: "q-1", SenderType: "local", Decision: WakeQueued},
		"woke with reason":    {MessageID: "q-1", SenderType: "local", Decision: WakeWoke, Reason: "x"},
		"reason too long":     {MessageID: "q-1", SenderType: "local", Decision: WakeQueued, Reason: string(long)},
		"sequence given":      {MessageID: "q-1", SenderType: "local", Decision: WakeWoke, Sequence: 3},
		"time given":          {MessageID: "q-1", SenderType: "local", Decision: WakeWoke, Time: testTime},
		"reason with newline": {MessageID: "q-1", SenderType: "local", Decision: WakeQueued, Reason: "a\nb"},
	} {
		_, err := s.RecordWake(w)
		if err == nil {
			t.Errorf("%s: recorded", name)
		} else if name != "missing message" && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.RecordWake(Wake{MessageID: "q-9", SenderType: "local", Decision: WakeWoke}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing message: err = %v, want ErrNotFound", err)
	}
	if ws, _, _ := s.Wakes(0, 10); len(ws) != 0 {
		t.Fatalf("refused wakes recorded %+v", ws)
	}
}

// TestDirectorRecordsOnSchemaV1Fixture shows a schema 1 file written before the director bucket existed
// accepts sessions and wakes, creating the bucket on first use.
func TestDirectorRecordsOnSchemaV1Fixture(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	if _, err := s.LatestDirectorSession(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest session on the fixture = %v, want ErrNotFound", err)
	}
	if ws, more, err := s.Wakes(0, 10); err != nil || more || len(ws) != 0 {
		t.Fatalf("wakes on the fixture = %v, %v, %v", ws, more, err)
	}
	mustStartSession(t, s, sampleSession(1))
	if _, err := s.SendMessage(toDirector("q-fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWake(Wake{MessageID: "q-fixture", SenderType: "local", Decision: WakeQueued, Reason: "no pane"}); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}
