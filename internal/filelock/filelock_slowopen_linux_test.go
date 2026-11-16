//go:build linux

package filelock_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/filelock"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestLockPathSlowOpenStillTakesAFreeLock is the regression test for the Windows flake in TestProvisionResume and
// TestProvisionNeverCopiesOverUnreadable: an open of the lock file that outlasts the caller's ctx must not turn a free
// lock into a refusal, because ctx bounds contention only.
func TestLockPathSlowOpenStillTakesAFreeLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	testenv.StallOpens(t, path, 150*time.Millisecond)
	// timing:allow the stalled open deliberately outlasts this deadline; no holder exists, so the lock must be taken.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	f, err := filelock.LockPath(ctx, path, highOffset)
	if err != nil {
		t.Fatalf("LockPath on a free lock with a slow open = %v, want the lock", err)
	}
	_ = f.Close()
}
