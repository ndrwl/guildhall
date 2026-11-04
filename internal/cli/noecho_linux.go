package cli

import "golang.org/x/sys/unix"

// Terminal attribute requests of this platform, for noEcho.
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
