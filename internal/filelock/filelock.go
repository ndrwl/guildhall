// Package filelock is Guildhall's one exclusive kernel file lock: flock on Unix, LockFileEx on Windows.
//
// See docs/contracts/filelock.md.
package filelock

import (
	"context"
	"os"
	"time"
)

// poll is how often Lock tries again after a refused attempt.
const poll = 10 * time.Millisecond

// Lock takes an exclusive lock on f without blocking the thread. It always makes at least one nonblocking attempt,
// whatever ctx's state, and after a refused attempt polls every 10 ms until it gets the lock or ctx is done, when it
// returns ctx.Err(); so context.DeadlineExceeded means another holder refused it. Any other error is returned at once.
// On Windows it locks the one byte of f at offset, so content before it stays readable through other handles; on Unix
// it flocks the whole file and ignores offset. Closing f, or the holder's death, releases the lock.
func Lock(ctx context.Context, f *os.File, offset int64) error {
	for {
		err := lock(f, offset, true)
		if err == nil || !busy(err) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// LockPath opens the file at path, creating it (0600) if needed and never truncating it, and takes Lock on it,
// returning the locked file; closing it releases the lock. A ctx already done fails it without opening or creating
// the file, but the time spent opening the file does not count against ctx, so a slow open never turns a free lock
// into a refusal.
func LockPath(ctx context.Context, path string, offset int64) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := Lock(ctx, f, offset); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// Wait takes the same lock as Lock, blocking until it is free.
func Wait(f *os.File, offset int64) error { return lock(f, offset, false) }

// Unlock releases a lock taken on f at offset, before f is closed. On Windows closing a handle does not release its
// locks at once, so a caller that hands the lock on promptly unlocks first.
func Unlock(f *os.File, offset int64) error { return unlock(f, offset) }
