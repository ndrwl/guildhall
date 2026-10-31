package launcher

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// tasks writes the copy's director.store with a task for each entry, recording the release it names, or none for
// an empty version, with sha256 sum when given, else that installed release's.
func (f *fixture) tasks(copyDir string, recorded map[string][2]string) {
	f.t.Helper()
	s, err := store.Open(filepath.Join(copyDir, DirectorStore), store.Options{})
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d := "sha256:" + strings.Repeat("a", 64)
	for id, rel := range recorded {
		t := store.Task{Repository: "app", ID: id, Title: "A task", Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d}
		if rel[0] == "" {
			_, err = s.CreateTask("task:app:"+id, t)
		} else {
			sum := rel[1]
			if sum == "" {
				if sum, err = release.FileSHA256(f.self); err != nil {
					f.t.Fatal(err)
				}
			}
			_, err = s.CreateTaskWithRelease("task:app:"+id, t, store.TaskRelease{Version: rel[0], SHA256: sum})
		}
		if err != nil {
			f.t.Fatal(err)
		}
	}
}

// TestLauncherRunsATaskOnItsRecordedRelease: a task command runs on the release its task recorded, not the copy's
// pin, by every form a task is named in, including Herdr's plain resume argv from outside the copy and with an
// inherited GUILDHALL_RELEASE replaced; new work and an unrecorded task run on the pin; a copy with no pin still
// runs its recorded tasks.
func TestLauncherRunsATaskOnItsRecordedRelease(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	f.install("v2.0.0")
	a := f.copyDir("a", "v2.0.0")
	f.tasks(a, map[string][2]string{"old": {"v1.0.0"}, "legacy": {}})
	resume := launch.ResumeArgv(launch.Attempt{Store: filepath.Join(a, DirectorStore), StateDir: filepath.Join(a, "state"),
		Key: artisan.AttemptKey{Repository: "app", TaskID: "old", Step: "build", Attempt: 1}})
	if resume[0] != "guildhall" {
		t.Fatalf("resume argv %q does not start with the plain command", resume)
	}
	for _, tc := range []struct {
		name, dir string
		args      []string
		want      string
	}{
		{"task start", a, []string{"task", "start", "--repo", "app", "old"}, "v1.0.0"},
		{"task start --repo=, after the id", f.root, []string{"task", "start", "old", "--ignore-deps", "--reason", "now", "--repo=app", "--copy", a}, "v1.0.0"},
		{"step done", a, []string{"step", "done", "--repo", "app", "--task", "old", "--step", "build", "--attempt", "1"}, "v1.0.0"},
		{"artisan start", a, []string{"artisan", "start", "--repo", "app", "--task", "old", "--step", "build", "--attempt", "1"}, "v1.0.0"},
		{"Herdr's resume argv", f.root, resume[1:], "v1.0.0"},
		{"task design", a, []string{"task", "design", "--repo", "app", "--task", "old", "--brief", "b.md"}, "v1.0.0"},
		{"quality judge", a, []string{"quality", "judge", "--repo", "app", "--review", "1", "old"}, "v1.0.0"},
		{"an unrecorded task", a, []string{"task", "start", "--repo", "app", "legacy"}, "v2.0.0"},
		{"an unknown task", a, []string{"step", "done", "--repo", "app", "--task", "nope"}, "v2.0.0"},
		{"new work", a, []string{"task", "new", "--repo", "app", "--title", "x", "t-new"}, "v2.0.0"},
		{"a task's read-only command", a, []string{"task", "show", "--repo", "app", "--json", "old"}, "v2.0.0"},
		{"help", a, []string{"task", "start", "--help"}, "v2.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.launch(tc.dir, "", nil, tc.args...)
			if r.code != 0 || r.ran.Release != tc.want || !slices.Equal(r.ran.Args, tc.args) || r.stderr != "" {
				t.Fatalf("got exit %d, release %q, args %q, stderr %q; want %s", r.code, r.ran.Release, r.ran.Args, r.stderr, tc.want)
			}
		})
	}
	// Existing tasks do not depend on the current pin.
	if err := os.Remove(release.PinPath(a)); err != nil {
		t.Fatal(err)
	}
	if r := f.launch(a, "", nil, "task", "start", "--repo", "app", "old"); r.code != 0 || r.ran.Release != "v1.0.0" {
		t.Fatalf("with no pin: exit %d on %q, stderr %q; want v1.0.0", r.code, r.ran.Release, r.stderr)
	}
	if r := f.launch(a, "", nil, "task", "start", "--repo", "app", "legacy"); r.code != 1 || !strings.Contains(r.stderr, "no release pin") {
		t.Fatalf("an unrecorded task with no pin: exit %d, stderr %q; want the no-pin refusal", r.code, r.stderr)
	}
}

// TestLauncherRefusesAnUnusableTaskRelease: a task whose recorded release is missing, damaged or another binary
// under its version is refused, naming the version and the fix, and never moved to the pin or recovered; so is a
// task command that names no task, or two.
func TestLauncherRefusesAnUnusableTaskRelease(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	f.install("v2.0.0")
	f.install("v3.0.0")
	a := f.copyDir("a", "v3.0.0")
	f.tasks(a, map[string][2]string{"missing": {"v9.0.0"}, "damaged": {"v1.0.0"}, "other": {"v2.0.0", strings.Repeat("c", 64)}})
	if err := os.WriteFile(f.home.Binary("v1.0.0"), []byte("not the binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		task string
		want []string
	}{
		{"missing", []string{"records release v9.0.0", f.home.Binary("v9.0.0"), "never moved", "guildhall release install v9.0.0"}},
		{"damaged", []string{"records release v1.0.0", "damaged", "guildhall release install v1.0.0"}},
		{"other", []string{"records release v2.0.0", "not the " + strings.Repeat("c", 64), "--sha256 " + strings.Repeat("c", 64)}},
	} {
		r := f.launch(a, "", nil, "step", "done", "--repo", "app", "--task", tc.task)
		if r.code != 1 || r.stdout != "" {
			t.Fatalf("%s: got exit %d and output %q, want a refusal", tc.task, r.code, r.stdout)
		}
		for _, w := range tc.want {
			if !strings.Contains(r.stderr, w) {
				t.Fatalf("%s: stderr %q does not name %q", tc.task, r.stderr, w)
			}
		}
	}
	for _, args := range [][]string{
		{"step", "done", "--task", "missing"},
		{"step", "done", "--repo", "app"},
		{"task", "start", "--repo", "app"},
		{"step", "done", "--repo", "app", "--task", "missing", "--task", "other"},
		{"task", "start", "--repo", "app", "missing", "other"},
	} {
		r := f.launch(a, "", nil, args...)
		if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "never run as new work") && !strings.Contains(r.stderr, "conflicting tasks") {
			t.Fatalf("%v: got exit %d, output %q, stderr %q; want a refusal of its task identity", args, r.code, r.stdout, r.stderr)
		}
	}
}

// TestTaskCommand: which commands are task commands and the task each names.
func TestTaskCommand(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		isTask bool
		want   Task
		err    string
	}{
		{[]string{"task", "start", "--copy", "c", "--repo", "r", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"task", "cancel", "--reason", "gone", "--repo=r", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"task", "reconcile", "--repo", "r", "--", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"step", "finish", "--failed", "--repo", "r", "--task", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"step", "send-back", "--repo", "r", "--task=t", "--feedback", "more"}, true, Task{"r", "t"}, ""},
		{[]string{"artisan", "resume", "--repo", "r", "--task", "t", "--task", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"quality", "judged", "--repo", "r", "--task", "t"}, true, Task{"r", "t"}, ""},
		{[]string{"task", "design", "--repo", "r", "--task", "t", "--brief", "b.md"}, true, Task{"r", "t"}, ""},
		{[]string{"task", "design", "--repo", "r", "--brief", "b.md"}, true, Task{}, "needs --repo and --task"},
		{[]string{"task", "start", "--repo", "r"}, true, Task{}, "needs --repo and the task id"},
		{[]string{"artisan", "start", "--task", "t"}, true, Task{}, "needs --repo and --task"},
		{[]string{"step", "retry", "--repo", "r", "--repo", "s", "--task", "t"}, true, Task{}, "conflicting tasks"},
		{[]string{"step", "done", "--repo"}, true, Task{}, "needs a value"},
		{[]string{"task", "new", "--repo", "r", "t"}, false, Task{}, ""},
		{[]string{"task", "show", "--repo", "r", "t"}, false, Task{}, ""},
		{[]string{"artisan", "stop", "--repo", "r", "--task", "t"}, false, Task{}, ""},
		{[]string{"step", "done", "-h"}, false, Task{}, ""},
		{[]string{"task"}, false, Task{}, ""},
	} {
		got, isTask, err := TaskCommand(tc.args)
		if isTask != tc.isTask || got != tc.want || (tc.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: got %+v, %v, %v; want %+v, %v, %q", tc.args, got, isTask, err, tc.want, tc.isTask, tc.err)
		}
	}
}
