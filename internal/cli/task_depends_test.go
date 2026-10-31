package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestTaskDependsAddRemove: add records a dependency with its reason and prints its receipt, a rerun prints the same
// receipt and writes nothing, remove drops one the task was created with, and task show prints the effective
// depends_on while task timeline lists every record with its reason.
func TestTaskDependsAddRemove(t *testing.T) {
	path := goldenStore(t)
	// app/t-2 depends on lib/t-1 from creation; it is replaced by app/t-1.
	add := []string{"depends", "add", "--store", path, "--repo", "app", "--reason", "lib/t-1 was replaced by app/t-1.", "t-2", "app/t-1"}
	code, out, stderr := runTask(t, add...)
	if code != 0 || stderr != "" {
		t.Fatalf("add: exit %d, stderr %s", code, stderr)
	}
	rc := decodeJSON[dependsReceipt](t, out)
	if rc.Record != "task-dependency/1" || rc.Operation != "add-task-dependency" || rc.MutationID != "task-dependency:app:t-2:add:app:t-1:0" {
		t.Fatalf("add receipt = %+v", rc)
	}
	if code, again, _ := runTask(t, "depends", "add", "t-2", "--reason", "lib/t-1 was replaced by app/t-1.", "app/t-1", "--repo", "app", "--store", path); code != 0 || again != out {
		t.Fatalf("rerun: exit %d, %s, want %s", code, again, out)
	}
	code, out, stderr = runTask(t, "depends", "remove", "--store", path, "--repo", "app", "--reason", "Replaced by app/t-1.", "t-2", "lib/t-1")
	if rc := decodeJSON[dependsReceipt](t, out); code != 0 || rc.Record != "task-dependency/2" || rc.MutationID != "task-dependency:app:t-2:remove:lib:t-1:0" || rc.Note != "" {
		t.Fatalf("remove: exit %d, %s, stderr %s", code, out, stderr)
	}

	code, out, stderr = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-2")
	if code != 0 {
		t.Fatalf("show: exit %d, %s", code, stderr)
	}
	if got := decodeJSON[taskDetail](t, out).Task.DependsOn; !slices.Equal(got, []store.TaskRef{{Repository: "app", ID: "t-1"}}) {
		t.Errorf("show depends_on = %v, want app/t-1 only", got)
	}
	code, out, stderr = runTask(t, "list", "--store", path, "--repo", "app", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d, %s", code, stderr)
	}
	for _, v := range decodeJSON[[]taskView](t, out) {
		if v.ID == "t-2" && !slices.Equal(v.DependsOn, []store.TaskRef{{Repository: "app", ID: "t-1"}}) {
			t.Errorf("list depends_on = %v, want app/t-1 only", v.DependsOn)
		}
	}
	code, out, stderr = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-2")
	if code != 0 {
		t.Fatalf("timeline: exit %d, %s", code, stderr)
	}
	tl := decodeJSON[taskTimeline](t, out)
	if !slices.Equal(tl.Task.DependsOn, []store.TaskRef{{Repository: "lib", ID: "t-1"}}) {
		t.Errorf("timeline task depends_on = %v, want the creation list", tl.Task.DependsOn)
	}
	if len(tl.Dependencies) != 2 {
		t.Fatalf("timeline dependencies = %+v", tl.Dependencies)
	}
	for i, d := range tl.Dependencies {
		op := []store.TaskDependencyOp{store.TaskDependencyAdd, store.TaskDependencyRemove}[i]
		dep := []string{"app/t-1", "lib/t-1"}[i]
		text := []string{"lib/t-1 was replaced by app/t-1.", "Replaced by app/t-1."}[i]
		if d.Number != i+1 || d.Op != op || store.DependencyName(d.Dependency) != dep || d.Text != text || d.Author != "director" || d.AddedAt.IsZero() {
			t.Errorf("timeline dependency %d = %+v", i+1, d)
		}
	}
	if !slices.ContainsFunc(tl.Events, func(r store.Receipt) bool { return r.Record == "task-dependency/2" }) {
		t.Errorf("timeline events omit task-dependency/2: %+v", tl.Events)
	}
	app, stdout, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "doctor", path}); code != 0 || !strings.Contains(stdout.String(), "task dependencies 2,") {
		t.Errorf("store doctor: exit %d, %s", code, stdout)
	}
}

// TestTaskDependsRefusals: malformed commands are usage errors that write nothing, and the store's refusals exit 1.
func TestTaskDependsRefusals(t *testing.T) {
	path := goldenStore(t)
	base := func(args ...string) []string {
		return append([]string{"--store", path, "--repo", "app", "--reason", "Why."}, args...)
	}
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no subcommand":      {nil, 2, "missing task depends subcommand"},
		"unknown subcommand": {[]string{"list", "t-2"}, 2, "unknown task depends subcommand"},
		"no reason":          {[]string{"add", "--store", path, "--repo", "app", "t-1", "lib/t-1"}, 2, "--reason"},
		"blank reason":       {[]string{"add", "--store", path, "--repo", "app", "--reason", " ", "t-1", "lib/t-1"}, 2, "--reason"},
		"oversized reason":   {[]string{"add", "--store", path, "--repo", "app", "--reason", strings.Repeat("r", store.MaxTextBytes+1), "t-1", "lib/t-1"}, 2, "limit"},
		"no repo":            {[]string{"add", "--store", path, "--reason", "Why.", "t-1", "lib/t-1"}, 2, "--repo"},
		"one argument":       {append([]string{"add"}, base("t-1")...), 2, "2 arguments"},
		"bare dependency":    {append([]string{"add"}, base("t-1", "t-2")...), 2, "<repository>/<task>"},
		"three-part dep":     {append([]string{"add"}, base("t-1", "lib/t-1/x")...), 2, "<repository>/<task>"},
		"unknown flag":       {append([]string{"add", "--text", "x"}, base("t-1", "lib/t-1")...), 2, "-text"},
		"self":               {append([]string{"add"}, base("t-1", "app/t-1")...), 1, "itself"},
		"unknown dependency": {append([]string{"add"}, base("t-1", "lib/t-9")...), 1, "not found"},
		"unknown task":       {append([]string{"add"}, base("t-9", "lib/t-1")...), 1, "not found"},
		"already effective":  {append([]string{"add"}, base("t-2", "lib/t-1")...), 1, "already depends on lib/t-1"},
		"not a dependency":   {append([]string{"remove"}, base("t-1", "lib/t-1")...), 1, "does not depend on lib/t-1"},
	} {
		code, out, stderr := runTask(t, append([]string{"depends"}, tc.args...)...)
		if code != tc.code || !strings.Contains(stderr, tc.want) || out != "" {
			t.Errorf("%s: exit %d, out %q, stderr %s; want exit %d naming %q", name, code, out, stderr, tc.code, tc.want)
		}
	}
	// A cycle across repositories: app/t-2 depends on lib/t-1, so lib/t-1 cannot depend on app/t-2.
	code, _, stderr := runTask(t, "depends", "add", "--store", path, "--repo", "lib", "--reason", "Why.", "t-1", "app/t-2")
	if code != 1 || !strings.Contains(stderr, "lib/t-1 -> app/t-2 -> lib/t-1") {
		t.Errorf("cycle: exit %d, %s", code, stderr)
	}
	h, err := withStore(path, true, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory("app", "t-1") })
	if err != nil || len(h.Dependencies) != 0 {
		t.Errorf("a refused change was recorded: %+v, %v", h.Dependencies, err)
	}
	if code, out, _ := runTask(t, "depends", "--help"); code != 2 || out != "" {
		t.Errorf("depends --help with no subcommand: exit %d, %s", code, out)
	}
}

// TestTaskDependsNotes: an add to a task with a dependency override, or one past the dependency gate, is recorded
// and its receipt carries a note saying it holds nothing; an add to a task before the gate carries none.
func TestTaskDependsNotes(t *testing.T) {
	d := newDependentDirector(t)
	newTask(t, d.store, d.config, "app", "Another", "t-3")
	newTask(t, d.store, d.config, "app", "And another", "t-4")
	depends := func(task, dep string) dependsReceipt {
		t.Helper()
		code, out, stderr := runTask(t, "depends", "add", "--store", d.store, "--config", d.config, "--repo", "app", "--reason", "Wait for "+dep+".", task, dep)
		if code != 0 {
			t.Fatalf("depends add %s %s: exit %d, %s", task, dep, code, stderr)
		}
		return decodeJSON[dependsReceipt](t, out)
	}
	if rc := depends("t-3", "app/t-1"); rc.Note != "" {
		t.Errorf("a task before the gate was noted: %q", rc.Note)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.OverrideDependencies("dependency-override.app.t-2", "app", "t-2", "Start anyway.")
	}); err != nil {
		t.Fatal(err)
	}
	if rc := depends("t-2", "app/t-3"); rc.Note != dependsNoteOverride || rc.Record != "task-dependency/1" {
		t.Errorf("an overridden task: %+v, want the override note", rc)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("start-t-4", "app", "t-4", "plan")
	}); err != nil {
		t.Fatal(err)
	}
	if rc := depends("t-4", "app/t-1"); rc.Note != dependsNotePastGate || rc.Record != "task-dependency/1" {
		t.Errorf("a task past the gate: %+v, want the past-gate note", rc)
	}
}

// TestTaskStartIgnoreDepsReadsEffectiveDependencies: --ignore-deps overrides a task whose only dependency was added
// after creation.
func TestTaskStartIgnoreDepsReadsEffectiveDependencies(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Sort the archive too", "t-2")
	code, _, stderr := d.run(t, append([]string{"task", "start", "--ignore-deps", "--reason", "why"}, d.flags("t-2")...)...)
	if code != 1 || !strings.Contains(stderr, "no dependencies") {
		t.Fatalf("an override with no dependencies: exit %d, %s", code, stderr)
	}
	if code, _, stderr := runTask(t, "depends", "add", "--store", d.store, "--repo", "app", "--reason", "Wait for t-1.", "t-2", "app/t-1"); code != 0 {
		t.Fatalf("depends add: exit %d, %s", code, stderr)
	}
	code, stdout, stderr := d.run(t, append([]string{"task", "start", "--ignore-deps", "--reason", "t-2 touches none of t-1's files."}, d.flags("t-2")...)...)
	if code != 0 || decodeJSON[next](t, stdout).Wait == "dependencies" {
		t.Fatalf("task start --ignore-deps: exit %d, %s %s", code, stdout, stderr)
	}
	h, err := withStore(d.store, true, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory("app", "t-2") })
	if err != nil || h.DependencyOverride == nil {
		t.Errorf("override = %+v, %v; want it recorded", h.DependencyOverride, err)
	}
}

// TestTellDependentsReadsEffectiveDependencies: cancelling a task tells a dependent that added it after creation,
// and not one that removed it.
func TestTellDependentsReadsEffectiveDependencies(t *testing.T) {
	c := newCancelDirector(t)
	newTask(t, c.store, c.config, "app", "Depended on t-1, no longer", "t-2", "app/t-1")
	newTask(t, c.store, c.config, "app", "Depends on t-1 now", "t-3")
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.AddTaskDependency("dep-t-2", "app", "t-2", store.TaskDependencyRemove, store.TaskRef{Repository: "app", ID: "t-1"}, 0, "Not needed.")
	})
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.AddTaskDependency("dep-t-3", "app", "t-3", store.TaskDependencyAdd, store.TaskRef{Repository: "app", ID: "t-1"}, 0, "Needs it.")
	})
	cfg, err := config.Load(c.config)
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	say := func(format string, args ...any) { told = append(told, args[1].(string)) }
	if err := tellDependents(storeRecords{path: c.store}, cfg, store.TaskRef{Repository: "app", ID: "t-1"}, "Duplicate.", say); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(told, []string{"t-3"}) {
		t.Errorf("told = %q, want t-3 only", told)
	}
}
