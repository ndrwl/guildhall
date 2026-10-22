package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Windows store path is named by drive letter, with either separator; init then refuses it in any case spelling.
func TestStoreInitWindowsPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "director.store")
	if filepath.VolumeName(path) == "" {
		t.Fatalf("store %s has no drive letter", path)
	}
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.ToSlash(path)}); code != 0 {
		t.Fatalf("init: exit %d, stderr %s", code, stderr)
	}
	app, _, stderr = newTestApp(nil, nil)
	if code := app.Run([]string{"store", "doctor", path}); code != 0 {
		t.Fatalf("doctor: exit %d, stderr %s", code, stderr)
	}
	app, _, stderr = newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", strings.ToUpper(path)}); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("init again: exit %d, stderr %s; want a refusal", code, stderr)
	}
}
