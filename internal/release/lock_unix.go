//go:build !windows

package release

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock on f without waiting; false when another open file holds it.
func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// unlockFile releases tryLock's flock.
func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
