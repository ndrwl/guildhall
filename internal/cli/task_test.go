package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// directorTOML has repository "app" on an explicit fake-profile workflow and "lib" on the human-review example
// with its review swapped back to an Artisan. Paths are fixed so digests and goldens are stable.
const directorTOML = `schema_version = 1
mode = "director"
name = "alpha"

[local]

[profile.demo]
adapter = "fake"

[[repository]]
id = "app"
task_prefix = "t"
path = "/srv/app"
workflow = "test-default"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"

[[repository]]
id = "lib"
task_prefix = "l"
path = "/srv/lib"
remote = "git@github.com:example/lib.git"
owner_machine = "workstation"
workflow = "human-review"

[repository.workflow_overrides.review]
actor = "artisan"
`

// writeDirector writes config.toml and workflows/human-review.json into a new directory whose name has a space,
// and returns the config's path.
func writeDirector(t *testing.T, toml string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "director config")
	if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "workflows", "human-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	for i := range def.Steps {
		if def.Steps[i].Type == "plan" || def.Steps[i].Type == "build" || def.Steps[i].Type == "review" {
			if def.Steps[i].Settings == nil {
				def.Steps[i].Settings = map[string]json.RawMessage{}
			}
			def.Steps[i].Settings["profile"] = json.RawMessage(`"demo"`)
		}
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "workflows", "human-review.json"), string(data))
	data, err = json.Marshal(testDefault(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "workflows", "test-default.json"), string(data))
	writeFile(t, filepath.Join(dir, "config.toml"), toml)
	return filepath.Join(dir, "config.toml")
}

func testDefault(t *testing.T) *workflow.Definition {
	t.Helper()
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	def.Name = "test-default"
	for i := range def.Steps {
		if def.Steps[i].Actor() == workflow.ArtisanActor {
			if def.Steps[i].Settings == nil {
				def.Steps[i].Settings = map[string]json.RawMessage{}
			}
			def.Steps[i].Settings["profile"] = json.RawMessage(`"demo"`)
		}
	}
	return def
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// emptyStore returns an initialized store with no records.
func emptyStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func runTask(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	code := app.Run(append([]string{"task"}, args...))
	return code, stdout.String(), stderr.String()
}

// newTask runs task new for repo with title and deps and returns its receipt, whose TaskID is the id Guildhall
// assigned: the repository's task_prefix followed by the next number, t1 for app's first and l1 for lib's.
func newTask(t *testing.T, storePath, cfg, repo, title string, deps ...string) store.Receipt {
	t.Helper()
	c, err := config.LoadUnadmitted(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := c.Repository(repo); ok && r.Workflow == "test-default" {
		approveRepo(t, storePath, cfg, repo)
	}
	args := []string{"new", "--store", storePath, "--config", cfg, "--repo", repo, "--title", title}
	for _, d := range deps {
		args = append(args, "--depends-on", d)
	}
	code, out, stderr := runTask(t, args...)
	if code != 0 {
		t.Fatalf("task new %s %q: exit %d, stderr %s", repo, title, code, stderr)
	}
	return decodeJSON[store.Receipt](t, out)
}

// approveRepo records the Guildmaster's approval of the repository's effective workflow, which task new needs
// for "lib": the human-review example with its review swapped to an Artisan weakens the shipped default.
func approveRepo(t *testing.T, storePath, cfg, repo string) {
	t.Helper()
	code, _, stderr := runWorkflow(t, "Approved for "+repo+".\n", "approve", "--store", storePath, "--config", cfg, "--repo", repo, "--words-file", "-")
	if code != 0 {
		t.Fatalf("workflow approve %s: exit %d, stderr %s", repo, code, stderr)
	}
}

func readTask(t *testing.T, path, repo, id string) store.Task {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	task, err := s.Task(repo, id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// configDigest is the contract's configuration digest: sha256 of the loaded configuration's canonical JSON.
func configDigest(t *testing.T, path string) string {
	t.Helper()
	cfg, err := config.LoadUnadmitted(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestTaskNewBindsEffectiveWorkflowAndConfig(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)

	r := newTask(t, path, cfg, "app", "Fix the login redirect")
	if r.MutationID != "task:app:t1" || r.Operation != "create-task" || r.Repository != "app" || r.TaskID != "t1" {
		t.Fatalf("receipt = %+v", r)
	}
	def := testDefault(t)
	got := readTask(t, path, "app", "t1")
	if got.Workflow != def.Revision() || got.ConfigDigest != configDigest(t, cfg) || got.State != store.TaskOpen || got.Title != "Fix the login redirect" {
		t.Fatalf("default task = %+v, want workflow %+v", got, def.Revision())
	}

	approveRepo(t, path, cfg, "lib")
	newTask(t, path, cfg, "lib", "Bump the parser")
	data, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), "workflows", "human-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	overridden, err := workflow.ApplyOverrides(file, map[string]workflow.Override{"review": {Actor: workflow.ArtisanActor}})
	if err != nil {
		t.Fatal(err)
	}
	got = readTask(t, path, "lib", "l1")
	if got.Workflow != overridden.Revision() || got.Workflow.Name != "human-review" || got.Workflow.Digest == file.Digest() {
		t.Fatalf("overridden task workflow = %+v, want %+v (file digest %s)", got.Workflow, overridden.Revision(), file.Digest())
	}
}

func TestTaskNewConfigDigestCoversManagedSettingsNotFormatting(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	base := configDigest(t, cfg)
	reformatted := writeDirector(t, "# Director alpha\n\n"+strings.ReplaceAll(directorTOML, " = ", "   =   "))
	managed := writeDirector(t, directorTOML)
	body, err := config.EncodeManaged(map[string]config.ManagedSetting{"standards_revision": {Value: "standards-2"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(filepath.Dir(managed), "managed.toml"), string(body))
	enrolManaged(t, filepath.Dir(managed))

	path := emptyStore(t)
	// The reformatted config files the same request as the base, so it is a retry of t1.
	ids := map[string]string{}
	for _, c := range []struct{ name, cfg string }{{"base", cfg}, {"reformatted", reformatted}, {"managed", managed}} {
		ids[c.name] = newTask(t, path, c.cfg, "app", "Task").TaskID
	}
	if ids["base"] != "t1" || ids["reformatted"] != "t1" || ids["managed"] != "t2" {
		t.Fatalf("ids = %v, want the reformatted config's task a retry of the base one", ids)
	}
	if got := readTask(t, path, "app", "t1").ConfigDigest; got != base {
		t.Fatalf("base digest %s, want %s", got, base)
	}
	if got := readTask(t, path, "app", "t2").ConfigDigest; got == base {
		t.Fatal("managed.toml did not change the digest")
	}
}

func TestTaskNewConfigPaths(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	t.Chdir(filepath.Dir(filepath.Dir(cfg)))
	relative := filepath.Join("director config", "config.toml")

	approveRepo(t, path, cfg, "lib")
	newTask(t, path, relative, "lib", "Relative config path")
	if got, want := readTask(t, path, "lib", "l1").ConfigDigest, configDigest(t, cfg); got != want {
		t.Fatalf("relative config digest %s, want %s", got, want)
	}
}

func TestTaskNewRefusesWorkflowAndWritesNothing(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("..", "..", "examples", "workflows", "human-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		toml     string
		file     string // replaces workflows/human-review.json; "-" removes it
		problems []string
	}{
		{name: "missing file", toml: directorTOML, file: "-", problems: []string{"human-review.json"}},
		{name: "malformed JSON", toml: directorTOML, file: "{", problems: []string{"not one JSON value"}},
		{name: "unknown field", toml: directorTOML, file: strings.Replace(string(valid), `"name"`, `"colour": "red", "name"`, 1), problems: []string{"colour"}},
		{name: "validation problems", toml: directorTOML,
			file:     strings.Replace(strings.Replace(string(valid), `"plan.spec"`, `"plan.nothing"`, 1), `"max": 3`, `"max": 99`, 1),
			problems: []string{`has no output "nothing"`, "max must be between"}},
		{name: "name mismatch", toml: directorTOML, file: strings.Replace(string(valid), `"name": "human-review"`, `"name": "other"`, 1), problems: []string{`"other"`, `"human-review"`}},
		{name: "override of a single-actor step", toml: strings.Replace(directorTOML, "overrides.review]", "overrides.build]", 1), problems: []string{"build always runs as artisan"}},
		{name: "override to a disallowed actor", toml: strings.Replace(directorTOML, `actor = "artisan"`, `actor = "engine"`, 1), problems: []string{`does not allow actor "engine"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeDirector(t, tc.toml)
			workflows := filepath.Join(filepath.Dir(cfg), "workflows")
			switch tc.file {
			case "":
			case "-":
				if err := os.Remove(filepath.Join(workflows, "human-review.json")); err != nil {
					t.Fatal(err)
				}
			default:
				writeFile(t, filepath.Join(workflows, "human-review.json"), tc.file)
			}
			path := emptyStore(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "lib", "--title", "Task")
			if code != 1 || out != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
			for _, p := range tc.problems {
				if !strings.Contains(stderr, p) {
					t.Errorf("stderr %q does not name %q", stderr, p)
				}
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("store changed (%v)", err)
			}
		})
	}
}

func TestTaskNewRefusesUnknownProfilesAndWritesNothing(t *testing.T) {
	for _, kind := range []string{"plan", "build", "review", "document-review"} {
		t.Run(kind, func(t *testing.T) {
			toml := directorTOML
			cfg := writeDirector(t, toml)
			file := filepath.Join(filepath.Dir(cfg), "workflows", "human-review.json")
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			def, err := workflow.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			stepID := kind
			if kind == "document-review" {
				stepID = "design-review"
			}
			for i := range def.Steps {
				if def.Steps[i].ID == stepID {
					if def.Steps[i].Settings == nil {
						def.Steps[i].Settings = map[string]json.RawMessage{}
					}
					def.Steps[i].Settings["profile"] = json.RawMessage(`"missing"`)
				}
			}
			data, err = json.Marshal(def)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, file, string(data))
			path := emptyStore(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "lib", "--title", "Task")
			if code != 1 || out != "" || !strings.Contains(stderr, `step "`+stepID+`" names unknown profile "missing"`) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("store changed (%v)", err)
			}
			code, out, stderr = runTask(t, "list", "--store", path, "--repo", "lib", "--json")
			if code != 0 {
				t.Fatalf("list: exit %d, stderr %q", code, stderr)
			}
			if tasks := decodeJSON[[]store.Task](t, out); len(tasks) != 0 {
				t.Fatalf("refusal recorded tasks: %+v", tasks)
			}
			writeFile(t, cfg, toml+"\n[profile.missing]\nadapter = \"claude\"\n")
			approveRepo(t, path, cfg, "lib")
			newTask(t, path, cfg, "lib", "Task")
			if got := readTask(t, path, "lib", "l1"); got.ConfigDigest != configDigest(t, cfg) {
				t.Fatalf("declared-profile task = %+v", got)
			}
		})
	}
}

func TestTaskDefaultIgnoresShadowFile(t *testing.T) {
	cfg := writeDirector(t, strings.Replace(directorTOML, `workflow = "test-default"`, `workflow = "default"`, 1))
	writeFile(t, filepath.Join(filepath.Dir(cfg), "workflows", "default.json"), "invalid JSON")
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Task")
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	if got := readTask(t, path, "app", "t1").Workflow; got != def.Revision() {
		t.Fatalf("workflow = %+v, want the shipped default %+v", got, def.Revision())
	}
}

func TestTaskArgumentOrders(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "lib")
	newTask(t, path, cfg, "lib", "Dependency")
	approveRepo(t, path, cfg, "app")
	flags := []string{"--store", path, "--config", cfg, "--repo", "app", "--title=--", "--depends-on", "lib/l1"}
	// Each order files the same request, so the later ones are retries printing the first receipt.
	var first string
	for _, rotate := range []int{0, 2, 6} {
		args := append([]string{"new"}, flags[rotate:]...)
		args = append(args, flags[:rotate]...)
		code, out, stderr := runTask(t, args...)
		if code != 0 || (first != "" && out != first) {
			t.Fatalf("rotation %d: exit %d, stdout %q, stderr %q", rotate, code, out, stderr)
		}
		first = out
	}
	for _, args := range [][]string{
		{"show", "--store", path, "--repo", "app", "t1", "--json"},
		{"show", "t1", "--store", path, "--repo", "app", "--json"},
		{"show", "--store", path, "--repo", "app", "--json", "t1"},
		{"show", "--store", path, "--repo", "app", "--json", "--", "t1"},
	} {
		code, out, stderr := runTask(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr %s", args, code, stderr)
		}
		detail := decodeJSON[taskDetail](t, out)
		if detail.Task.ID != "t1" || detail.Task.Title != "--" || len(detail.Task.DependsOn) != 1 {
			t.Fatalf("detail = %+v", detail)
		}
	}
}

// TestTaskNewMaximalIDsRetry: a maximal repository id with a three-letter prefix keeps the task mutation unhashed,
// "task:<repo>:<id>", and a retry prints the same receipt.
func TestTaskNewMaximalIDsRetry(t *testing.T) {
	repo := strings.Repeat("r", 63)
	cfg := writeDirector(t, strings.Replace(strings.Replace(directorTOML, `id = "app"`, `id = "`+repo+`"`, 1), `task_prefix = "t"`, `task_prefix = "zzz"`, 1))
	path := emptyStore(t)
	first := newTask(t, path, cfg, repo, "Task")
	if first.MutationID != "task:"+repo+":zzz1" || first.TaskID != "zzz1" || len(first.MutationID) > 128 {
		t.Fatalf("receipt = %+v", first)
	}
	if again := newTask(t, path, cfg, repo, "Task"); again != first {
		t.Fatalf("retry = %+v, want %+v", again, first)
	}
	if got := readTask(t, path, repo, "zzz1").Title; got != "Task" {
		t.Fatalf("title = %q", got)
	}
}

// TestTaskNewRetry: rerunning an identical task new prints the same receipt and writes nothing, each run opening the
// store afresh; a different request takes the next number, and an identical request whose task is cancelled, or
// done, files a new task.
func TestTaskNewRetry(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	first := newTask(t, path, cfg, "app", "Fix the login redirect")
	if first.TaskID != "t1" {
		t.Fatalf("first task = %+v, want t1", first)
	}
	before := lastSequence(t, path)
	if again := newTask(t, path, cfg, "app", "Fix the login redirect"); again != first {
		t.Fatalf("retry receipt %+v, want %+v", again, first)
	}
	if after := lastSequence(t, path); after != before {
		t.Fatalf("a retry wrote: sequence %d, was %d", after, before)
	}

	if r := newTask(t, path, cfg, "app", "Another title"); r.TaskID != "t2" {
		t.Fatalf("a different title = %+v, want t2", r)
	}
	if r := newTask(t, path, cfg, "app", "Fix the login redirect", "app/t2"); r.TaskID != "t3" {
		t.Fatalf("different dependencies = %+v, want t3", r)
	}
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTask("cancel-t1", "app", "t1", "filed again"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r := newTask(t, path, cfg, "app", "Fix the login redirect"); r.TaskID != "t4" || r.MutationID != "task:app:t4" {
		t.Fatalf("an identical request after a cancel = %+v, want t4", r)
	}
	if got := readTask(t, path, "app", "t1"); got.Title != "Fix the login redirect" || got.State != store.TaskCancelled {
		t.Fatalf("t1 = %+v", got)
	}
}

// TestTaskNewAssignsIDs: task new assigns the repository's prefix followed by one more than the highest number of
// any task id of that form, compared as numbers, ignoring other ids, so a cancelled or pruned highest task still
// advances it; each repository numbers under its own prefix.
func TestTaskNewAssignsIDs(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "lib")
	if r := newTask(t, path, cfg, "lib", "First"); r.TaskID != "l1" {
		t.Fatalf("lib's first task = %+v, want l1", r)
	}
	if r := newTask(t, path, cfg, "app", "First"); r.TaskID != "t1" {
		t.Fatalf("app's first task = %+v, want t1", r)
	}
	d := readTask(t, path, "app", "t1")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t9", "t10", "t-1", "t007", "t0", "tx12", "x99"} {
		task := store.Task{Repository: "app", ID: id, Title: "Seeded " + id, Workflow: d.Workflow, ConfigDigest: d.ConfigDigest}
		if _, err := s.CreateTask("seed-"+id, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CancelTask("cancel-t10", "app", "t10", "seeded"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r := newTask(t, path, cfg, "app", "After t10"); r.TaskID != "t11" {
		t.Fatalf("after a cancelled t10 = %+v, want t11", r)
	}
	if r := newTask(t, path, cfg, "lib", "Second"); r.TaskID != "l2" {
		t.Fatalf("lib's second task = %+v, want l2", r)
	}

	// A pruned highest task still counts: its records stay.
	s, err = store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskState("done-t11", "app", "t11", store.TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneTask("prune-t11", store.TaskPrune{Repository: "app", TaskID: "t11", Worktree: store.WorktreeRemoved, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r := newTask(t, path, cfg, "app", "After a pruned t11"); r.TaskID != "t12" {
		t.Fatalf("after a pruned t11 = %+v, want t12", r)
	}
}

// TestTaskNewRefusesAnIDAndAMissingPrefix: a task id passed to task new is a usage error saying Guildhall assigns
// ids, and a repository without task_prefix is refused naming the key and the repository; neither writes anything.
func TestTaskNewRefusesAnIDAndAMissingPrefix(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "app")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "t1")
	if code != 2 || out != "" || !strings.Contains(stderr, `task new takes no task id, but was given "t1"; Guildhall assigns ids`) {
		t.Fatalf("a passed id: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	noPrefix := writeDirector(t, strings.Replace(directorTOML, "task_prefix = \"t\"\n", "", 1))
	code, out, stderr = runTask(t, "new", "--store", path, "--config", noPrefix, "--repo", "app", "--title", "T")
	if code != 1 || out != "" || !strings.Contains(stderr, "repository app has no task_prefix") {
		t.Fatalf("no prefix: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("a refusal changed the store (%v)", err)
	}
}

// TestTaskNewTitleLimit: a title over 100 characters is refused with exit 1, naming its length, the limit and
// --detail, and creates nothing; 100 multibyte characters are accepted.
func TestTaskNewTitleLimit(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "app")
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", strings.Repeat("é", 101))
	want := "guildhall: task new: title is 101 characters; the limit is 100; put the full statement in --detail or --detail-file\n"
	if code != 1 || out != "" || stderr != want {
		t.Fatalf("long title: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if code, _, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t1"); code != 1 {
		t.Fatalf("refused task was written: show exit %d", code)
	}
	title := strings.Repeat("é", 100)
	newTask(t, path, cfg, "app", title)
	if got := readTask(t, path, "app", "t1").Title; got != title {
		t.Fatalf("title = %q", got)
	}
}

// TestTaskNewDetail: --detail and --detail-file, from a file or standard input, store the task's detail, which task
// show prints; giving both is a usage error.
func TestTaskNewDetail(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "app")
	file := filepath.Join(t.TempDir(), "detail.txt")
	writeFile(t, file, "\xef\xbb\xbfFrom a file:\r\n  error: Y\r\n")
	cases := []struct {
		id, stdin string
		args      []string
		want      string
	}{
		{"t1", "", []string{"--detail", "Running X fails: error: Y"}, "Running X fails: error: Y"},
		{"t2", "", []string{"--detail-file", file}, "From a file:\n  error: Y\n"},
		{"t3", "From stdin:\r\nerror: Z\n", []string{"--detail-file", "-"}, "From stdin:\nerror: Z\n"},
	}
	for _, c := range cases {
		app, stdout, stderr := newTestApp(nil, nil)
		app.Stdin = strings.NewReader(c.stdin)
		args := append([]string{"task", "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix X", "--kind", "bug"}, c.args...)
		if code := app.Run(args); code != 0 {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q", c.id, code, stdout, stderr)
		}
		code, out, errOut := runTask(t, "show", "--store", path, "--repo", "app", "--json", c.id)
		if code != 0 {
			t.Fatalf("show %s: exit %d, stderr %s", c.id, code, errOut)
		}
		var shown struct {
			Task struct {
				Detail string `json:"detail"`
			} `json:"task"`
		}
		if err := json.Unmarshal([]byte(out), &shown); err != nil {
			t.Fatal(err)
		}
		if shown.Task.Detail != c.want {
			t.Errorf("%s: detail = %q, want %q", c.id, shown.Task.Detail, c.want)
		}
	}
	if code, out, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t1"); code != 0 || !strings.Contains(out, `"detail"`) {
		t.Fatalf("show without detail field: exit %d, %s", code, out)
	}

	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix X",
		"--detail", "text", "--detail-file", file)
	if code != 2 || out != "" || !strings.Contains(stderr, "give --detail or --detail-file, not both") {
		t.Fatalf("both flags: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	newTask(t, path, cfg, "app", "No detail")
	if code, out, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t4"); code != 0 || strings.Contains(out, `"detail"`) {
		t.Fatalf("task without a detail: exit %d, %s", code, out)
	}
}

func TestTaskNewDependencies(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "lib")
	newTask(t, path, cfg, "lib", "Parser")
	newTask(t, path, cfg, "app", "Schema")

	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Login",
		"--depends-on", "lib/l1", "--depends-on", "app/t9")
	if code != 1 || out != "" || !strings.Contains(stderr, "app/t9") || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown dependency: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if code, _, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t2"); code != 1 {
		t.Fatalf("refused task was written: show exit %d", code)
	}

	newTask(t, path, cfg, "app", "Login", "lib/l1", "app/t1")
	want := []store.TaskRef{{Repository: "lib", ID: "l1"}, {Repository: "app", ID: "t1"}}
	got := readTask(t, path, "app", "t2").DependsOn
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("depends_on = %+v, want %+v", got, want)
	}
}

// TestTaskNewFixes: --fixes names the earlier task whose defect the new one fixes, which must exist, and is kept on
// the task; --kind feature keeps it on the feature workflow.
func TestTaskNewFixes(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Schema")
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix", "--kind", "feature", "--fixes", "app/t9")
	if code != 1 || out != "" || !strings.Contains(stderr, "app/t9") || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("an unknown fixed task: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix", "--kind", "feature", "--fixes", "app/t1"); code != 0 {
		t.Fatalf("task new --fixes: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if got := readTask(t, path, "app", "t2").Fixes; got == nil || *got != (store.TaskRef{Repository: "app", ID: "t1"}) {
		t.Fatalf("fixes = %+v, want app/t1", got)
	}
}

func TestTaskUsageErrors(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	cases := map[string][]string{
		"trailing unknown":       {"show", "t1", "--store", path, "--repo", "app", "--json", "--unknown"},
		"trailing disallowed":    {"show", "t1", "--store", path, "--repo", "app", "--json", "--title", "T"},
		"flags after terminator": {"show", "--store", path, "--repo", "app", "--", "t1", "--json"},
		"no subcommand":          {},
		"unknown subcommand":     {"frobnicate"},
		"new without store":      {"new", "--config", cfg, "--repo", "app", "--title", "T"},
		"new without config":     {"new", "--store", path, "--repo", "app", "--title", "T"},
		"new without repo":       {"new", "--store", path, "--config", cfg, "--title", "T"},
		"new without title":      {"new", "--store", path, "--config", cfg, "--repo", "app"},
		"new with a task id":     {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "t1"},
		"bad dependency":         {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--depends-on", "t1"},
		"bad fixed task":         {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--fixes", "t1"},
		"fixes on show":          {"show", "--store", path, "--repo", "app", "--fixes", "app/t1", "--json", "t1"},
		"show without json":      {"show", "--store", path, "--repo", "app", "t1"},
		"show without repo":      {"show", "--store", path, "--json", "t1"},
		"show with title":        {"show", "--store", path, "--repo", "app", "--title", "T", "--json", "t1"},
		"list without json":      {"list", "--store", path},
		"list with task":         {"list", "--store", path, "--json", "t1"},
		"list with config":       {"list", "--store", path, "--config", cfg, "--json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, args...)
			if code != 2 || out != "" || !strings.Contains(stderr, "guildhall task new") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
		})
	}
}

func TestTaskRefusesMissingAndEmptyStore(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	for _, sub := range []string{"new", "show", "list"} {
		t.Run(sub, func(t *testing.T) {
			dir := t.TempDir()
			missing := filepath.Join(dir, "missing.store")
			empty := filepath.Join(dir, "empty.store")
			writeFile(t, empty, "")
			for path, want := range map[string]string{missing: "no store at", empty: "not a Guildhall store"} {
				args := map[string][]string{
					"new":  {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T"},
					"show": {"show", "--store", path, "--repo", "app", "--json", "t1"},
					"list": {"list", "--store", path, "--json"},
				}[sub]
				code, out, stderr := runTask(t, args...)
				if code != 1 || out != "" || !strings.Contains(stderr, want) {
					t.Fatalf("%s: exit %d, stdout %q, stderr %q", path, code, out, stderr)
				}
			}
			if _, err := os.Stat(missing); !os.IsNotExist(err) {
				t.Fatalf("missing store was created: %v", err)
			}
			if body, err := os.ReadFile(empty); err != nil || len(body) != 0 {
				t.Fatalf("empty file changed: %d bytes, %v", len(body), err)
			}
		})
	}
}

func TestTaskNewRefusesUnknownRepository(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "web", "--title", "T")
	if code != 1 || out != "" || !strings.Contains(stderr, `"web"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

// goldenStore builds a store through task new, then adds an attempt, a document and decisions directly.
func goldenStore(t *testing.T) string {
	t.Helper()
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "lib")
	newTask(t, path, cfg, "lib", "Bump the parser")
	newTask(t, path, cfg, "app", "Add a sign-out link")
	newTask(t, path, cfg, "app", "Fix the login redirect", "lib/l1")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	steps := []func() (store.Receipt, error){
		func() (store.Receipt, error) { return s.StartAttempt("g-1", "app", "t2", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("g-2", "app", "t2", "plan", 1, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) {
			return s.PutDocument("g-3", store.DocumentRef{Repository: "app", TaskID: "t2", Name: "spec", Path: "tasks/t2/spec.md", SHA256: strings.Repeat("b", 64)})
		},
		func() (store.Receipt, error) {
			return s.OpenDecision("g-4", store.Decision{Repository: "app", TaskID: "t2", ID: "accept-plan", Kind: store.DecisionQuestion, Question: "Accept the plan?"})
		},
		func() (store.Receipt, error) {
			return s.AnswerDecision("g-5", "app", "t2", "accept-plan", "Yes, but keep the old route.")
		},
		func() (store.Receipt, error) { return s.SetTaskPriority("g-6", "app", "t2", store.PriorityHighest) },
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if os.Getenv("GUILDHALL_UPDATE_GOLDEN") != "" {
		writeFile(t, golden, got)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch:\n got: %s\nwant: %s", name, got, want)
	}
}

func TestTaskShowGolden(t *testing.T) {
	path := goldenStore(t)
	code, out, stderr := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	checkGolden(t, "task-show.json", out)

	code, out, _ = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t1")
	if code != 0 || !strings.Contains(out, `"attempts":[],"documents":[],"decisions":[]`) {
		t.Fatalf("task without records: exit %d, %s", code, out)
	}
	code, out, stderr = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t9")
	if code != 1 || out != "" || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown task: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

func TestTaskListGolden(t *testing.T) {
	path := goldenStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "list", "--store", path, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	checkGolden(t, "task-list.json", out)

	code, out, _ = runTask(t, "list", "--store", path, "--repo", "lib", "--json")
	if tasks := decodeJSON[[]store.Task](t, out); code != 0 || len(tasks) != 1 || tasks[0].Repository != "lib" {
		t.Fatalf("list lib: exit %d, %s", code, out)
	}
	if code, out, _ = runTask(t, "list", "--store", path, "--repo", "web", "--json"); code != 0 || out != "[]\n" {
		t.Fatalf("list web: exit %d, %q", code, out)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reads changed the store (%v)", err)
	}
}

// smallWorkflow is the configuration page's example: plan, acceptance, build, review, publish and deliver, with no validation.
const smallWorkflow = `{
  "schema": "guildhall.workflow/1",
  "name": "small",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "demo"}},
    {"id": "accept", "type": "guildmaster-approval", "version": 1, "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "settings": {"profile": "demo"}, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "demo"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "publish", "type": "publish", "version": 1, "inputs": {"head": ["build.head"]}},
    {"id": "deliver", "type": "deliver", "version": 1, "inputs": {"head": ["publish.head"], "review": ["review.outcome"]}}
  ]
}`

// refuseTask runs task new for repo, which must be refused with exit 1 and leave the store's bytes unchanged,
// and returns its standard error.
func refuseTask(t *testing.T, path, cfg, repo string) string {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", repo, "--title", "Task")
	if code != 1 || out != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("store changed (%v)", err)
	}
	return stderr
}

func TestTaskNewEnforcesWorkflowAuthority(t *testing.T) {
	t.Run("a weakening override needs the effective revision approved", func(t *testing.T) {
		cfg := writeDirector(t, directorTOML)
		path := emptyStore(t)
		file := filepath.Join(filepath.Dir(cfg), "workflows", "human-review.json")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		def, err := workflow.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		effective, err := workflow.ApplyOverrides(def, map[string]workflow.Override{"review": {Actor: workflow.ArtisanActor}})
		if err != nil {
			t.Fatal(err)
		}
		stderr := refuseTask(t, path, cfg, "lib")
		for _, want := range []string{"not authorized", effective.Digest(), "step review: instructions", "guildhall workflow approve"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr %q does not name %q", stderr, want)
			}
		}
		// Approving the file is not approving the repository's override of it.
		if code, _, stderr := runWorkflow(t, "Approved.\n", "approve", "--store", path, "--file", file, "--words-file", "-"); code != 0 {
			t.Fatalf("approve file: exit %d, stderr %s", code, stderr)
		}
		if stderr := refuseTask(t, path, cfg, "lib"); !strings.Contains(stderr, "step review: actor guildmaster -> artisan") {
			t.Errorf("stderr %q does not name the weaker actor", stderr)
		}
		approveRepo(t, path, cfg, "lib")
		newTask(t, path, cfg, "lib", "Task")
		if got := readTask(t, path, "lib", "l1").Workflow; got != effective.Revision() {
			t.Fatalf("workflow = %+v, want %+v", got, effective.Revision())
		}
	})

	t.Run("below the floor needs the exact revision approved", func(t *testing.T) {
		cfg := writeDirector(t, strings.Replace(directorTOML, `workflow = "human-review"`, `workflow = "small"`, 1))
		writeFile(t, filepath.Join(filepath.Dir(cfg), "workflows", "small.json"), smallWorkflow)
		path := emptyStore(t)
		if stderr := refuseTask(t, path, cfg, "lib"); !strings.Contains(stderr, `deliver step "deliver" takes no validate result`) {
			t.Errorf("stderr %q does not name the floor", stderr)
		}
		approveRepo(t, path, cfg, "lib")
		newTask(t, path, cfg, "lib", "Task")
	})

	t.Run("a non-weakening change needs no approval", func(t *testing.T) {
		def := testDefault(t)
		def.Name = "careful"
		for i := range def.Steps {
			if def.Steps[i].ID == "review" {
				def.Steps[i].Settings = map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}
			}
		}
		data, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		toml := strings.NewReplacer(`workflow = "human-review"`, `workflow = "careful"`, `workflow = "test-default"`, `workflow = "careful"`).Replace(directorTOML)
		toml = strings.Replace(toml, "[repository.workflow_overrides.review]\nactor = \"artisan\"\n", "", 1)
		toml += "\n[repository.workflow_overrides.review]\nactor = \"guildmaster\"\n"
		cfg := writeDirector(t, toml)
		writeFile(t, filepath.Join(filepath.Dir(cfg), "workflows", "careful.json"), string(data))
		path := emptyStore(t)
		newTask(t, path, cfg, "lib", "Task")
		newTask(t, path, cfg, "app", "Task")
		if got := storeApprovals(t, path); len(got) != 0 {
			t.Fatalf("approvals = %+v", got)
		}
	})
}
