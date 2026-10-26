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

// The CLI must refuse an unconfigured Artisan before writing a task or advancing it.
func TestTaskNewRefusesImplicitFake(t *testing.T) {
	cfg := writeDirector(t, strings.Replace(directorTOML, `workflow = "test-default"`, `workflow = "default"`, 1))
	path := emptyStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "No pretend work", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, `step "research"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "custom workflow") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("admission changed store: %v", err)
	}
}

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
	if code != 1 || out != "" || !strings.Contains(stderr, `step "plan"`) || !strings.Contains(stderr, "custom workflow") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(d.store)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("advance changed store: %v", err)
	}
}
