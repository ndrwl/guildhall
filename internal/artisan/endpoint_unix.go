//go:build unix

package artisan

import (
	"errors"
	"os"
	"syscall"
)

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

// restrictEndpoint limits the socket to its owner.
func restrictEndpoint(path string) error { return os.Chmod(path, 0o600) }
