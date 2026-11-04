package setup

import (
	"os"

	"golang.org/x/sys/unix"
)

// noEcho turns off echo on the terminal f and returns how to turn it back on; f not a terminal is an error.
func noEcho(f *os.File) (func(), error) {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	old := *t
	t.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, &old) }, nil
}
