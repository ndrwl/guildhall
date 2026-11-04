//go:build linux || darwin

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// noEcho turns off f's echo when f is a terminal, so a secret typed there is not shown, and returns whether it is one
// and how to restore it.
func noEcho(f *os.File) (terminal bool, restore func(), err error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return false, func() {}, nil
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return true, func() {}, err
	}
	return true, func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}
