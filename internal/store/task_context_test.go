package store

import (
	"errors"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestAddTaskContext(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, tc := range map[string]struct {
		task, text string
		want       error
	}{
		"blank text":     {"t-1", " \n", ErrInvalid},
		"oversized text": {"t-1", strings.Repeat("x", MaxTextBytes+1), ErrInvalid},
		"invalid utf-8":  {"t-1", "\xff", ErrInvalid},
		"unknown task":   {"t-9", "Keep it quiet.", ErrNotFound},
	} {
		if _, err := s.AddTaskContext("bad-"+strings.ReplaceAll(name, " ", "-"), "app", tc.task, tc.text); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if got, err := s.TaskContexts("app", "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("TaskContexts after refusals = %+v, %v", got, err)
	}

	texts := []string{"Keep it quiet.", strings.Repeat("y", MaxTextBytes)}
	var receipts []Receipt
	for i, text := range texts {
		rc, err := s.AddTaskContext("ctx-"+string(rune('1'+i)), "app", "t-1", text)
		if err != nil || rc.Record != TaskContextRecord(i+1) || rc.Operation != "add-task-context" || rc.Repository != "app" || rc.TaskID != "t-1" {
			t.Fatalf("AddTaskContext %d = %+v, %v", i+1, rc, err)
		}
		receipts = append(receipts, rc)
	}
	if replay, err := s.AddTaskContext("ctx-1", "app", "t-1", texts[0]); err != nil || replay != receipts[0] {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, receipts[0])
	}
	if _, err := s.AddTaskContext("ctx-1", "app", "t-1", "Other words."); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("reused mutation id: err = %v, want ErrMutationConflict", err)
	}
	got, err := s.TaskContexts("app", "t-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("TaskContexts = %+v, %v", got, err)
	}
	for i, c := range got {
		w := TaskContext{Repository: "app", TaskID: "t-1", Number: i + 1, Text: texts[i], Author: TaskContextAuthor, AddedAt: receipts[i].CommittedAt}
		if c != w {
			t.Errorf("entry %d = %+v, want %+v", i+1, c, w)
		}
	}

	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.Contexts) != 2 || h.Contexts[1] != got[1] {
		t.Fatalf("TaskHistory contexts = %+v, %v", h.Contexts, err)
	}
	if h, err := s.TaskHistory("app", "t-1"); err != nil || h.Receipts[len(h.Receipts)-1].Record != TaskContextRecord(2) {
		t.Errorf("TaskHistory receipts = %+v, %v", h.Receipts, err)
	}

	for _, state := range []TaskState{TaskDone, TaskCancelled} {
		id := "t-" + string(state)
		mustCreate(t, s, "m-"+id, sampleTask("app", id))
		if _, err := s.SetTaskState("close-"+id, "app", id, state); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTaskContext("ctx-"+id, "app", id, "Too late."); !errors.Is(err, ErrInvalid) {
			t.Errorf("context on a %s task: err = %v, want ErrInvalid", state, err)
		}
	}
}

func TestDoctorCountsAndDecodesTaskContexts(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for _, m := range []string{"ctx-1", "ctx-2"} {
		if _, err := s.AddTaskContext(m, "app", "t-1", "Words for "+m+"."); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || len(rep.Repositories) != 1 || rep.Repositories[0].TaskContexts != 2 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketTaskContexts).Put([]byte("t-1/9999999999"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "repository app") || rep.Repositories[0].TaskContexts != 3 {
		t.Fatalf("doctor after a bad entry = %+v, %v", rep, err)
	}
}
