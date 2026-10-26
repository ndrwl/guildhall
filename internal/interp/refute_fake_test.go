package interp_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Execute the fake runtime with the real rendered prompt and consume its result.
// This pins deterministic file exchange, not a model's ability to critique a plan.
func TestRefuteFakeAdapter(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	release := filepath.Join(root, "baseline")
	if err := baseline.Install(release); err != nil {
		t.Fatal(err)
	}
	source := func(name string) render.Source {
		return render.Source{Name: name, Path: filepath.Join(release, filepath.FromSlash(name))}
	}
	role := filepath.Join(root, "fake-role.md")
	if err := os.WriteFile(role, []byte("guildhall-fake: output=findings.md:# Refutation: high severity; suggested spec change: bound the queue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "spec.md")
	spec := []byte("# Spec\nUse a queue.\n")
	if err := os.WriteFile(specPath, spec, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := render.Prompt(render.Stage{Repository: "app", Task: "t7", Step: workflow.Step{ID: "refute", Type: "refute", Version: 1}, Attempt: 1, Dir: filepath.Join(root, "prompt")}, []render.Input{{Port: "spec", Name: "spec.md", Path: specPath, SHA256: sum(spec)}}, render.Baseline{Role: render.Source{Name: "roles/fake.md", Path: role}, Guide: source("stages/refute.md")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(got.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "### Output port `findings`") || !strings.Contains(string(prompt), `"path": "findings.md"`) {
		t.Fatal("generated output contract must name findings.md")
	}
	resultPath := filepath.Join(root, "result.json")
	identity := adapter.Identity{TaskID: "t7", AttemptID: "refute-1"}
	if err := fake.Run(context.Background(), fake.Args{Identity: identity, PromptFile: got.PromptPath, ResultFile: resultPath, Role: fake.RoleWorker}, io.Discard, fake.Sleep); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ParseResult(data, identity)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != adapter.StatusSucceeded || len(result.Outputs) != 1 || result.Outputs[0].Path != "findings.md" {
		t.Fatalf("result: %+v", result)
	}
	findings, err := os.ReadFile(filepath.Join(root, result.Outputs[0].Path))
	if err != nil || !strings.Contains(string(findings), "bound the queue") {
		t.Fatalf("findings: %q %v", findings, err)
	}
}
