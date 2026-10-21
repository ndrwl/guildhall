package artisan

import (
	"os"

	"golang.org/x/sys/windows"
)

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// CTRL_BREAK reaches only a worker sharing the console and may end it
// abruptly, so it is not.
const interruptGraceful = false

func lockFile(f *os.File, nonblocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
}

func lockBusy(err error) bool { return err == windows.ERROR_LOCK_VIOLATION }
