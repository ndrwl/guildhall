//go:build unix

package session

import (
	"errors"
	"syscall"
)

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
