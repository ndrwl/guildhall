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
	// A role step always resolves its role profile, shipped if the copy declares none; a refute step has no role, so
	// without a profile of its own it has no Artisan configured.
	missing := bytes.Replace(original, []byte(`{"id": "accept"`), []byte(`{"id": "refute", "type": "refute", "version": 1, "inputs": {"spec": ["plan.spec"]}},
    {"id": "accept"`), 1)
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
		t.Logf("guildhall %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, stderr)
		if err == nil || out != "" || !strings.Contains(stderr, `step "refute"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "custom workflow") {
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
	started := d.advance([]string{"task", "start"}, "t-1")
	t.Logf("explicit fake profile: task start output: %s", started)
	d.want(started, `{"wait":"attempt","step":"plan","attempt":1}`)
	done := d.done("plan", 1) // Explicit fake actually produces a result, not merely admission.
	t.Logf("explicit fake profile: step done output: %s", done)
	d.want(done, `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	result, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "plan-1", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explicit fake profile: persisted Artisan result: %s", result)
}
