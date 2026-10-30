package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandoffDueIsRecordedOncePerCrossing: a crossing is recorded once, a replay returns its receipt, the same
// crossing with another reason is a conflict, and the dues of each session are listed in order after a reopen.
func TestHandoffDueIsRecordedOncePerCrossing(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	if got, err := s.HandoffDues(1); err != nil || len(got) != 0 {
		t.Fatalf("dues of a session with none = %+v, %v; want an empty list", got, err)
	}
	ctx := HandoffDue{Session: 1, Trigger: HandoffContext, Crossing: 1, Reason: "context 72%", MessageID: "q-1"}
	r, err := s.RecordHandoffDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.MutationID != "handoff-due:1:context:1" || r.Operation != "record-handoff-due" || r.Record != "director/handoff-due/"+itoa(r.Sequence) {
		t.Fatalf("receipt = %+v", r)
	}
	if again, err := s.RecordHandoffDue(ctx); err != nil || again != r {
		t.Fatalf("replay = %+v, %v; want the original receipt", again, err)
	}
	other := ctx
	other.Reason, other.MessageID = "context 80%", "q-2"
	if _, err := s.RecordHandoffDue(other); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("the same crossing again = %v, want ErrMutationConflict", err)
	}
	idle := HandoffDue{Session: 1, Trigger: HandoffIdle, Crossing: 1, Reason: "idle 30m0s"}
	ir, err := s.RecordHandoffDue(idle)
	if err != nil {
		t.Fatal(err)
	}
	mustStartSession(t, s, sampleSession(2))
	tasks := HandoffDue{Session: 2, Trigger: HandoffTasks, Crossing: 2, Reason: "4 tasks finished", MessageID: "f-1"}
	if _, err := s.RecordHandoffDue(tasks); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = openTest(t, path)
	got, err := s.HandoffDues(1)
	if err != nil || len(got) != 2 {
		t.Fatalf("dues of session 1 = %+v, %v; want two", got, err)
	}
	if got[0].Sequence != r.Sequence || got[0].Reason != "context 72%" || got[0].MessageID != "q-1" || got[0].Time.IsZero() {
		t.Errorf("first due = %+v", got[0])
	}
	if got[1].Sequence != ir.Sequence || got[1].Trigger != HandoffIdle || got[1].MessageID != "" {
		t.Errorf("second due = %+v", got[1])
	}
	if got, err := s.HandoffDues(2); err != nil || len(got) != 1 || got[0].Crossing != 2 || got[0].Trigger != HandoffTasks {
		t.Errorf("dues of session 2 = %+v, %v", got, err)
	}
}

func TestRecordHandoffDueRefusals(t *testing.T) {
	s, _ := newStore(t)
	valid := HandoffDue{Session: 1, Trigger: HandoffTasks, Crossing: 1, Reason: "2 tasks finished"}
	if _, err := s.RecordHandoffDue(valid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session = %v, want ErrNotFound", err)
	}
	mustStartSession(t, s, sampleSession(1))
	bad := map[string]func(*HandoffDue){
		"session 0":        func(d *HandoffDue) { d.Session = 0 },
		"unknown trigger":  func(d *HandoffDue) { d.Trigger = "weather" },
		"crossing 0":       func(d *HandoffDue) { d.Crossing = 0 },
		"no reason":        func(d *HandoffDue) { d.Reason = " " },
		"long reason":      func(d *HandoffDue) { d.Reason = strings.Repeat("r", MaxHandoffReasonBytes+1) },
		"bad message id":   func(d *HandoffDue) { d.MessageID = "not an id" },
		"sequence given":   func(d *HandoffDue) { d.Sequence = 3 },
		"time given":       func(d *HandoffDue) { d.Time = testTime },
		"control in label": func(d *HandoffDue) { d.Reason = "two\nlines" },
	}
	for name, edit := range bad {
		d := valid
		edit(&d)
		if _, err := s.RecordHandoffDue(d); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.HandoffDues(2); err != nil {
		t.Errorf("dues of an unknown session = %v, want an empty list", err)
	}
}

// TestFinishedTasksCountsSinceTheSessionStarted: tasks moved to done or cancelled after the session was recorded,
// by SetTaskState or CancelTask, count across repositories; tasks finished before it, and open tasks, do not.
func TestFinishedTasksCountsSinceTheSessionStarted(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.FinishedTasks(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session = %v, want ErrNotFound", err)
	}
	for _, task := range []Task{sampleTask("app", "t-1"), sampleTask("app", "t-2"), sampleTask("app", "t-3"), sampleTask("lib", "t-1"), sampleTask("lib", "t-2")} {
		if _, err := s.CreateTask("create-"+task.Repository+"-"+task.ID, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetTaskState("done-app-t-1", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	mustStartSession(t, s, sampleSession(1))
	if n, err := s.FinishedTasks(1); err != nil || n != 0 {
		t.Fatalf("FinishedTasks before any = %d, %v; want 0", n, err)
	}
	for _, f := range []struct {
		repo, id string
		state    TaskState
	}{{"app", "t-2", TaskDone}, {"lib", "t-1", TaskCancelled}, {"lib", "t-2", TaskDone}} {
		if _, err := s.SetTaskState("finish-"+f.repo+"-"+f.id, f.repo, f.id, f.state); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.FinishedTasks(1); err != nil || n != 3 {
		t.Fatalf("FinishedTasks = %d, %v; want 3", n, err)
	}
	// A task cancel's receipt counts as a set-task-state one does.
	if _, err := s.CancelTask("cancel.app.t-3", "app", "t-3", "Duplicate."); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FinishedTasks(1); err != nil || n != 4 {
		t.Fatalf("FinishedTasks after a cancel = %d, %v; want 4", n, err)
	}
	mustStartSession(t, s, sampleSession(2))
	if n, err := s.FinishedTasks(2); err != nil || n != 0 {
		t.Fatalf("FinishedTasks of a new session = %d, %v; want 0", n, err)
	}
}

// TestDirectorSessionRecordsItsHandoffPolicy: the policy is kept with the session and refused when out of range.
func TestDirectorSessionRecordsItsHandoffPolicy(t *testing.T) {
	s, _ := newStore(t)
	file := filepath.Join(t.TempDir(), "context.json")
	bad := map[string]HandoffPolicy{
		"negative tasks":   {AfterTasks: -1},
		"percent over 100": {ContextPercent: 101},
		"negative percent": {ContextPercent: -5},
		"relative file":    {ContextPercent: 70, ContextFile: "context.json"},
	}
	for name, p := range bad {
		d := sampleSession(1)
		d.Handoff = p
		if _, err := s.StartDirectorSession(d); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	d := sampleSession(1)
	d.Handoff = HandoffPolicy{AfterTasks: 3, ContextPercent: 70, ContextFile: file}
	mustStartSession(t, s, d)
	if got, err := s.DirectorSession(1); err != nil || got.Handoff != d.Handoff {
		t.Fatalf("session handoff = %+v, %v; want %+v", got.Handoff, err, d.Handoff)
	}
}
