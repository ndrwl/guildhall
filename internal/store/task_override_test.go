package store

import (
	"errors"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// overrideFixture is a store with an open task app/t-1 and a done task app/t-done.
func overrideFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-done", sampleTask("app", "t-done"))
	if _, err := s.SetTaskState("close-done", "app", "t-done", TaskDone); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAddTaskOverride: sets and unsets share one numbered sequence, set A → B → A records three times, the latest
// record per key wins, a replay returns the original receipt and the task's history reads the records.
func TestAddTaskOverride(t *testing.T) {
	s := overrideFixture(t)
	type step struct {
		m     string
		op    TaskOverrideOp
		key   string
		value string
		prior int
	}
	steps := []step{
		{"o-1", TaskOverrideSet, "default_effort", "high", 0},
		{"o-2", TaskOverrideSet, "stall_interval", "2m", 0},
		{"o-3", TaskOverrideSet, "default_effort", "low", 1},
		{"o-4", TaskOverrideSet, "default_effort", "high", 2},
		{"o-5", TaskOverrideUnset, "stall_interval", "", 1},
		{"o-6", TaskOverrideSet, "ci_log_limit", "1MiB", 0},
	}
	var receipts []Receipt
	for i, st := range steps {
		rc, err := s.AddTaskOverride(st.m, "app", "t-1", st.op, st.key, st.value, st.prior, "Guildmaster: same words.")
		if err != nil || rc.Record != TaskOverrideRecord(i+1) || rc.Operation != "add-task-override" || rc.Repository != "app" || rc.TaskID != "t-1" {
			t.Fatalf("AddTaskOverride %d = %+v, %v", i+1, rc, err)
		}
		receipts = append(receipts, rc)
	}
	if replay, err := s.AddTaskOverride("o-4", "app", "t-1", TaskOverrideSet, "default_effort", "high", 2, "Guildmaster: same words."); err != nil || replay != receipts[3] {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, receipts[3])
	}
	got, err := s.TaskOverrides("app", "t-1")
	if err != nil || len(got) != len(steps) {
		t.Fatalf("TaskOverrides = %+v, %v", got, err)
	}
	for i, o := range got {
		w := TaskOverride{Repository: "app", TaskID: "t-1", Number: i + 1, Op: steps[i].op, Key: steps[i].key, Value: steps[i].value,
			Text: "Guildmaster: same words.", Author: TaskOverrideAuthor, AddedAt: receipts[i].CommittedAt}
		if o != w {
			t.Errorf("record %d = %+v, want %+v", i+1, o, w)
		}
	}
	active := ActiveTaskOverrides(got)
	if len(active) != 2 || active[0].Number != 6 || active[1].Number != 4 || active[1].Value != "high" {
		t.Errorf("ActiveTaskOverrides = %+v, want records 6 (ci_log_limit) and 4 (default_effort high)", active)
	}
	if active := ActiveTaskOverrides(nil); len(active) != 0 {
		t.Errorf("ActiveTaskOverrides(nil) = %+v", active)
	}

	h, err := s.TaskHistory("app", "t-1")
	if err != nil || !slices.Equal(h.Overrides, got) || h.Receipts[len(h.Receipts)-1].Record != TaskOverrideRecord(6) {
		t.Fatalf("TaskHistory overrides = %+v, receipts %+v, %v", h.Overrides, h.Receipts, err)
	}
	if h, err := s.TaskHistory("app", "t-done"); err != nil || h.Overrides == nil || len(h.Overrides) != 0 {
		t.Errorf("TaskHistory of a task without overrides = %+v, %v", h.Overrides, err)
	}
}

func TestAddTaskOverrideRefusals(t *testing.T) {
	s := overrideFixture(t)
	if _, err := s.AddTaskOverride("o-1", "app", "t-1", TaskOverrideSet, "default_effort", "high", 0, "Words."); err != nil {
		t.Fatal(err)
	}
	for label, tc := range map[string]struct {
		task  string
		op    TaskOverrideOp
		key   string
		value string
		prior int
		text  string
		want  error
	}{
		"unknown task":          {"t-9", TaskOverrideSet, "default_effort", "high", 0, "Words.", ErrNotFound},
		"done task":             {"t-done", TaskOverrideSet, "default_effort", "high", 0, "Words.", ErrInvalid},
		"empty key":             {"t-1", TaskOverrideSet, "", "high", 0, "Words.", ErrInvalid},
		"spaced key":            {"t-1", TaskOverrideSet, "default effort", "high", 0, "Words.", ErrInvalid},
		"key with newline":      {"t-1", TaskOverrideSet, "default_effort\n", "high", 0, "Words.", ErrInvalid},
		"step key":              {"t-1", TaskOverrideSet, "step.build.effort", "max", 0, "Words.", ErrInvalid},
		"set without value":     {"t-1", TaskOverrideSet, "stall_interval", "", 0, "Words.", ErrInvalid},
		"unset with value":      {"t-1", TaskOverrideUnset, "default_effort", "high", 1, "Words.", ErrInvalid},
		"oversized value":       {"t-1", TaskOverrideSet, "stall_interval", strings.Repeat("x", MaxTextBytes+1), 0, "Words.", ErrInvalid},
		"stale prior":           {"t-1", TaskOverrideSet, "default_effort", "low", 0, "Words.", ErrExists},
		"prior ahead":           {"t-1", TaskOverrideSet, "stall_interval", "2m", 1, "Words.", ErrExists},
		"negative prior":        {"t-1", TaskOverrideSet, "stall_interval", "2m", -1, "Words.", ErrInvalid},
		"unset when not in set": {"t-1", TaskOverrideUnset, "stall_interval", "", 0, "Words.", ErrInvalid},
		"blank text":            {"t-1", TaskOverrideSet, "stall_interval", "2m", 0, " \n", ErrInvalid},
		"oversized text":        {"t-1", TaskOverrideSet, "stall_interval", "2m", 0, strings.Repeat("x", MaxTextBytes+1), ErrInvalid},
		"unknown op":            {"t-1", "clear", "default_effort", "", 1, "Words.", ErrInvalid},
	} {
		rc, err := s.AddTaskOverride("bad-"+strings.ReplaceAll(label, " ", "-"), "app", tc.task, tc.op, tc.key, tc.value, tc.prior, tc.text)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: AddTaskOverride = %+v, %v, want %v", label, rc, err, tc.want)
		}
	}
	// Unset of a key no longer in effect is refused too, once its unset is recorded.
	if _, err := s.AddTaskOverride("o-2", "app", "t-1", TaskOverrideUnset, "default_effort", "", 1, "Drop it."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTaskOverride("o-3", "app", "t-1", TaskOverrideUnset, "default_effort", "", 2, "Drop it again."); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no override of default_effort in effect") {
		t.Errorf("second unset: err = %v", err)
	}
	if got, err := s.TaskOverrides("app", "t-1"); err != nil || len(got) != 2 {
		t.Errorf("TaskOverrides after refusals = %+v, %v", got, err)
	}
	if got, err := s.TaskOverrides("app", "t-done"); err != nil || len(got) != 0 {
		t.Errorf("TaskOverrides of the done task = %+v, %v", got, err)
	}
	if _, err := s.AddTaskOverride("o-1", "app", "t-1", TaskOverrideSet, "default_effort", "low", 0, "Words."); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("reused mutation id: err = %v, want ErrMutationConflict", err)
	}
}

func TestDoctorCountsAndDecodesTaskOverrides(t *testing.T) {
	s := overrideFixture(t)
	for i, key := range []string{"default_effort", "stall_interval"} {
		if _, err := s.AddTaskOverride("o-"+string(rune('1'+i)), "app", "t-1", TaskOverrideSet, key, "x", 0, "Words."); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	app := slices.IndexFunc(rep.Repositories, func(r RepositoryCounts) bool { return r.Repository == "app" })
	if app < 0 || rep.Repositories[app].TaskOverrides != 2 {
		t.Fatalf("doctor repositories = %+v", rep.Repositories)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketTaskOverrides).Put([]byte("t-1/9999999999"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "repository app") || rep.Repositories[app].TaskOverrides != 3 {
		t.Fatalf("doctor after a bad record = %+v, %v", rep, err)
	}
}

// TestDirectorViewOverrides: the Director's view lists each task's overrides in effect, in task and key order, and
// none of a task whose only override was unset.
func TestDirectorViewOverrides(t *testing.T) {
	s := overrideFixture(t)
	mustCreate(t, s, "m-10", sampleTask("app", "t-10"))
	for _, o := range []struct {
		m, task, key string
		op           TaskOverrideOp
		value        string
		prior        int
	}{
		{"o-1", "t-10", "stall_interval", TaskOverrideSet, "2m", 0},
		{"o-2", "t-1", "stall_interval", TaskOverrideSet, "2m", 0},
		{"o-3", "t-1", "default_effort", TaskOverrideSet, "high", 0},
		{"o-4", "t-10", "stall_interval", TaskOverrideUnset, "", 1},
	} {
		if _, err := s.AddTaskOverride(o.m, "app", o.task, o.op, o.key, o.value, o.prior, "Guildmaster: so."); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range v.Overrides {
		got = append(got, o.TaskID+" "+o.Key+"="+o.Value)
	}
	if want := []string{"t-1 default_effort=high", "t-1 stall_interval=2m"}; !slices.Equal(got, want) {
		t.Errorf("DirectorView overrides = %v, want %v", got, want)
	}
}
