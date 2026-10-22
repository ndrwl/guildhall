package endpoint

import (
	"errors"

	"golang.org/x/sys/windows"
)

const maxPath = 107

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }

// restrict does nothing: the socket inherits its directory's ACL.
func restrict(string) error { return nil }
