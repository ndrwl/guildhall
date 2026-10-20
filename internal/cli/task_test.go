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

// directorTOML has repository "app" on the shipped default workflow and "lib" on the human-review example
// with its review swapped back to automated review. Paths are fixed so digests and goldens are stable.
const directorTOML = `schema_version = 1
mode = "director"
name = "alpha"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"

[[repository]]
id = "lib"
path = "/srv/lib"
remote = "git@github.com:example/lib.git"
owner_machine = "workstation"
workflow = "human-review"

[repository.workflow_overrides.review]
type = "automated-review"
version = 1
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
	writeFile(t, filepath.Join(dir, "workflows", "human-review.json"), string(example))
	writeFile(t, filepath.Join(dir, "config.toml"), toml)
	return filepath.Join(dir, "config.toml")
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

func newTask(t *testing.T, storePath, cfg, repo, title, id string, deps ...string) store.Receipt {
	t.Helper()
	args := []string{"new", "--store", storePath, "--config", cfg, "--repo", repo, "--title", title}
	for _, d := range deps {
		args = append(args, "--depends-on", d)
	}
	code, out, stderr := runTask(t, append(args, id)...)
	if code != 0 {
		t.Fatalf("task new %s/%s: exit %d, stderr %s", repo, id, code, stderr)
	}
	return decodeJSON[store.Receipt](t, out)
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
	cfg, err := config.Load(path)
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

	r := newTask(t, path, cfg, "app", "Fix the login redirect", "t-1")
	if r.MutationID != "task:app:t-1" || r.Operation != "create-task" || r.Repository != "app" || r.TaskID != "t-1" {
		t.Fatalf("receipt = %+v", r)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	got := readTask(t, path, "app", "t-1")
	if got.Workflow != def.Revision() || got.ConfigDigest != configDigest(t, cfg) || got.State != store.TaskOpen || got.Title != "Fix the login redirect" {
		t.Fatalf("default task = %+v, want workflow %+v", got, def.Revision())
	}

	newTask(t, path, cfg, "lib", "Bump the parser", "t-1")
	data, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), "workflows", "human-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	overridden, err := workflow.ApplyOverrides(file, map[string]workflow.Override{"review": {Type: "automated-review", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	got = readTask(t, path, "lib", "t-1")
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

	path := emptyStore(t)
	for id, c := range map[string]string{"t-base": cfg, "t-reformatted": reformatted, "t-managed": managed} {
		newTask(t, path, c, "app", "Task", id)
	}
	if got := readTask(t, path, "app", "t-base").ConfigDigest; got != base {
		t.Fatalf("base digest %s, want %s", got, base)
	}
	if got := readTask(t, path, "app", "t-reformatted").ConfigDigest; got != base {
		t.Fatalf("reformatting changed the digest: %s, want %s", got, base)
	}
	if got := readTask(t, path, "app", "t-managed").ConfigDigest; got == base {
		t.Fatal("managed.toml did not change the digest")
	}
}

func TestTaskNewConfigPaths(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	t.Chdir(filepath.Dir(filepath.Dir(cfg)))
	relative := filepath.Join("director config", "config.toml")

	newTask(t, path, relative, "lib", "Relative config path", "t-1")
	if got, want := readTask(t, path, "lib", "t-1").ConfigDigest, configDigest(t, cfg); got != want {
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
		extra    map[string]string
		problems []string
	}{
		{name: "missing file", toml: directorTOML, file: "-", problems: []string{"human-review.json"}},
		{name: "malformed JSON", toml: directorTOML, file: "{", problems: []string{"malformed JSON"}},
		{name: "unknown field", toml: directorTOML, file: strings.Replace(string(valid), `"name"`, `"colour": "red", "name"`, 1), problems: []string{"colour"}},
		{name: "validation problems", toml: directorTOML,
			file:     strings.Replace(strings.Replace(string(valid), `"plan.spec"`, `"plan.nothing"`, 1), `"max": 3`, `"max": 99`, 1),
			problems: []string{`has no output "nothing"`, "max must be between"}},
		{name: "name mismatch", toml: directorTOML, file: strings.Replace(string(valid), `"name": "human-review"`, `"name": "other"`, 1), problems: []string{`"other"`, `"human-review"`}},
		{name: "override of a non-review step", toml: strings.Replace(directorTOML, "overrides.review]", "overrides.build]", 1), problems: []string{"only review steps"}},
		{name: "override to an unknown version", toml: strings.Replace(directorTOML, "review\"\nversion = 1", "review\"\nversion = 2", 1), problems: []string{"version 2"}},
		{name: "shadowed default", toml: strings.Replace(directorTOML, `workflow = "human-review"`, `workflow = "default"`, 1),
			extra: map[string]string{"default.json": string(valid)}, problems: []string{"default.json"}},
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
			for name, body := range tc.extra {
				writeFile(t, filepath.Join(workflows, name), body)
			}
			path := emptyStore(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "lib", "--title", "Task", "t-1")
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

func TestTaskNewRetry(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	first := newTask(t, path, cfg, "app", "Fix the login redirect", "t-1")
	if again := newTask(t, path, cfg, "app", "Fix the login redirect", "t-1"); again != first {
		t.Fatalf("retry receipt %+v, want %+v", again, first)
	}

	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Another title", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, store.ErrMutationConflict.Error()) {
		t.Fatalf("conflicting retry: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if got := readTask(t, path, "app", "t-1").Title; got != "Fix the login redirect" {
		t.Fatalf("title changed to %q", got)
	}
}

func TestTaskNewDependencies(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "lib", "Parser", "t-1")
	newTask(t, path, cfg, "app", "Schema", "t-2")

	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Login",
		"--depends-on", "lib/t-1", "--depends-on", "app/t-9", "t-3")
	if code != 1 || out != "" || !strings.Contains(stderr, "app/t-9") || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown dependency: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if code, _, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-3"); code != 1 {
		t.Fatalf("refused task was written: show exit %d", code)
	}

	newTask(t, path, cfg, "app", "Login", "t-3", "lib/t-1", "app/t-2")
	want := []store.TaskRef{{Repository: "lib", ID: "t-1"}, {Repository: "app", ID: "t-2"}}
	got := readTask(t, path, "app", "t-3").DependsOn
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("depends_on = %+v, want %+v", got, want)
	}
}

func TestTaskUsageErrors(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	cases := map[string][]string{
		"no subcommand":      {},
		"unknown subcommand": {"start"},
		"new without store":  {"new", "--config", cfg, "--repo", "app", "--title", "T", "t-1"},
		"new without config": {"new", "--store", path, "--repo", "app", "--title", "T", "t-1"},
		"new without repo":   {"new", "--store", path, "--config", cfg, "--title", "T", "t-1"},
		"new without title":  {"new", "--store", path, "--config", cfg, "--repo", "app", "t-1"},
		"new without task":   {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T"},
		"bad dependency":     {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--depends-on", "t-1", "t-2"},
		"show without json":  {"show", "--store", path, "--repo", "app", "t-1"},
		"show without repo":  {"show", "--store", path, "--json", "t-1"},
		"show with title":    {"show", "--store", path, "--repo", "app", "--title", "T", "--json", "t-1"},
		"list without json":  {"list", "--store", path},
		"list with task":     {"list", "--store", path, "--json", "t-1"},
		"list with config":   {"list", "--store", path, "--config", cfg, "--json"},
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
					"new":  {"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "t-1"},
					"show": {"show", "--store", path, "--repo", "app", "--json", "t-1"},
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
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "web", "--title", "T", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, `"web"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

// goldenStore builds a store through task new, then adds an attempt, a document and decisions directly.
func goldenStore(t *testing.T) string {
	t.Helper()
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "lib", "Bump the parser", "t-1")
	newTask(t, path, cfg, "app", "Fix the login redirect", "t-2", "lib/t-1")
	newTask(t, path, cfg, "app", "Add a sign-out link", "t-1")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	steps := []func() (store.Receipt, error){
		func() (store.Receipt, error) { return s.StartAttempt("g-1", "app", "t-2", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("g-2", "app", "t-2", "plan", 1, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) {
			return s.PutDocument("g-3", store.DocumentRef{Repository: "app", TaskID: "t-2", Name: "spec", Path: "tasks/t-2/spec.md", SHA256: strings.Repeat("b", 64)})
		},
		func() (store.Receipt, error) {
			return s.OpenDecision("g-4", "app", "t-2", "accept-plan", "Accept the plan?")
		},
		func() (store.Receipt, error) {
			return s.AnswerDecision("g-5", "app", "t-2", "accept-plan", "Yes, but keep the old route.")
		},
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
	code, out, stderr := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	checkGolden(t, "task-show.json", out)

	code, out, _ = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-1")
	if code != 0 || !strings.Contains(out, `"attempts":[],"documents":[],"decisions":[]`) {
		t.Fatalf("task without records: exit %d, %s", code, out)
	}
	code, out, stderr = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-9")
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
