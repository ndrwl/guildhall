package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestJudgeHold: a hold waits while any named task is open, is released once all are done, and is released as
// dependency-cancelled at once when any is cancelled.
func TestJudgeHold(t *testing.T) {
	task := func(id string, st TaskState) Task { return Task{Repository: "app", ID: id, State: st} }
	tasks := []Task{task("a", TaskOpen), task("b", TaskDone), task("c", TaskDone), task("d", TaskCancelled)}
	ref := func(id string) TaskRef { return TaskRef{Repository: "app", ID: id} }
	for name, tc := range map[string]struct {
		until     []TaskRef
		state     HoldState
		waiting   []TaskRef
		cancelled []TaskRef
	}{
		"waiting":                {[]TaskRef{ref("a")}, HoldWaiting, []TaskRef{ref("a")}, nil},
		"partly merged":          {[]TaskRef{ref("b"), ref("a")}, HoldWaiting, []TaskRef{ref("a")}, nil},
		"all merged":             {[]TaskRef{ref("b"), ref("c")}, HoldReleased, nil, nil},
		"one cancelled":          {[]TaskRef{ref("a"), ref("d")}, HoldDependencyCancelled, []TaskRef{ref("a")}, []TaskRef{ref("d")}},
		"an unknown task waited": {[]TaskRef{ref("z")}, HoldWaiting, []TaskRef{ref("z")}, nil},
	} {
		j := JudgeHold(tc.until, tasks)
		if j.State != tc.state || !reflect.DeepEqual(j.Waiting, tc.waiting) || !reflect.DeepEqual(j.Cancelled, tc.cancelled) {
			t.Errorf("%s: JudgeHold = %+v, want %s waiting %v cancelled %v", name, j, tc.state, tc.waiting, tc.cancelled)
		}
	}
}

// holdFixture is parkFixture with app/t-4 open, decision q-1 of app/t-1 open and q-2 answered, and Director session
// 1 running.
func holdFixture(t *testing.T) *Store {
	t.Helper()
	s := parkFixture(t)
	mustCreate(t, s, "m-4", sampleTask("app", "t-4"))
	for _, id := range []string{"q-1", "q-2"} {
		if _, err := s.OpenDecision("open-"+id, sampleDecision(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AnswerDecision("answer-q-2", "app", "t-1", "q-2", "approve"); err != nil {
		t.Fatal(err)
	}
	mustStartSession(t, s, sampleSession(1))
	return s
}

// TestHoldRefusals: every kind of hold refuses no named task, an unknown task, a task already done or cancelled, a
// task named twice and the held item's own task, writing nothing.
func TestHoldRefusals(t *testing.T) {
	s := holdFixture(t)
	ref := func(id string) TaskRef { return TaskRef{Repository: "app", ID: id} }
	cases := map[string]struct {
		until []TaskRef
		want  error
		text  string
	}{
		"unknown task":      {[]TaskRef{ref("t-9")}, ErrNotFound, ""},
		"already merged":    {[]TaskRef{ref("t-3")}, ErrInvalid, "app/t-3 already merged; act now instead"},
		"already cancelled": {[]TaskRef{ref("t-2")}, ErrInvalid, "app/t-2 is already cancelled"},
		"named twice":       {[]TaskRef{ref("t-4"), ref("t-4")}, ErrInvalid, "twice"},
		"bad id":            {[]TaskRef{ref("T 4")}, ErrInvalid, ""},
	}
	check := func(kind, name string, err error, want error, text string) {
		t.Helper()
		if !errors.Is(err, want) || !strings.Contains(err.Error(), text) {
			t.Errorf("%s %s: err = %v, want %v containing %q", kind, name, err, want, text)
		}
	}
	self := map[string]struct {
		until []TaskRef
		want  error
		text  string
	}{"own task": {[]TaskRef{ref("t-1")}, ErrInvalid, "itself"}}
	for name, tc := range cases {
		self[name] = tc
	}
	for name, tc := range self {
		id := strings.ReplaceAll(name, " ", "-")
		_, err := s.ParkAttempt("park-"+id, "app", "t-1", "build", 2, "Hold.", 0, tc.until)
		check("park", name, err, tc.want, tc.text)
		_, err = s.DeferDecision("defer-"+id, "app", "t-1", "q-1", "Hold.", tc.until, 0)
		check("defer", name, err, tc.want, tc.text)
	}
	for name, tc := range cases {
		_, _, err := s.AddFollowUp(1, "Do it "+name+".", tc.until)
		check("follow-up", name, err, tc.want, tc.text)
	}
	if _, err := s.DeferDecision("defer-none", "app", "t-1", "q-1", "Hold.", nil, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("defer naming no task = %v, want ErrInvalid", err)
	}
	if _, _, err := s.AddFollowUp(1, "Do it.", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("follow-up naming no task = %v, want ErrInvalid", err)
	}
	if _, err := s.DeferDecision("defer-answered", "app", "t-1", "q-2", "Hold.", []TaskRef{ref("t-4")}, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("defer of an answered decision = %v, want ErrInvalid", err)
	}
	if _, err := s.DeferDecision("defer-unknown", "app", "t-1", "q-9", "Hold.", []TaskRef{ref("t-4")}, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("defer of an unknown decision = %v, want ErrNotFound", err)
	}
	if _, _, err := s.AddFollowUp(1, "two\nlines", []TaskRef{ref("t-4")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("multi-line follow-up = %v, want ErrInvalid", err)
	}
	if _, _, err := s.AddFollowUp(1, strings.Repeat("x", MaxNoteBytes+1), []TaskRef{ref("t-4")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized follow-up = %v, want ErrInvalid", err)
	}
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Parks) != 0 || len(v.Deferrals) != 0 || len(v.FollowUps) != 0 {
		t.Errorf("after refusals: parks %+v, deferrals %+v, follow-ups %+v", v.Parks, v.Deferrals, v.FollowUps)
	}
}

// TestParkUntilMerged: a park records the tasks it waits for, replays under its id with the same set and conflicts
// with another set under the same id.
func TestParkUntilMerged(t *testing.T) {
	s := holdFixture(t)
	until := []TaskRef{{Repository: "app", ID: "t-4"}}
	rc, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Hold until t-4 merges.", 0, until)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Hold until t-4 merges.", 0, until); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	if _, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Hold until t-4 merges.", 0, nil); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another set under the same id = %v, want ErrMutationConflict", err)
	}
	p, err := s.AttemptPark("app", "t-1", "build", 2)
	if err != nil || !p.Parked || !reflect.DeepEqual(p.UntilMerged, until) {
		t.Fatalf("AttemptPark = %+v, %v", p, err)
	}
}

// TestDeferAndUndeferDecision: a deferral replays under its id, is refused under another id or a stale cycle, and
// an undefer keeps the record so the next deferral, under the next cycle's id, records anew. The decision stays open.
func TestDeferAndUndeferDecision(t *testing.T) {
	s := holdFixture(t)
	until := []TaskRef{{Repository: "app", ID: "t-4"}}
	if _, err := s.UndeferDecision("undefer-0", "app", "t-1", "q-1", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undefer of a decision never deferred = %v, want ErrInvalid", err)
	}
	rc, err := s.DeferDecision("defer.0", "app", "t-1", "q-1", "After t-4.", until, 0)
	if err != nil || rc.Operation != "defer-decision" || rc.Record != "decision-defer/q-1" || rc.TaskID != "t-1" {
		t.Fatalf("DeferDecision = %+v, %v", rc, err)
	}
	if replay, err := s.DeferDecision("defer.0", "app", "t-1", "q-1", "After t-4.", until, 0); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	if _, err := s.DeferDecision("other", "app", "t-1", "q-1", "After t-4.", until, 0); !errors.Is(err, ErrExists) {
		t.Fatalf("a second deferral under another id = %v, want ErrExists", err)
	}
	want := DecisionDeferral{Repository: "app", TaskID: "t-1", Decision: "q-1", Reason: "After t-4.", UntilMerged: until, Deferred: true}
	if got, err := s.DecisionDeferral("app", "t-1", "q-1"); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DecisionDeferral = %+v, %v; want %+v", got, err, want)
	}
	un, err := s.UndeferDecision("undefer.0", "app", "t-1", "q-1", 0)
	if err != nil || un.Operation != "undefer-decision" || un.Record != "decision-undefer/q-1" {
		t.Fatalf("UndeferDecision = %+v, %v", un, err)
	}
	want.Deferred, want.Cycle = false, 1
	if got, err := s.DecisionDeferral("app", "t-1", "q-1"); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("after undefer = %+v, %v; want %+v", got, err, want)
	}
	if _, err := s.DeferDecision("defer.stale", "app", "t-1", "q-1", "After t-4.", until, 0); !errors.Is(err, ErrExists) {
		t.Fatalf("deferral at a stale cycle = %v, want ErrExists", err)
	}
	if _, err := s.DeferDecision("defer.1", "app", "t-1", "q-1", "After t-4 again.", until, 1); err != nil {
		t.Fatalf("deferral at cycle 1 = %v", err)
	}
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Deferrals) != 1 || !v.Deferrals[0].Deferred || v.Deferrals[0].Cycle != 1 {
		t.Errorf("DirectorView.Deferrals = %+v", v.Deferrals)
	}
	if len(v.OpenDecisions) != 1 || v.OpenDecisions[0].ID != "q-1" {
		t.Errorf("open decisions = %+v, want q-1 still open", v.OpenDecisions)
	}
}

// TestFollowUps: a follow-up gets its own id, replays, is done once, leaves the view once done, and is written only
// by the newest session that is not stopped.
func TestFollowUps(t *testing.T) {
	s := holdFixture(t)
	until := []TaskRef{{Repository: "app", ID: "t-4"}}
	f, r, err := s.AddFollowUp(1, "Rebase d20 once t-4 merges.", until)
	if err != nil || f.ID != "f1" || f.Done || f.Created.IsZero() || !reflect.DeepEqual(f.UntilMerged, until) || r.Record != "director/follow-up/f1" {
		t.Fatalf("AddFollowUp = %+v, %+v, %v", f, r, err)
	}
	if again, rr, err := s.AddFollowUp(1, "Rebase d20 once t-4 merges.", until); err != nil || rr != r || again.ID != "f1" {
		t.Fatalf("replayed add = %+v, %+v, %v", again, rr, err)
	}
	g, _, err := s.AddFollowUp(1, "Tell the Guildmaster.", until)
	if err != nil || g.ID != "f2" {
		t.Fatalf("second = %+v, %v; want f2", g, err)
	}
	if _, _, err := s.AddFollowUp(2, "From a session not started.", until); !errors.Is(err, ErrNotFound) {
		t.Errorf("add by an unknown session = %v, want ErrNotFound", err)
	}
	done, dr, err := s.DoneFollowUp(1, "f1")
	if err != nil || !done.Done || dr.MutationID != "follow-up-done:f1" {
		t.Fatalf("DoneFollowUp = %+v, %+v, %v", done, dr, err)
	}
	if again, rr, err := s.DoneFollowUp(1, "f1"); err != nil || rr != dr || !again.Done {
		t.Fatalf("replayed done = %+v, %+v, %v", again, rr, err)
	}
	if _, _, err := s.DoneFollowUp(1, "f9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("done of an unknown follow-up = %v, want ErrNotFound", err)
	}
	live, err := s.FollowUps(false)
	if err != nil || len(live) != 1 || live[0].ID != "f2" {
		t.Errorf("FollowUps(false) = %+v, %v; want f2", live, err)
	}
	all, err := s.FollowUps(true)
	if err != nil || len(all) != 2 {
		t.Errorf("FollowUps(true) = %+v, %v; want f1 and f2", all, err)
	}
	v, err := s.DirectorView()
	if err != nil || len(v.FollowUps) != 1 || v.FollowUps[0].ID != "f2" {
		t.Errorf("DirectorView.FollowUps = %+v, %v; want f2", v.FollowUps, err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Errorf("doctor = %+v, %v", rep, err)
	}
}
