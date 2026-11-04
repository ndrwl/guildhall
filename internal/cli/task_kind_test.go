package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// kindTask runs task new in repository app with extra flags and returns its exit code, standard error and the id it
// assigned, empty when it was refused.
func kindTask(t *testing.T, path, cfg string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Task"}, extra...)
	code, out, stderr := runTask(t, args...)
	var id string
	if code == 0 {
		id = decodeJSON[store.Receipt](t, out).TaskID
	}
	return code, stderr, id
}

// approveKind records the Guildmaster's approval of repository app's effective workflow for taskKind.
func approveKind(t *testing.T, path, cfg, taskKind string) {
	t.Helper()
	code, _, stderr := runWorkflow(t, "Approved.\n", "approve", "--store", path, "--config", cfg, "--repo", "app", "--kind", taskKind, "--words-file", "-")
	if code != 0 {
		t.Fatalf("workflow approve --kind %s: exit %d, stderr %s", taskKind, code, stderr)
	}
}

// kindRevision is repository app's effective workflow revision for taskKind under cfg.
func kindRevision(t *testing.T, cfg, taskKind string) workflow.Revision {
	t.Helper()
	def, err := effectiveWorkflow(filepath.Join(filepath.Dir(cfg), "workflows"), mustLoad(t, cfg), mustRepository(t, cfg, "app"), taskKind)
	if err != nil {
		t.Fatal(err)
	}
	return def.Revision()
}

// TestTaskNewKinds: with no kind a task binds the repository's workflow; --kind bug, or --fixes alone, binds the
// shipped bug workflow exactly as shipped, with no approval; --kind feature --fixes binds the feature workflow and
// keeps the fixed task; and the same title with another kind is another request, so it files a new task.
func TestTaskNewKinds(t *testing.T) {
	cfg := writeDirector(t, rolesTOML)
	path := emptyStore(t)
	feature, bug := kindRevision(t, cfg, config.TaskKindFeature), kindRevision(t, cfg, config.TaskKindBug)
	shippedBug, err := workflow.Bug()
	if err != nil {
		t.Fatal(err)
	}
	if feature.Name != "default" || bug != shippedBug.Revision() {
		t.Fatalf("feature %+v, bug %+v", feature, bug)
	}
	newTask(t, path, cfg, "app", "Task")
	if got := readTask(t, path, "app", "t1").Workflow; got != feature {
		t.Fatalf("no kind: workflow %+v, want %+v", got, feature)
	}

	for _, extra := range [][]string{{"--kind", "bug"}, {"--fixes", "app/t1"}} {
		code, stderr, id := kindTask(t, path, cfg, extra...)
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr %s", extra, code, stderr)
		}
		if got := readTask(t, path, "app", id).Workflow; got != bug {
			t.Fatalf("%s %v: workflow %+v, want %+v", id, extra, got, bug)
		}
	}
	if code, stderr, id := kindTask(t, path, cfg, "--kind", "feature", "--fixes", "app/t1"); code != 0 || id != "t4" {
		t.Fatalf("--kind feature --fixes: exit %d, id %s, stderr %s", code, id, stderr)
	}
	if got := readTask(t, path, "app", "t4"); got.Workflow != feature || got.Fixes == nil || *got.Fixes != (store.TaskRef{Repository: "app", ID: "t1"}) {
		t.Fatalf("--kind feature --fixes: task %+v", got)
	}
	// t2 is a bug titled Task; the same title as a feature is t1's request, so it is a retry of t1, and as a bug
	// again it is a retry of t2.
	if code, stderr, id := kindTask(t, path, cfg, "--kind", "feature"); code != 0 || id != "t1" {
		t.Fatalf("the feature again: exit %d, id %s, stderr %s", code, id, stderr)
	}
	if code, stderr, id := kindTask(t, path, cfg, "--kind", "bug"); code != 0 || id != "t2" {
		t.Fatalf("the bug again: exit %d, id %s, stderr %s", code, id, stderr)
	}
}

// TestTaskNewKindOverride: [repository.kinds] bug names the repository's own bug workflow, which a bug task binds
// once the Guildmaster approves it, here because it writes a profile into the review; a feature task still binds the
// workflow setting.
func TestTaskNewKindOverride(t *testing.T) {
	cfg := writeDirector(t, rolesTOML+"\n[repository.kinds]\nbug = \"my-bug\"\n")
	def, err := workflow.Bug()
	if err != nil {
		t.Fatal(err)
	}
	def.Name, def.Description = "my-bug", "Our bug workflow."
	for i := range def.Steps {
		if def.Steps[i].ID == "review" {
			def.Steps[i].Settings["profile"] = json.RawMessage(`"reviewer"`)
		}
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(filepath.Dir(cfg), "workflows", "my-bug.json"), string(data))
	path := emptyStore(t)
	want := kindRevision(t, cfg, config.TaskKindBug)
	if want.Name != "my-bug" {
		t.Fatalf("bug resolves to %+v", want)
	}
	if code, stderr, _ := kindTask(t, path, cfg, "--kind", "bug"); code != 1 || !strings.Contains(stderr, "not authorized") {
		t.Fatalf("unapproved my-bug: exit %d, stderr %s", code, stderr)
	}
	approveKind(t, path, cfg, config.TaskKindBug)
	if code, stderr, _ := kindTask(t, path, cfg, "--kind", "bug"); code != 0 {
		t.Fatalf("my-bug: exit %d, stderr %s", code, stderr)
	}
	if got := readTask(t, path, "app", "t1").Workflow; got != want {
		t.Fatalf("workflow %+v, want %+v", got, want)
	}
	if got := kindRevision(t, cfg, config.TaskKindFeature); got.Name != "default" {
		t.Fatalf("feature resolves to %+v", got)
	}
}

// TestTaskNewKindRefusals: an unknown --kind is a usage error, and a config naming the feature kind or an unknown
// kind in its kinds table is refused; none writes anything.
func TestTaskNewKindRefusals(t *testing.T) {
	cfg := writeDirector(t, rolesTOML)
	path := emptyStore(t)
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--kind", "docs")
	if code != 2 || out != "" || !strings.Contains(stderr, `--kind "docs" is not a task kind; use feature, bug or hallmark`) {
		t.Fatalf("unknown --kind: exit %d, stdout %q, stderr %s", code, out, stderr)
	}
	for table, want := range map[string]string{
		"feature = \"default\"": "kinds.feature is not accepted",
		"docs = \"default\"":    `unknown task kind "docs" in kinds`,
	} {
		cfg := writeDirector(t, rolesTOML+"\n[repository.kinds]\n"+table+"\n")
		if stderr := refuseTask(t, path, cfg, "app"); !strings.Contains(stderr, want) {
			t.Errorf("%s: stderr %q does not say %q", table, stderr, want)
		}
	}
	if code, _, stderr := runWorkflow(t, "", "approve", "--store", path, "--file", "x.json", "--kind", "bug", "--words-file", "-"); code != 2 || !strings.Contains(stderr, "--kind applies only to workflow approve with --config and --repo") {
		t.Errorf("approve --file --kind: exit %d, stderr %s", code, stderr)
	}
}

// TestDirectorTasksSkillCopiesKindDescriptions: the Director's tasks skill lists each kind with the description
// task new --help gives.
func TestDirectorTasksSkillCopiesKindDescriptions(t *testing.T) {
	skill := mustReadFile(t, filepath.Join("..", "..", "skills", "guildhall-director-tasks", "SKILL.md"))
	for _, k := range config.TaskKinds {
		if want := "- `" + k + "`: " + taskKindDescriptions[k] + "\n"; !strings.Contains(skill, want) {
			t.Errorf("SKILL.md does not list %q", want)
		}
	}
}

// TestTaskNewHelpDescribesKinds: task new --help prints the usage, with --kind and a paragraph for each kind.
func TestTaskNewHelpDescribesKinds(t *testing.T) {
	code, out, stderr := runTask(t, "new", "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	for _, want := range []string{"[--kind feature|bug|hallmark]", "[--detail <text> | --detail-file <file|->]", "at most 100 characters", "--fixes without --kind means bug", "  feature  " + taskKindDescriptions[config.TaskKindFeature], "  bug      " + taskKindDescriptions[config.TaskKindBug], "  hallmark " + taskKindDescriptions[config.TaskKindHallmark]} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not contain %q:\n%s", want, out)
		}
	}
}
