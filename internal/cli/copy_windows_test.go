package cli

import (
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
		if code, got, stderr := runEnv(t, "", args...); code != 0 || got != want {
			t.Errorf("%q: exit %d, stdout %q, stderr %s; want %q", args, code, got, stderr, want)
		}
	}
	if code, got, stderr := runEnv(t, filepath.ToSlash(dir), "task", "list", "--json"); code != 0 || got != want {
		t.Errorf("GUILDHALL_COPY with forward slashes: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
}
