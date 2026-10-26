package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI must refuse an unconfigured Artisan before writing a task or advancing it.
func TestTaskNewRefusesImplicitFake(t *testing.T) {
	cfg := writeDirector(t, strings.ReplaceAll(directorTOML, "default_adapter = \"fake\"\n", ""))
	path := emptyStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "No pretend work", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, `step "research"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "default_adapter") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("admission changed store: %v", err)
	}
}

func TestAdvanceRefusesImplicitFake(t *testing.T) {
	d := newRunDirector(t)
	cfg, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Dir(d.config))); err != nil {
		t.Fatal(err)
	}
	d.config = filepath.Join(dir, filepath.Base(d.config))
	writeFile(t, d.config, strings.ReplaceAll(string(cfg), "default_adapter = \"fake\"\n", ""))
	before, err := os.ReadFile(d.store)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...)
	if code != 1 || out != "" || !strings.Contains(stderr, `step "plan"`) || !strings.Contains(stderr, "default_adapter") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	after, err := os.ReadFile(d.store)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("advance changed store: %v", err)
	}
}
