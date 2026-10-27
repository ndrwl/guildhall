package store

import (
	"errors"
	"strings"
	"testing"
)

// TestOverrideDependencies records a dependency override once per task: its validation, replay, a second
// override's conflict and refusal, and its receipt and record in TaskHistory.
func TestOverrideDependencies(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	dependent := sampleTask("app", "t-2")
	dependent.DependsOn = []TaskRef{{Repository: "app", ID: "t-1"}}
	mustCreate(t, s, "m-2", dependent)
	closed := sampleTask("app", "t-3")
	closed.DependsOn = dependent.DependsOn
	mustCreate(t, s, "m-3", closed)
	if _, err := s.SetTaskState("m-4", "app", "t-3", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		task, reason string
		want         error
	}{
		"blank reason":       {"t-2", " ", ErrInvalid},
		"oversized reason":   {"t-2", strings.Repeat("r", MaxTextBytes+1), ErrInvalid},
		"no dependencies":    {"t-1", "Start anyway.", ErrInvalid},
		"closed task":        {"t-3", "Start anyway.", ErrInvalid},
		"unknown task":       {"t-9", "Start anyway.", ErrNotFound},
		"invalid task id":    {"T 2", "Start anyway.", ErrInvalid},
		"empty reason":       {"t-2", "", ErrInvalid},
	} {
		if _, err := s.OverrideDependencies("bad-"+strings.ReplaceAll(name, " ", "-"), "app", tc.task, tc.reason); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := s.DependencyOverride("app", "t-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DependencyOverride before any = %v, want ErrNotFound", err)
	}
	rc, err := s.OverrideDependencies("dependency-override.app.t-2", "app", "t-2", "t-1 is blocked on review; t-2 does not touch its files.")
	if err != nil || rc.Record != DependencyOverrideRecord || rc.Operation != "override-dependencies" || rc.TaskID != "t-2" {
		t.Fatalf("OverrideDependencies = %+v, %v", rc, err)
	}
	if replay, err := s.OverrideDependencies("dependency-override.app.t-2", "app", "t-2", "t-1 is blocked on review; t-2 does not touch its files."); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	if _, err := s.OverrideDependencies("dependency-override.app.t-2", "app", "t-2", "Another reason."); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another reason under the same id = %v, want ErrMutationConflict", err)
	}
	if _, err := s.OverrideDependencies("other-id", "app", "t-2", "Another reason."); !errors.Is(err, ErrExists) {
		t.Fatalf("a second override = %v, want ErrExists", err)
	}
	want := DependencyOverride{Repository: "app", TaskID: "t-2", Reason: "t-1 is blocked on review; t-2 does not touch its files."}
	if got, err := s.DependencyOverride("app", "t-2"); err != nil || got != want {
		t.Fatalf("DependencyOverride = %+v, %v; want %+v", got, err, want)
	}
	h, err := s.TaskHistory("app", "t-2")
	if err != nil {
		t.Fatal(err)
	}
	if h.DependencyOverride == nil || *h.DependencyOverride != want {
		t.Errorf("TaskHistory.DependencyOverride = %+v, want %+v", h.DependencyOverride, want)
	}
	found := false
	for _, r := range h.Receipts {
		found = found || r.MutationID == rc.MutationID && r.Record == DependencyOverrideRecord
	}
	if !found {
		t.Errorf("TaskHistory receipts %+v omit the override's", h.Receipts)
	}
	if h, err := s.TaskHistory("app", "t-1"); err != nil || h.DependencyOverride != nil {
		t.Errorf("TaskHistory of a task with no override = %+v, %v", h.DependencyOverride, err)
	}
}
