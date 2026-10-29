package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The CLI must refuse an unconfigured Artisan before writing a task or advancing it. A role step always resolves its
// role profile, shipped if the copy declares none, so the unconfigured step is one whose type has no role.
func TestTaskNewRefusesImplicitFake(t *testing.T) {
	cfg := writeDirector(t, strings.Replace(directorTOML, `workflow = "test-default"`, `workflow = "refuting"`, 1))
	writeFile(t, filepath.Join(filepath.Dir(cfg), "workflows", "refuting.json"), `{"schema": "guildhall.workflow/1", "name": "refuting", "steps": [
  {"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "demo"}},
  {"id": "refute", "type": "refute", "version": 1, "inputs": {"spec": ["plan.spec"]}}]}`)
	path := emptyStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "No pretend work", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, `step "refute"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "custom workflow") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("admission changed store: %v", err)
	}
}

// TestAdvanceRefusesImplicitFake: a legacy task's plan step with no profile, in a copy that declares no planner,
// runs under the shipped claude planner, never an implicit fake; the test binary refuses claude before recording.
func TestAdvanceRefusesImplicitFake(t *testing.T) {
	d := newRunDirector(t)
	cfg, err := os.ReadFile(filepath.Join(filepath.Dir(d.config), "workflows", "fake-straight.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Dir(d.config))); err != nil {
		t.Fatal(err)
	}
	d.config = filepath.Join(dir, filepath.Base(d.config))
	toml, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, d.config, strings.Replace(string(toml), "[profile.planner]\nadapter = \"fake\"\n", "", 1))
	missing := strings.ReplaceAll(string(cfg), `"settings": {"profile": "demo"}`, `"settings": {}`)
	writeFile(t, filepath.Join(dir, "workflows", "fake-straight.json"), missing)
	def, err := workflow.Parse([]byte(missing))
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
	before, err := os.ReadFile(d.store)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("legacy")...)...)
	if code != 1 || out != "" || !strings.Contains(stderr, "step plan") || !strings.Contains(stderr, `"claude" in a test binary`) {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(d.store)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("advance changed store: %v", err)
	}
}
