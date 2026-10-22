package session

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
