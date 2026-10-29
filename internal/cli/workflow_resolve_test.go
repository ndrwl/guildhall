package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// rolesTOML has repository "app" on the shipped default with the four role profiles declared and no custom workflow.
const rolesTOML = `schema_version = 1
mode = "director"
name = "alpha"

[local]

[profile.researcher]
adapter = "fake"

[profile.planner]
adapter = "fake"

[profile.builder]
adapter = "fake"

[profile.reviewer]
adapter = "fake"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"
`

// profiledDefault is the shipped default with explicit role profiles written into its file, the shape the removed
// examples/workflows/profiled.json and a copy's dogfood workflow have, named and described as the shipped default.
func profiledDefault(t *testing.T) *workflow.Definition {
	t.Helper()
	def, err := workflow.Parse([]byte(`{
  "schema": "guildhall.workflow/1",
  "name": "default",
  "description": "Grill and research, plan, Guildmaster acceptance, build, independent automated review with bounded fix rounds, publish, validation, delivery under the repository's merge policy.",
  "steps": [
    {"id": "grill", "type": "grill", "version": 1, "settings": {"actor": "director", "skills": ["guildhall-grill", "guildhall-domain-model"]}},
    {"id": "research", "type": "research", "version": 1, "settings": {"profile": "researcher"}},
    {"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "planner"},
     "inputs": {"context": ["grill.notes", "research.notes"]}},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": ["plan.spec"]},
     "on": {"changes_requested": {"goto": "plan", "max": 3}}},
    {"id": "build", "type": "build", "version": 1, "settings": {"profile": "builder"},
     "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "reviewer"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "publish", "type": "publish", "version": 1,
     "inputs": {"head": ["build.head"], "review": ["review.outcome"]}},
    {"id": "validate", "type": "validate", "version": 1,
     "inputs": {"head": ["publish.head"]},
     "on": {"failed": {"goto": "build", "max": 3}}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["publish.head"], "review": ["review.outcome"], "validation": ["validate.result"]}}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

// TestTaskNewDefaultsRoleProfiles: on the shipped default with the four role profiles declared, task new binds the
// default with each Artisan step naming its role's profile, the same definition as a profiled custom file. The review's
// defaulted profile weakens the shipped default, so it is refused until the Guildmaster approves that revision.
func TestTaskNewDefaultsRoleProfiles(t *testing.T) {
	cfg := writeDirector(t, rolesTOML)
	path := emptyStore(t)
	want := profiledDefault(t)
	stderr := refuseTask(t, path, cfg, "app")
	for _, s := range []string{"not authorized", want.Digest(), "step review", "guildhall workflow approve"} {
		if !strings.Contains(stderr, s) {
			t.Errorf("unapproved: stderr %q does not name %q", stderr, s)
		}
	}
	approveRepo(t, path, cfg, "app")
	newTask(t, path, cfg, "app", "Task", "t-1")
	if got := readTask(t, path, "app", "t-1").Workflow; got != want.Revision() {
		t.Fatalf("workflow = %+v, want the profiled default %+v", got, want.Revision())
	}
}

// TestTaskNewRoleProfileMissing: with the reviewer's profile not declared, the review has no profile and task new is
// refused with the remedy naming the profile to declare.
func TestTaskNewRoleProfileMissing(t *testing.T) {
	cfg := writeDirector(t, strings.Replace(rolesTOML, "[profile.reviewer]\nadapter = \"fake\"\n", "", 1))
	path := emptyStore(t)
	stderr := refuseTask(t, path, cfg, "app")
	if !strings.Contains(stderr, `step "review"`) || !strings.Contains(stderr, "declare [profile.reviewer] with adapter, or set a profile on that step") {
		t.Fatalf("stderr %q does not give the role profile remedy", stderr)
	}
}

// TestTaskNewGuildmasterReviewGetsNoDefault: a review overridden to the Guildmaster gets no defaulted profile, so the
// effective workflow strengthens the shipped default and needs no approval.
func TestTaskNewGuildmasterReviewGetsNoDefault(t *testing.T) {
	cfg := writeDirector(t, rolesTOML+"\n[repository.workflow_overrides.review]\nactor = \"guildmaster\"\n")
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Task", "t-1")
	def, err := effectiveWorkflow(filepath.Join(filepath.Dir(cfg), "workflows"), mustLoad(t, cfg), mustRepository(t, cfg, "app"), config.TaskKindFeature)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range def.Steps {
		if s.ID == "review" && s.Profile() != "" {
			t.Fatalf("Guildmaster review has profile %q", s.Profile())
		}
	}
	if got := readTask(t, path, "app", "t-1").Workflow; got != def.Revision() {
		t.Fatalf("workflow = %+v, want %+v", got, def.Revision())
	}
	if got := storeApprovals(t, path); len(got) != 0 {
		t.Fatalf("approvals = %+v, want none", got)
	}
}

func mustLoad(t *testing.T, cfg string) *config.Config {
	t.Helper()
	c, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustRepository(t *testing.T, cfg, repo string) config.Repository {
	t.Helper()
	r, ok := mustLoad(t, cfg).Repository(repo)
	if !ok {
		t.Fatalf("no repository %s", repo)
	}
	return r
}

// TestBoundWorkflowResolvesByName: a task's bound revision resolves by its name after the repository's workflow
// setting moves to another workflow, and is refused, naming the workflow and why, once its file is edited.
func TestBoundWorkflowResolvesByName(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	dir := filepath.Join(filepath.Dir(cfg), "workflows")
	c := mustLoad(t, cfg)
	r := mustRepository(t, cfg, "app")
	bound, err := effectiveWorkflow(dir, c, r, config.TaskKindFeature)
	if err != nil {
		t.Fatal(err)
	}
	rev := bound.Revision()
	r.Workflow = "human-review"
	if moved, err := effectiveWorkflow(dir, c, r, config.TaskKindFeature); err != nil || moved.Name != "human-review" {
		t.Fatalf("repository now runs %v, %v", moved, err)
	}
	got, err := boundWorkflow(dir, c, r, rev)
	if err != nil || got.Revision() != rev {
		t.Fatalf("boundWorkflow = %+v, %v; want %+v", got, err, rev)
	}

	edited := testDefault(t)
	edited.Description = "Edited after the task was created."
	data, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "test-default.json"), string(data))
	_, err = boundWorkflow(dir, c, r, rev)
	if !errors.Is(err, interp.ErrRevision) || !strings.Contains(err.Error(), `bound workflow "test-default"`) || !strings.Contains(err.Error(), "changed after the task was created") {
		t.Fatalf("edited file: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "test-default.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = boundWorkflow(dir, c, r, rev); err == nil || !strings.Contains(err.Error(), `bound workflow "test-default"`) {
		t.Fatalf("missing file: %v", err)
	}
}

// copyRunConfig copies testdata/run into a new directory and returns the copy of its config file named config.
func copyRunConfig(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, config)
}

// setWorkflow rewrites the config file's workflow setting from one workflow to another.
func setWorkflow(t *testing.T, cfg, from, to string) {
	t.Helper()
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), `workflow = "`+from+`"`, `workflow = "`+to+`"`, 1)
	if s == string(b) {
		t.Fatalf("%s has no workflow %s", cfg, from)
	}
	writeFile(t, cfg, s)
}

// TestOpenTaskKeepsItsWorkflowWhenTheRepositorySwitches: a task created on fake-grill keeps starting, finishing its
// Director stage and taking its Artisan results after the repository's workflow setting moves to fake-straight.
func TestOpenTaskKeepsItsWorkflowWhenTheRepositorySwitches(t *testing.T) {
	d := newRunDirectorAt(t, copyRunConfig(t, "grill.toml"))
	setWorkflow(t, d.config, "fake-grill", "fake-straight")
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	file, _ := grillNotesFile(t)
	if code, out, stderr := d.finish(t, "grill", "1", "--summary", "Agreed.", "--output", "notes="+file); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("step finish: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	done := append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)
	if code, out, stderr := d.run(t, done...); code != 0 || out != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Fatalf("step done: exit %d, %q, stderr %s", code, out, stderr)
	}
}

// TestOpenTaskStrandsWhenItsWorkflowFileIsEdited: editing the bound workflow's file after the task was created
// refuses its next advance with an error naming the bound workflow.
func TestOpenTaskStrandsWhenItsWorkflowFileIsEdited(t *testing.T) {
	d := newRunDirectorAt(t, copyRunConfig(t, "config.toml"))
	file := filepath.Join(filepath.Dir(d.config), "workflows", "fake-straight.json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(b), `"description": "The straight path`, `"description": "Edited: the straight path`, 1))
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...)
	if code != 1 || out != "" || !strings.Contains(stderr, `bound workflow "fake-straight"`) || !strings.Contains(stderr, "changed after the task was created") {
		t.Fatalf("task start: exit %d, %q, stderr %s; want 1 naming the bound workflow", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("attempts = %+v, want none", got)
	}
}
