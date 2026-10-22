//go:build unix

package endpointtest

import (
	"os"
	"testing"
)

// checkModes checks the socket is its owner's alone.
func checkModes(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode %v, want 0600", perm)
	}
}
