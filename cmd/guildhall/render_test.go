package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
	"github.com/ndrwl/guildhall/skills"
)

func testReleaseRendering(t *testing.T, bin string) {
	t.Helper()
	tmp := t.TempDir()
	role := filepath.Join(tmp, "role.md")
	if err := os.WriteFile(role, []byte("Role\n"), 0644); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(tmp, "managed")
	name := "guildhall-grill-notes"
	if err := os.MkdirAll(filepath.Join(managed, name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, name, "SKILL.md"), []byte("decoy"), 0644); err != nil {
		t.Fatal(err)
	}
	stage := render.Stage{Repository: "repo", Task: "task", Attempt: 1, Dir: filepath.Join(tmp, "attempt"), Step: workflow.Step{ID: "review", Type: "review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"artisan"`), "skills": json.RawMessage(`["guildhall-grill-notes"]`), "profile": json.RawMessage(`"reviewer"`), "instructions": json.RawMessage(`"Check the release defaults."`)}}}
	baseline := render.Baseline{Role: render.Source{Name: "role.md", Path: role}, Guide: render.Source{Name: "role.md", Path: role}}
	data, err := json.Marshal(map[string]any{"Stage": stage, "Baseline": baseline, "Managed": managed, "Director": managed})
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(tmp, "request.json")
	if err := os.WriteFile(request, data, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "render", request).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var manifest render.Manifest
	if err := json.Unmarshal(out, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Skills) != 1 || manifest.Skills[0].Root != "release" {
		t.Fatalf("skills: %+v", manifest.Skills)
	}
	if manifest.Identity.Profile != "reviewer" {
		t.Errorf("profile = %q", manifest.Identity.Profile)
	}
	prompt, err := os.ReadFile(filepath.Join(stage.Dir, "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prompt, []byte("Check the release defaults.")) {
		t.Error("rendered prompt omitted instructions")
	}
	for _, file := range []string{"SKILL.md", "CREDITS.md"} {
		got, err := os.ReadFile(filepath.Join(stage.Dir, "skills", name, file))
		if err != nil {
			t.Fatal(err)
		}
		want, err := fs.ReadFile(skills.Release(), name+"/"+file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("rendered %s differs from release", file)
		}
	}
	out, err = exec.Command(bin, "render", request).CombinedOutput()
	if err == nil {
		t.Fatalf("replaced existing attempt: %s", out)
	}
}
