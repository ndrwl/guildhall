package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Windows copy is named by drive letter, with either separator, and an explicit flag agrees with it however it is spelled.
func TestCopyWindowsDriveLetterPath(t *testing.T) {
	dir := newCopy(t)
	if filepath.VolumeName(dir) == "" {
		t.Fatalf("copy %s has no drive letter", dir)
	}
	native := filepath.Join(dir, "director.store")
	_, want, _ := runTask(t, "list", "--store", native, "--json")
	for _, args := range [][]string{
		{"task", "list", "--copy", filepath.ToSlash(dir), "--json"},
		{"task", "list", "--copy", dir, "--store", filepath.ToSlash(native), "--json"},
		{"task", "list", "--copy", strings.ToUpper(dir), "--store", strings.ToLower(native), "--json"},
	} {
		if code, got, stderr := runIn(t, "", args...); code != 0 || got != want {
			t.Errorf("%q: exit %d, stdout %q, stderr %s; want %q", args, code, got, stderr, want)
		}
	}
	if code, got, stderr := runIn(t, filepath.ToSlash(filepath.Join(dir, "workflows")), "task", "list", "--json"); code != 0 || got != want {
		t.Errorf("discovery from a forward-slash directory: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
}

// Discovery stops at the drive root, and the state/ rule matches however the directory's case is spelled.
func TestCopyWindowsDiscovery(t *testing.T) {
	dir := markCopy(t, bareCopy(t))
	work := filepath.Join(dir, "state", "app", "t-1", "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, wd := range []string{work, strings.ToUpper(work), strings.ToLower(filepath.Join(dir, "STATE"))} {
		if code, _, stderr := runIn(t, wd, "task", "list", "--json"); code != 2 || !strings.Contains(stderr, noCopyLine(wd)) {
			t.Errorf("from %s: exit %d, stderr %q; want the no-copy usage error", wd, code, stderr)
		}
	}
	root := filepath.VolumeName(dir) + `\`
	if got, ok, err := discoverCopy(func() (string, error) { return root, nil }); err != nil || ok || got != root {
		t.Errorf("from the drive root: %q, %v, %v; want no copy and the walk stopped there", got, ok, err)
	}
}
