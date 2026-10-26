package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Cross the production executable boundary: test-process detection must not affect admission.
func TestBinaryRefusesImplicitFake(t *testing.T) {
	d := newBinDirector(t, filepath.Join(runCopy(t), "config.toml"))
	file := filepath.Join(filepath.Dir(d.cfg), "workflows", "fake-straight.json")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	d.newTask()
	missing := bytes.ReplaceAll(original, []byte(`"settings": {"profile": "demo"}`), []byte(`"settings": {}`))
	if err := os.WriteFile(file, missing, 0600); err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(missing)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(d.store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateTask("legacy", store.Task{Repository: "app", ID: "legacy", Title: "Legacy task", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"task", "new", "--store", d.store, "--config", d.cfg, "--repo", "app", "--title", "No fake", "t-2"},
		append([]string{"task", "start"}, append(d.flags, "legacy")...),
	} {
		before, err := os.ReadFile(d.store)
		if err != nil {
			t.Fatal(err)
		}
		out, stderr, err := d.try("", args...)
		if err == nil || out != "" || !strings.Contains(stderr, `step "plan"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "custom workflow") {
			t.Fatalf("%v: err %v out %q stderr %q", args, err, out, stderr)
		}
		after, err := os.ReadFile(d.store)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("refusal changed store: %v", err)
		}
	}
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.done("plan", 1) // Explicit fake actually produces a result, not merely admission.
}
