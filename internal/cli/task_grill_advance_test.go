package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/skills"
)

// unattendedRun is a Director copy whose repository app runs the shipped default workflow, with the fake Artisan's
// grill guide grillGuide and a research guide that writes its notes, and task app/t-1 created with --grill unattended.
func unattendedRun(t *testing.T, grillGuide string) runDirector {
	t.Helper()
	cfg := copyRunConfig(t, "config.toml")
	setWorkflow(t, cfg, "fake-straight", "default")
	stages := filepath.Join(filepath.Dir(cfg), "baseline", "stages")
	writeFile(t, filepath.Join(stages, "grill.md"), grillGuide)
	writeFile(t, filepath.Join(stages, "research.md"), "# Research\n\nguildhall-fake: output=notes.md:# Research\\n\\nThe list is sorted in the view.\n")
	d := newRunDirectorBare(t, cfg)
	// The grill renders the release's own guildhall-grill-unattended and guildhall-domain-model.
	d.installSkills = skills.Install
	code, out, stderr := runTask(t, "new", "--store", d.store, "--config", d.config, "--repo", "app", "--title", "Follow up the hallmark",
		"--grill", "unattended", "--grill-reason", "A hallmark follow-up whose contracts are written.", "t-1")
	if code != 0 {
		t.Fatalf("task new --grill unattended on the default workflow: exit %d, %s, stderr %s", code, out, stderr)
	}
	return d
}

// TestUnattendedGrillOnTheDefaultWorkflow: a feature task created with --grill unattended on the shipped default
// workflow is admitted, its grill launches an Artisan attempt under the researcher profile with
// guildhall-grill-unattended in its prompt, and its notes move the task on to research.
func TestUnattendedGrillOnTheDefaultWorkflow(t *testing.T) {
	d := unattendedRun(t, "# Grill\n\nguildhall-fake: output=notes.md:# Grill notes\\n\\n## Questions\\n\\n1. Sort by id? Recommended: yes.\n")
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "grill", 1)
	dir := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "grill", Attempt: 1}.Dir(d.state)
	data, err := os.ReadFile(filepath.Join(dir, "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m render.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var skills []string
	for _, s := range m.Skills {
		skills = append(skills, s.Name)
	}
	if m.Identity.Profile != "researcher" || m.Role.Path == "roles/director.md" || !slices.Equal(skills, []string{"guildhall-grill-unattended", "guildhall-domain-model"}) {
		t.Errorf("grill prompt profile %q, role %s, skills %q; want an Artisan under researcher with the unattended skills", m.Identity.Profile, m.Role.Path, skills)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt", render.PromptFile))
	if err != nil || !strings.Contains(string(prompt), "guildhall-grill-unattended") {
		t.Errorf("grill prompt does not carry guildhall-grill-unattended: %v", err)
	}
	done := append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--result", d.result("grill", 1))...)
	if code, out, stderr := d.run(t, done...); code != 0 || out != `{"wait":"attempt","step":"research","attempt":1}`+"\n" {
		t.Fatalf("step done grill: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "research", 1)
	if code, _, stderr := runTask(t, "grill", "--store", d.store, "--config", d.config, "app/t-1", "live", "--reason", "Too late."); code != 1 || !strings.Contains(stderr, "research has started") {
		t.Errorf("task grill after research started: exit %d, stderr %s", code, stderr)
	}
}

// TestUnattendedGrillEscalatesToADirectorStage: a failed Artisan grill waits for the Director, which switches the
// task to live with task grill and retries the grill as a Director stage.
func TestUnattendedGrillEscalatesToADirectorStage(t *testing.T) {
	d := unattendedRun(t, "# Grill\n\nguildhall-fake: outcome=failed\n")
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "grill", 1)
	done := append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--result", d.result("grill", 1))...)
	if code, out, stderr := d.run(t, done...); code != 0 || out != `{"wait":"director","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("step done grill: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, _, stderr := runTask(t, "grill", "--store", d.store, "--config", d.config, "app/t-1", "live", "--reason", "The answer changes the goal."); code != 0 {
		t.Fatalf("task grill live: exit %d, stderr %s", code, stderr)
	}
	retry := append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--reason", "Grill the Guildmaster live.")...)
	if code, out, stderr := d.run(t, retry...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":2}`+"\n" {
		t.Fatalf("step retry grill: exit %d, %q, stderr %s", code, out, stderr)
	}
	if g := showGrill(t, d.store, "t-1"); g.Line != "grill: live (director): The answer changes the goal." {
		t.Errorf("grill = %+v", g)
	}
}

// TestTaskDependsAfterAnUnattendedGrill: an unattended grill runs as an Artisan, so once it has an attempt the task is
// past the dependency gate as the engine judges it, and a dependency added after that carries the past-gate note.
func TestTaskDependsAfterAnUnattendedGrill(t *testing.T) {
	d := unattendedRun(t, "# Grill\n\nguildhall-fake: output=notes.md:# Grill notes\n")
	newTask(t, d.store, d.config, "app", "Another", "t-2")
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "grill", 1)
	code, out, stderr := runTask(t, "depends", "add", "--store", d.store, "--config", d.config, "--repo", "app", "--reason", "Wait for app/t-2.", "t-1", "app/t-2")
	if code != 0 {
		t.Fatalf("depends add: exit %d, stderr %s", code, stderr)
	}
	if rc := decodeJSON[dependsReceipt](t, out); rc.Note != dependsNotePastGate {
		t.Errorf("depends add after an unattended grill attempt: %+v, want the past-gate note", rc)
	}
}

// TestTaskGrantOnAnUnattendedGrill: an unattended grill is an Artisan step under the researcher profile, so a task
// grant may name it when that profile runs claude, and a live grill, run by the Director, may not.
func TestTaskGrantOnAnUnattendedGrill(t *testing.T) {
	cfg := copyRunConfig(t, "config.toml")
	setWorkflow(t, cfg, "fake-straight", "default")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "[profile.researcher]\nadapter = \"fake\"", "[profile.researcher]\nadapter = \"claude\"", 1)
	if s == string(b) {
		t.Fatalf("%s has no fake researcher profile", cfg)
	}
	writeFile(t, cfg, s)
	d := newRunDirectorBare(t, cfg)
	for _, task := range []string{"t-1", "t-2"} {
		args := []string{"new", "--store", d.store, "--config", d.config, "--repo", "app", "--title", "Task"}
		if task == "t-1" {
			args = append(args, "--grill", "unattended", "--grill-reason", "Contracts are written.")
		}
		if code, _, stderr := runTask(t, append(args, task)...); code != 0 {
			t.Fatalf("task new %s: exit %d, stderr %s", task, code, stderr)
		}
	}
	create := func(task string) (int, string, string) {
		return d.run(t, "task", "grant", "create", "--store", d.store, "--config", d.config, "--repo", "app", "--task", task,
			"--step", "grill", "--tool", "Bash(go:*)", "--reason", "The grill reads go docs.")
	}
	if code, out, stderr := create("t-1"); code != 0 || !strings.Contains(out, "task-grant:app:t-1:g1") {
		t.Errorf("grant on an unattended grill: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, _, stderr := create("t-2"); code != 1 || !strings.Contains(stderr, "claude adapter") {
		t.Errorf("grant on a live grill: exit %d, stderr %s", code, stderr)
	}
}
