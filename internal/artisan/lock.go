package artisan

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/filelock"
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
// and returns the locked file; closing it releases the lock, as does the holder's death. It is filelock.LockPath,
// whose contract it keeps: ctx bounds contention only, and on Windows it locks the one byte at offset while on Unix
// it flocks the whole file. LockPath is LockPathAt at offset 0.
func LockPathAt(ctx context.Context, path string, offset int64) (*os.File, error) {
	return filelock.LockPath(ctx, path, offset)
}
