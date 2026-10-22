//go:build unix

package endpoint

import (
	"errors"
	"os"
	"syscall"
)

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

// restrict limits the socket to its owner.
func restrict(path string) error { return os.Chmod(path, 0o600) }
