package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lock locks the one byte of f at offset.
func lock(f *os.File, offset int64, nonblocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, overlapped(offset))
}

// unlock releases lock's byte at offset.
func unlock(f *os.File, offset int64) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped(offset))
}

func overlapped(offset int64) *windows.Overlapped {
	return &windows.Overlapped{Offset: uint32(offset), OffsetHigh: uint32(offset >> 32)}
}

// busy is whether a nonblocking lock was refused because another handle holds it: ERROR_LOCK_VIOLATION. Our handles
// are synchronous, so ERROR_IO_PENDING should not occur; it is counted as busy too, because if it ever does, retrying
// until ctx ends is safer than failing a caller who could have had the lock.
func busy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
