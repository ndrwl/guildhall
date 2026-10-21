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
	f, err := os.OpenFile(filepath.Join(dir, LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		err = lockFile(f, true)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !lockBusy(err) {
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
