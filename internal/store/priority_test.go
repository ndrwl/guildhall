package store

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestParsePriority(t *testing.T) {
	for in, want := range map[string]Priority{"p0": 0, "0": 0, "urgent": 0, "P1": 1, "1": 1, "highest": 1, "high": 2, "p3": 3, "normal": 3, " low ": 4, "5": 5, "Someday": 5} {
		if got, err := ParsePriority(in); err != nil || got != want {
			t.Errorf("ParsePriority(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "p", "p6", "6", "-1", "+1", "p+1", "p01", "top", "pp1"} {
		if _, err := ParsePriority(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParsePriority(%q) = %v; want ErrInvalid", in, err)
		}
	}
	data, err := json.Marshal(PriorityHighest)
	if err != nil || string(data) != `"p1"` {
		t.Fatalf("marshal = %s, %v", data, err)
	}
	if PriorityHighest.Word() != "highest" || Priority(9).Word() != "" {
		t.Fatalf("words = %q, %q", PriorityHighest.Word(), Priority(9).Word())
	}
}

// TestSetTaskPriority records each change with its history, replays a mutation, refuses a closed or unknown task
// and an invalid level, and shows the record in TaskHistory.
func TestSetTaskPriority(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	if _, err := s.SetTaskState("m-3", "app", "t-2", TaskDone); err != nil {
		t.Fatal(err)
	}
	if p, err := s.PriorityOf("app", "t-1"); err != nil || p != DefaultPriority {
		t.Fatalf("PriorityOf before any = %v, %v", p, err)
	}
	rc, err := s.SetTaskPriority("p-1", "app", "t-1", PriorityHighest)
	if err != nil || rc.Record != TaskPriorityRecord || rc.Operation != "set-task-priority" {
		t.Fatalf("SetTaskPriority = %+v, %v", rc, err)
	}
	if again, err := s.SetTaskPriority("p-1", "app", "t-1", PriorityHighest); err != nil || again != rc {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.SetTaskPriority("p-1", "app", "t-1", PriorityLow); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another level under the same id = %v", err)
	}
	if _, err := s.SetTaskPriority("p-2", "app", "t-1", PriorityLow); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		task  string
		level Priority
		want  error
	}{
		"closed task":  {"t-2", PriorityHigh, ErrInvalid},
		"unknown task": {"t-9", PriorityHigh, ErrNotFound},
		"low level":    {"t-1", -1, ErrInvalid},
		"high level":   {"t-1", 6, ErrInvalid},
	} {
		if _, err := s.SetTaskPriority("bad-"+tc.task, "app", tc.task, tc.level); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	tp, err := s.TaskPriority("app", "t-1")
	if err != nil || tp.Priority != PriorityLow || !slices.Equal(tp.History, []Priority{PriorityHighest, PriorityLow}) {
		t.Fatalf("TaskPriority = %+v, %v", tp, err)
	}
	all, err := s.TaskPriorities("app")
	if err != nil || len(all) != 1 || all["t-1"] != PriorityLow {
		t.Fatalf("TaskPriorities = %v, %v", all, err)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || h.Priority == nil || h.Priority.Priority != PriorityLow {
		t.Fatalf("TaskHistory priority = %+v, %v", h.Priority, err)
	}
}
