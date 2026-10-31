package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// dependencyFixture is a store with open tasks app/t-1, app/a and app/a2, and lib/b, which depends on app/t-1 from
// creation, done app/done and cancelled app/gone.
func dependencyFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := newStore(t)
	for _, id := range []string{"t-1", "a", "a2", "done", "gone"} {
		mustCreate(t, s, "m-"+id, sampleTask("app", id))
	}
	b := sampleTask("lib", "b")
	b.DependsOn = []TaskRef{{Repository: "app", ID: "t-1"}}
	mustCreate(t, s, "m-b", b)
	if _, err := s.SetTaskState("close-done", "app", "done", TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTask("close-gone", "app", "gone", "Replaced."); err != nil {
		t.Fatal(err)
	}
	return s
}

func ref(repo, id string) TaskRef { return TaskRef{Repository: repo, ID: id} }

func TestAddTaskDependency(t *testing.T) {
	s := dependencyFixture(t)
	c := sampleTask("app", "c")
	c.DependsOn = []TaskRef{ref("app", "a")}
	mustCreate(t, s, "m-c", c)
	type step struct {
		m     string
		op    TaskDependencyOp
		dep   TaskRef
		prior int
	}
	// The d102/d103 case: app/a is replaced by app/a2, and app/done is added already satisfied.
	steps := []step{
		{"dep-1", TaskDependencyRemove, ref("app", "a"), 0},
		{"dep-2", TaskDependencyAdd, ref("app", "a2"), 0},
		{"dep-3", TaskDependencyAdd, ref("app", "done"), 0},
	}
	var receipts []Receipt
	for i, st := range steps {
		rc, err := s.AddTaskDependency(st.m, "app", "c", st.op, st.dep, st.prior, "Guildmaster: "+string(st.op)+" "+DependencyName(st.dep)+".")
		if err != nil || rc.Record != TaskDependencyRecord(i+1) || rc.Operation != "add-task-dependency" || rc.Repository != "app" || rc.TaskID != "c" {
			t.Fatalf("AddTaskDependency %d = %+v, %v", i+1, rc, err)
		}
		receipts = append(receipts, rc)
	}
	if replay, err := s.AddTaskDependency("dep-1", "app", "c", TaskDependencyRemove, ref("app", "a"), 0, "Guildmaster: remove app/a."); err != nil || replay != receipts[0] {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, receipts[0])
	}
	got, err := s.TaskDependencies("app", "c")
	if err != nil || len(got) != len(steps) {
		t.Fatalf("TaskDependencies = %+v, %v", got, err)
	}
	for i, x := range got {
		w := TaskDependency{Repository: "app", TaskID: "c", Number: i + 1, Op: steps[i].op, Dependency: steps[i].dep,
			Text: "Guildmaster: " + string(steps[i].op) + " " + DependencyName(steps[i].dep) + ".", Author: TaskDependencyAuthor, AddedAt: receipts[i].CommittedAt}
		if x != w {
			t.Errorf("record %d = %+v, want %+v", i+1, x, w)
		}
	}
	task, err := s.Task("app", "c")
	if err != nil || !slices.Equal(task.DependsOn, []TaskRef{ref("app", "a")}) {
		t.Fatalf("the Task record changed: %+v, %v", task.DependsOn, err)
	}
	if eff := EffectiveDependencies(task, got); !slices.Equal(eff, []TaskRef{ref("app", "a2"), ref("app", "done")}) {
		t.Errorf("EffectiveDependencies = %v", eff)
	}
	// Re-adding a removed creation-time dependency counts its earlier record.
	if _, err := s.AddTaskDependency("dep-4", "app", "c", TaskDependencyAdd, ref("app", "a"), 1, "Back again."); err != nil {
		t.Fatal(err)
	}
	h, err := s.TaskHistory("app", "c")
	if err != nil || len(h.Dependencies) != 4 || h.Receipts[len(h.Receipts)-1].Record != TaskDependencyRecord(4) {
		t.Fatalf("TaskHistory dependencies = %+v, receipts %+v, %v", h.Dependencies, h.Receipts, err)
	}
	if h, err := s.TaskHistory("app", "a"); err != nil || h.Dependencies == nil || len(h.Dependencies) != 0 {
		t.Errorf("TaskHistory of a task with no records = %+v, %v; want an empty list", h.Dependencies, err)
	}
}

func TestEffectiveDependencies(t *testing.T) {
	a, b, c := ref("app", "a"), ref("lib", "b"), ref("app", "c")
	task := Task{Repository: "app", ID: "t", DependsOn: []TaskRef{a, b, a}}
	rec := func(n int, op TaskDependencyOp, d TaskRef) TaskDependency {
		return TaskDependency{Number: n, Op: op, Dependency: d}
	}
	for label, tc := range map[string]struct {
		records []TaskDependency
		want    []TaskRef
	}{
		"creation list":         {nil, []TaskRef{a, b}},
		"add":                   {[]TaskDependency{rec(1, TaskDependencyAdd, c)}, []TaskRef{a, b, c}},
		"remove a creation one": {[]TaskDependency{rec(1, TaskDependencyRemove, a)}, []TaskRef{b}},
		"remove then re-add":    {[]TaskDependency{rec(1, TaskDependencyRemove, a), rec(2, TaskDependencyAdd, a)}, []TaskRef{b, a}},
		"add then remove":       {[]TaskDependency{rec(1, TaskDependencyAdd, c), rec(2, TaskDependencyRemove, c)}, []TaskRef{a, b}},
		"remove all":            {[]TaskDependency{rec(1, TaskDependencyRemove, a), rec(2, TaskDependencyRemove, b)}, nil},
	} {
		if got := EffectiveDependencies(task, tc.records); !slices.Equal(got, tc.want) {
			t.Errorf("%s: EffectiveDependencies = %v, want %v", label, got, tc.want)
		}
	}
}

func TestAddTaskDependencyRefusals(t *testing.T) {
	s := dependencyFixture(t)
	// app/a depends on lib/b, which depends on app/t-1 from creation.
	if _, err := s.AddTaskDependency("dep-a-b", "app", "a", TaskDependencyAdd, ref("lib", "b"), 0, "Wait for b."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTaskDependency("dep-t1-a2", "app", "t-1", TaskDependencyAdd, ref("app", "a2"), 0, "Wait for a2."); err != nil {
		t.Fatal(err)
	}
	for label, tc := range map[string]struct {
		task  string
		op    TaskDependencyOp
		dep   TaskRef
		prior int
		text  string
		want  error
	}{
		"unknown task":             {"t-9", TaskDependencyAdd, ref("app", "a2"), 0, "Wait.", ErrNotFound},
		"done task":                {"done", TaskDependencyAdd, ref("app", "a2"), 0, "Wait.", ErrInvalid},
		"self":                     {"t-1", TaskDependencyAdd, ref("app", "t-1"), 0, "Wait.", ErrInvalid},
		"unknown dependency":       {"t-1", TaskDependencyAdd, ref("app", "nope"), 0, "Wait.", ErrNotFound},
		"cancelled dependency":     {"t-1", TaskDependencyAdd, ref("app", "gone"), 0, "Wait.", ErrInvalid},
		"already effective":        {"t-1", TaskDependencyAdd, ref("app", "a2"), 1, "Wait.", ErrExists},
		"stale prior":              {"t-1", TaskDependencyRemove, ref("app", "a2"), 0, "Drop it.", ErrExists},
		"remove when not depended": {"t-1", TaskDependencyRemove, ref("app", "a"), 0, "Drop it.", ErrInvalid},
		"direct cycle":             {"a2", TaskDependencyAdd, ref("app", "t-1"), 0, "Wait.", ErrInvalid},
		"transitive cycle":         {"t-1", TaskDependencyAdd, ref("app", "a"), 0, "Wait.", ErrInvalid},
		"blank reason":             {"t-1", TaskDependencyRemove, ref("app", "a2"), 1, " \n", ErrInvalid},
		"oversized reason":         {"t-1", TaskDependencyRemove, ref("app", "a2"), 1, strings.Repeat("x", MaxTextBytes+1), ErrInvalid},
		"unknown op":               {"t-1", "move", ref("app", "a2"), 1, "Move it.", ErrInvalid},
		"negative prior":           {"t-1", TaskDependencyAdd, ref("app", "a"), -1, "Wait.", ErrInvalid},
	} {
		rc, err := s.AddTaskDependency("bad-"+strings.ReplaceAll(label, " ", "-"), "app", tc.task, tc.op, tc.dep, tc.prior, tc.text)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: AddTaskDependency = %+v, %v, want %v", label, rc, err, tc.want)
		}
	}
	// The cycle is named, across repositories: app/t-1 -> app/a -> lib/b -> app/t-1.
	_, err := s.AddTaskDependency("bad-cycle-named", "app", "t-1", TaskDependencyAdd, ref("app", "a"), 0, "Wait.")
	if err == nil || !strings.Contains(err.Error(), "app/t-1 -> app/a -> lib/b -> app/t-1") {
		t.Errorf("cycle error %v does not name the path", err)
	}
	// A done dependency is allowed.
	if _, err := s.AddTaskDependency("dep-t1-done", "app", "t-1", TaskDependencyAdd, ref("app", "done"), 0, "Already merged."); err != nil {
		t.Errorf("adding a done dependency: %v", err)
	}
	if got, err := s.TaskDependencies("app", "t-1"); err != nil || len(got) != 2 {
		t.Errorf("TaskDependencies after refusals = %+v, %v", got, err)
	}
	if _, err := s.AddTaskDependency("dep-t1-a2", "app", "t-1", TaskDependencyAdd, ref("app", "a2"), 0, "Other words."); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("reused mutation id: err = %v, want ErrMutationConflict", err)
	}
}

// TestOverrideDependenciesReadsEffectiveDependencies: a task whose only dependency was added later may be overridden,
// and one whose every dependency was removed may not.
func TestOverrideDependenciesReadsEffectiveDependencies(t *testing.T) {
	s := dependencyFixture(t)
	if _, err := s.AddTaskDependency("dep-1", "app", "t-1", TaskDependencyAdd, ref("app", "a"), 0, "Wait for a."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OverrideDependencies("override-t1", "app", "t-1", "Start anyway."); err != nil {
		t.Errorf("override of a task with an added dependency: %v", err)
	}
	if _, err := s.AddTaskDependency("dep-2", "lib", "b", TaskDependencyRemove, ref("app", "t-1"), 0, "Not needed."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OverrideDependencies("override-b", "lib", "b", "Start anyway."); !errors.Is(err, ErrInvalid) {
		t.Errorf("override of a task whose dependencies were all removed: %v, want ErrInvalid", err)
	}
}

func TestDoctorCountsAndDecodesTaskDependencies(t *testing.T) {
	s := dependencyFixture(t)
	for i, d := range []string{"a", "a2"} {
		if _, err := s.AddTaskDependency("dep-"+d, "app", "t-1", TaskDependencyAdd, ref("app", d), 0, "Wait for "+d+"."); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	app := slices.IndexFunc(rep.Repositories, func(r RepositoryCounts) bool { return r.Repository == "app" })
	if app < 0 || rep.Repositories[app].TaskDependencies != 2 {
		t.Fatalf("doctor repositories = %+v", rep.Repositories)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketTaskDependencies).Put([]byte("t-1/9999999999"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "repository app") || rep.Repositories[app].TaskDependencies != 3 {
		t.Fatalf("doctor after a bad record = %+v, %v", rep, err)
	}
}

// TestOldStoreWithoutTaskDependencies: a store written before dependency records existed opens, passes Doctor and
// reads no records, and a first change creates the bucket.
func TestOldStoreWithoutTaskDependencies(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor of the old store = %+v, %v", rep, err)
	}
	if got, err := s.TaskDependencies("app", "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("TaskDependencies of the old store = %+v, %v", got, err)
	}
	mustCreate(t, s, "m-dep", sampleTask("app", "t-2"))
	if _, err := s.AddTaskDependency("dep-1", "app", "t-1", TaskDependencyAdd, ref("app", "t-2"), 0, "Wait for t-2."); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 || rep.Repositories[0].TaskDependencies != 1 {
		t.Fatalf("doctor after the first change = %+v, %v", rep, err)
	}
}
