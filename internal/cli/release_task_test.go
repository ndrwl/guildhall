package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskRelease is the release task app/id recorded in the fixture's store.
func (f *pinFixture) taskRelease(id string) (store.TaskRelease, error) {
	f.t.Helper()
	return withStore(filepath.Join(f.copy, "director.store"), true, func(s *store.Store) (store.TaskRelease, error) { return s.TaskRelease("app", id) })
}

// newTask runs task new for app/id on the fixture's copy, its workflow approved, and returns its exit code.
func (f *pinFixture) newTask(id string) int {
	f.t.Helper()
	approveRepo(f.t, filepath.Join(f.copy, "director.store"), filepath.Join(f.copy, "config.toml"), "app")
	return f.run("task", "new", "--copy", f.copy, "--repo", "app", "--title", "Task "+id, id)
}

// install installs a release file holding body as version, reporting store schema schema, and returns its sha256.
func (f *pinFixture) install(version, body string, schema int) string {
	f.t.Helper()
	from, sum := releaseFile(f.t, body)
	f.report = `{"version":"` + version + `","modified":false,"store_schema":` + string(rune('0'+schema)) + `,"release_contract":2}`
	if code := f.run("release", "install", version, "--from", from, "--sha256", sum); code != 0 {
		f.t.Fatalf("release install %s = %d", version, code)
	}
	return sum
}

// TestTaskNewRecordsThePin: on a copy that has not adopted a task records no release, as before; after adopt,
// task new records the copy's pinned version and sha256 and registers the copy; a later pin change leaves an
// existing task on its release while new tasks record the new pin; and a pin whose release is damaged refuses
// task new, creating nothing.
func TestTaskNewRecordsThePin(t *testing.T) {
	f := newPinFixture(t)
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	if code := f.newTask("t-0"); code != 0 {
		t.Fatalf("task new before adopt = %d", code)
	}
	if _, err := f.taskRelease("t-0"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a task created before adopt recorded a release: %v", err)
	}
	if fileExists(f.home) {
		t.Fatal("task new on a copy that has not adopted touched the Guildhall home")
	}
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	adopted, err := h.Runnable("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.CopiesFile(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.newTask("t-1"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	if r, err := f.taskRelease("t-1"); err != nil || r.Version != "v1.0.0" || r.SHA256 != adopted.SHA256 {
		t.Fatalf("t-1 release %+v, %v; want v1.0.0 %s", r, err, adopted.SHA256)
	}
	if copies, err := h.Copies(); err != nil || !slices.Equal(copies, []string{f.copy}) {
		t.Fatalf("task new did not register the copy: %q, %v", copies, err)
	}
	sum := f.install("v1.1.0", "release v1.1.0", 1)
	if code := f.run("release", "use", "v1.1.0", "--copy", f.copy); code != 0 {
		t.Fatalf("release use = %d", code)
	}
	if code := f.newTask("t-2"); code != 0 {
		t.Fatalf("task new after use = %d", code)
	}
	if r, err := f.taskRelease("t-1"); err != nil || r.Version != "v1.0.0" {
		t.Fatalf("the pin change moved t-1: %+v, %v", r, err)
	}
	if r, err := f.taskRelease("t-2"); err != nil || r.Version != "v1.1.0" || r.SHA256 != sum {
		t.Fatalf("t-2 release %+v, %v; want v1.1.0 %s", r, err, sum)
	}
	if err := os.WriteFile(h.Binary("v1.1.0"), []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.newTask("t-3"); code != 1 || !strings.Contains(f.stderr.String(), "pins v1.1.0") || !strings.Contains(f.stderr.String(), "damaged") {
		t.Fatalf("task new on a damaged pin = %d, %q", code, f.stderr)
	}
	tasks, err := withStore(filepath.Join(f.copy, "director.store"), true, func(s *store.Store) ([]store.Task, error) { return s.Tasks("app") })
	if err != nil || len(tasks) != 3 {
		t.Fatalf("tasks %d, %v; want t-0, t-1 and t-2 only", len(tasks), err)
	}
}

// TestReleaseUseRefusesAStoreSchemaChange: release use refuses a release whose store schema differs from the
// release an active task records, naming the task, and changes no pin; once the task is cancelled the pin moves.
func TestReleaseUseRefusesAStoreSchemaChange(t *testing.T) {
	f := newPinFixture(t)
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if code := f.newTask("t-1"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	f.install("v2.0.0", "release v2.0.0", 2)
	if code := f.run("release", "use", "v2.0.0", "--copy", f.copy); code != 1 ||
		!strings.Contains(f.stderr.String(), "store schema 2") || !strings.Contains(f.stderr.String(), "app/t-1 on v1.0.0 (store schema 1)") {
		t.Fatalf("release use across a store schema = %d, %q", code, f.stderr)
	}
	if pin, _ := release.ReadPin(f.copy); pin != "v1.0.0" || len(f.pins()) != 1 {
		t.Fatalf("a refused release use moved the pin to %q", pin)
	}
	if _, err := withStore(filepath.Join(f.copy, "director.store"), false, func(s *store.Store) (store.Receipt, error) {
		return s.CancelTask("cancel-t-1", "app", "t-1", "drained")
	}); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "use", "v2.0.0", "--copy", f.copy); code != 0 {
		t.Fatalf("release use once the task is cancelled = %d, %q", code, f.stderr)
	}
}

// TestReleaseStatusListsTasks: status lists the selected copy's active tasks with the release each recorded, the
// unrecorded ones, which run on the pin, and the tasks holding each release; a task whose release is damaged is a
// problem there and a release needs_me item.
func TestReleaseStatusListsTasks(t *testing.T) {
	f := newPinFixture(t)
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	if code := f.newTask("t-0"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if code := f.newTask("t-1"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	if code := f.run("release", "status", "--json", "--copy", f.copy); code != 0 {
		t.Fatalf("release status = %d", code)
	}
	var rep releaseStatusReport
	if err := json.Unmarshal([]byte(f.stdout.String()), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Copy == nil || !slices.Equal(rep.Copy.Unrecorded, []string{"app/t-0"}) || len(rep.Copy.Tasks) != 1 ||
		rep.Copy.Tasks[0].TaskID != "t-1" || rep.Copy.Tasks[0].Version != "v1.0.0" || rep.Copy.Tasks[0].Problem != "" {
		t.Fatalf("copy %+v", rep.Copy)
	}
	if len(rep.Releases) != 1 || !slices.Equal(rep.Releases[0].Tasks, []string{"app/t-1"}) {
		t.Fatalf("releases %+v", rep.Releases)
	}
	if items := f.app.taskReleaseItems(filepath.Join(f.copy, "director.store")); len(items) != 0 {
		t.Fatalf("release items for an intact release: %+v", items)
	}
	if err := os.WriteFile(h.Binary("v1.0.0"), []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "status", "--copy", f.copy); code != 0 ||
		!strings.Contains(f.stdout.String(), "task app/t-1 runs on v1.0.0, which is damaged") || !strings.Contains(f.stdout.String(), "task app/t-0 recorded no release and runs on the pin") {
		t.Fatalf("release status = %d, %q", code, f.stdout)
	}
	items := f.app.taskReleaseItems(filepath.Join(f.copy, "director.store"))
	if len(items) != 1 || items[0].Kind != releaseKind || items[0].TaskID != "t-1" || !strings.Contains(items[0].Reason, "records release v1.0.0, which is damaged") ||
		!strings.HasPrefix(items[0].Command, "guildhall release install v1.0.0 --from <file> --sha256 ") {
		t.Fatalf("release items %+v", items)
	}
}

// TestWatcherAdvancesAnotherReleaseThroughTheLauncher: a task that recorded another release than the supervisor's
// is advanced by the home's launcher with the copy selection and task identity, and its next.json taken from it;
// an unrecorded task advances in process.
func TestWatcherAdvancesAnotherReleaseThroughTheLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake launcher is a shell script")
	}
	f := newPinFixture(t)
	storePath := filepath.Join(f.copy, "director.store")
	d := "sha256:" + strings.Repeat("a", 64)
	if _, err := withStore(storePath, false, func(s *store.Store) (store.Receipt, error) {
		for _, id := range []string{"old", "legacy"} {
			task := store.Task{Repository: "app", ID: id, Title: "T", Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d}
			var err error
			if id == "old" {
				_, err = s.CreateTaskWithRelease("task-"+id, task, store.TaskRelease{Version: "v1.0.0", SHA256: strings.Repeat("b", 64)})
			} else {
				_, err = s.CreateTask("task-"+id, task)
			}
			if err != nil {
				return store.Receipt{}, err
			}
		}
		return store.Receipt{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, other, err := otherTaskRelease(storePath, 0, taskRef{"app", "legacy"}); err != nil || other {
		t.Fatalf("an unrecorded task: other %v, %v", other, err)
	}
	rec, other, err := otherTaskRelease(storePath, 0, taskRef{"app", "old"})
	if err != nil || !other || rec.Version != "v1.0.0" {
		t.Fatalf("a task on v1.0.0 from %s: %+v, other %v, %v", Version(), rec, other, err)
	}
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	argsFile := filepath.Join(t.TempDir(), "args")
	if err := os.MkdirAll(h.BinDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho launched >&2\necho '{\"wait\":\"attempt\",\"step\":\"build\",\"attempt\":2}'\n"
	if err := os.WriteFile(h.Launcher(), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	n, err := f.app.advanceThroughLauncher(advanceFlags{copy: copyFlags{dir: f.copy}}, taskRef{"app", "old"}, rec, &log)
	if err != nil || n.Wait != "attempt" || n.Step != "build" || n.Attempt != 2 || strings.TrimSpace(log.String()) != "launched" {
		t.Fatalf("advance: %+v, %v, log %q", n, err, log.String())
	}
	if got := strings.TrimSpace(readText(t, argsFile)); got != "task start --copy "+f.copy+" --repo app old" {
		t.Fatalf("the launcher ran with %q", got)
	}
}
