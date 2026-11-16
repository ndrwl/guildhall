//go:build unix

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lock flocks the whole of f; Unix ignores offset.
func lock(f *os.File, _ int64, nonblocking bool) error {
	flags := unix.LOCK_EX
	if nonblocking {
		flags |= unix.LOCK_NB
	}
	return unix.Flock(int(f.Fd()), flags)
}

// unlock releases lock's flock.
func unlock(f *os.File, _ int64) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

// busy is whether a nonblocking lock was refused because another open file holds it.
func busy(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) }
