package artisan

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// LockAttempt acquires the launcher's exclusive attempt lock, creating only
// attempt.lock if needed. The directory must exist. ctx bounds contention;
// other open or locking errors are returned immediately. The caller must call
// the returned release function once, after its attempt-file operations.
func LockAttempt(ctx context.Context, dir string) (release func(), err error) {
	return LockPath(ctx, filepath.Join(dir, LockFile))
}

// LockPath acquires an exclusive lock on the file at path, creating it if needed, as LockAttempt does on
// attempt.lock: `guildhall result submit` serialises its submissions on the attempt's SubmitLockFile with it. ctx
// bounds contention; the caller must call release once.
func LockPath(ctx context.Context, path string) (release func(), err error) {
	f, err := LockPathAt(ctx, path, 0)
	if err != nil {
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// LockPathAt acquires an exclusive lock on the file at path, creating it (0600) if needed and never truncating it,
// and returns the locked file; closing it releases the lock, as does the holder's death. ctx bounds contention only:
// a ctx already done when LockPathAt is called fails it without opening or creating the file, but the time spent
// opening the file does not count against ctx, and once the file is open at least one lock attempt is always made,
// so a slow open never turns a free lock into a refusal. On Windows it locks the one byte at offset, so a holder
// locking past the file's content leaves that content readable through other handles; on Unix it flocks the whole
// file and ignores offset. LockPath is LockPathAt at offset 0.
func LockPathAt(ctx context.Context, path string, offset int64) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = lockFile(f, offset, true)
		if err == nil {
			return f, nil
		}
		if !lockBusy(err) {
			break
		}
		if err = ctx.Err(); err != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = f.Close()
	return nil, err
}
