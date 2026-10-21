package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Windows Director may name its config with forward slashes and its repositories by drive letter.
func TestTaskNewWindowsPaths(t *testing.T) {
	toml := strings.Replace(directorTOML, `path = "/srv/lib"`, `path = 'C:\work\lib'`, 1)
	cfg := writeDirector(t, toml)
	path := emptyStore(t)

	approveRepo(t, path, filepath.ToSlash(cfg), "lib")
	newTask(t, path, filepath.ToSlash(cfg), "lib", "Slash-separated config path", "t-1")
	if got, want := readTask(t, path, "lib", "t-1").ConfigDigest, configDigest(t, cfg); got != want {
		t.Fatalf("config digest %s, want %s", got, want)
	}
}
