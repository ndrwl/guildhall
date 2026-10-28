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

// profiledDefault is the shipped default with explicit role profiles written into its file, named and described as
// the shipped default: the legacy definition a task bound while role profiles were written into the digest.
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
     "inputs": {"head": ["build.head"], "review": ["review.outcome"], "spec": ["accept.spec"]}},
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

// noProfilesTOML is rolesTOML with no [profile.*] section, so every role step runs under a shipped profile.
var noProfilesTOML = strings.NewReplacer(
	"[profile.researcher]\nadapter = \"fake\"\n\n", "",
	"[profile.planner]\nadapter = \"fake\"\n\n", "",
	"[profile.builder]\nadapter = \"fake\"\n\n", "",
	"[profile.reviewer]\nadapter = \"fake\"\n\n", "",
).Replace(rolesTOML)

// TestTaskNewBindsTheShippedWorkflows: on the shipped default and, with --kind bug, the shipped bug workflow, task new
// binds the shipped revision byte for byte and needs no approval, whether the copy declares the role profiles or
// relies on the shipped ones; each Artisan step resolves its role profile at use.
func TestTaskNewBindsTheShippedWorkflows(t *testing.T) {
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	bug, err := workflow.Bug()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noProfilesTOML, "[profile.") {
		t.Fatal("noProfilesTOML still declares a profile")
	}
	for name, toml := range map[string]string{"shipped profiles": noProfilesTOML, "declared profiles": rolesTOML} {
		t.Run(name, func(t *testing.T) {
			cfg := writeDirector(t, toml)
			path := emptyStore(t)
			for id, c := range map[string]struct {
				kind string
				want *workflow.Definition
			}{"t-feature": {config.TaskKindFeature, def}, "t-bug": {config.TaskKindBug, bug}} {
				code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Task", "--kind", c.kind, id)
				if code != 0 {
					t.Fatalf("task new --kind %s: exit %d, stderr %s", c.kind, code, stderr)
				}
				if got := readTask(t, path, "app", id).Workflow; got != c.want.Revision() {
					t.Errorf("%s workflow = %+v, want the shipped %+v", c.kind, got, c.want.Revision())
				}
			}
			if got := storeApprovals(t, path); len(got) != 0 {
				t.Fatalf("approvals = %+v, want none", got)
			}
			c := mustLoad(t, cfg)
			for _, s := range def.Steps {
				if s.Actor() != workflow.ArtisanActor {
					continue
				}
				if _, err := c.Profile(workflow.EffectiveProfile(s)); err != nil {
					t.Errorf("step %s: %v", s.ID, err)
				}
			}
			if want := map[string]string{"shipped profiles": "claude", "declared profiles": "fake"}[name]; c.Profiles["reviewer"].Adapter != want {
				t.Errorf("reviewer adapter = %q, want %q", c.Profiles["reviewer"].Adapter, want)
			}
		})
	}
}

// TestExplicitProfileChangesTheDigest: a profile written into a step is part of the definition, so it changes the
// digest, and on the review it still weakens the shipped default, which only the Guildmaster's approval admits.
func TestExplicitProfileChangesTheDigest(t *testing.T) {
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := shippedDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	profiled := profiledDefault(t)
	if profiled.Digest() == def.Digest() {
		t.Fatal("explicit profiles left the digest unchanged")
	}
	weak := workflow.Weakenings(def, profiled)
	if len(weak) != 1 || weak[0].Step != "review" || !strings.Contains(weak[0].Reason, "profile changed") {
		t.Fatalf("weakenings = %v, want only the review's profile", weak)
	}
	auth := workflow.Authority{Shipped: shipped, Approved: func(string) bool { return false }}
	if _, err := auth.Admit(profiled); err == nil || !strings.Contains(err.Error(), "step review") {
		t.Fatalf("Admit = %v, want the review's profile refused", err)
	}
}

// TestTaskNewGuildmasterReviewGetsNoDefault: a review overridden to the Guildmaster resolves no profile, and the
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
		if s.ID == "review" && workflow.EffectiveProfile(s) != "" {
			t.Fatalf("Guildmaster review has profile %q", workflow.EffectiveProfile(s))
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

// TestBoundWorkflowAcceptsTheLegacyDefaultedDigest: a task bound while role profiles were written into the
// definition bound the profiled default's digest; it still resolves, to that profiled definition, while the bare
// default resolves as itself and any other digest strands.
func TestBoundWorkflowAcceptsTheLegacyDefaultedDigest(t *testing.T) {
	cfg := writeDirector(t, rolesTOML)
	dir := filepath.Join(filepath.Dir(cfg), "workflows")
	c := mustLoad(t, cfg)
	r := mustRepository(t, cfg, "app")
	legacy := profiledDefault(t)
	got, err := boundWorkflow(dir, c, r, legacy.Revision())
	if err != nil || got.Digest() != legacy.Digest() {
		t.Fatalf("boundWorkflow(legacy) = %v, %v; want the profiled default", got, err)
	}
	for _, s := range got.Steps {
		if s.ID == "review" && s.Profile() != "reviewer" {
			t.Errorf("legacy review profile = %q, want reviewer", s.Profile())
		}
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := boundWorkflow(dir, c, r, def.Revision()); err != nil || got.Digest() != def.Digest() {
		t.Fatalf("boundWorkflow(shipped) = %v, %v", got, err)
	}
	other := workflow.Revision{Name: "default", Digest: "sha256:" + strings.Repeat("e", 64)}
	if _, err := boundWorkflow(dir, c, r, other); !errors.Is(err, interp.ErrRevision) {
		t.Fatalf("boundWorkflow(other) = %v, want ErrRevision", err)
	}
}

// TestLegacyDefaultedTaskStillAdvances: a task whose plan step's role profile was written into the digest it bound
// keeps advancing once the workflow file names no profile there: its plan runs and its result is taken.
func TestLegacyDefaultedTaskStillAdvances(t *testing.T) {
	cfg := copyRunConfig(t, "config.toml")
	file := filepath.Join(filepath.Dir(cfg), "workflows", "fake-straight.json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	const plan = `{"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "demo"}}`
	if !strings.Contains(string(b), plan) {
		t.Fatalf("%s has no plan step %s", file, plan)
	}
	// The task binds the legacy shape: plan's role profile written in.
	writeFile(t, file, strings.Replace(string(b), plan, `{"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "planner"}}`, 1))
	d := newRunDirectorAt(t, cfg)
	// The file now names no profile on plan, as the definition did before the defaults were applied.
	writeFile(t, file, strings.Replace(string(b), plan, `{"id": "plan", "type": "plan", "version": 1}`, 1))
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	done := append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)
	if code, out, stderr := d.run(t, done...); code != 0 || out != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Fatalf("step done: exit %d, %q, stderr %s", code, out, stderr)
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
