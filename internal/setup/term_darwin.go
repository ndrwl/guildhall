package setup

import (
	"os"

	"golang.org/x/sys/unix"
)

// noEcho turns off echo on the terminal f and returns how to turn it back on; f not a terminal is an error.
func noEcho(f *os.File) (func(), error) {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	old := *t
	t.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TIOCSETA, &old) }, nil
}
