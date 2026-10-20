package artisan_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// TestListenKeepsUnreachableEndpoint: a leftover socket that refuses access
// cannot be proven stale, as with scratch L19's unreadable socket.
func TestListenKeepsUnreachableEndpoint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	path := endpointPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	self := mustSelf(t)
	if _, err := artisan.Listen(path, testKey, self, ownedBy(self), &testHandler{}); !errors.Is(err, artisan.ErrEndpointUnknown) {
		t.Errorf("Listen over an unreachable socket = %v, want ErrEndpointUnknown", err)
	}
	assertExists(t, path, "unreachable endpoint")
}
