//go:build unix

package endpoint_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListenCreatesItsDirectoryPrivate: a missing directory is created 0700.
func TestListenCreatesItsDirectoryPrivate(t *testing.T) {
	dir := filepath.Join(shortDir(t), "endpoints")
	listenPing(t, filepath.Join(dir, "ping.sock"), newPinger())
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode %v, want 0700", perm)
	}
}
