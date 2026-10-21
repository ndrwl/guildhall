package artisan

import (
	"errors"

	"golang.org/x/sys/windows"
)

const maxEndpointPath = 107

// isRefused reports whether a dial failed because nothing listens on the socket.
func isRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }

// restrictEndpoint does nothing: the socket inherits the state directory's ACL.
func restrictEndpoint(string) error { return nil }
