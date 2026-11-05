//go:build linux

package steward

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestTakePrivateLockSlowOpenIsNotBusy is the Steward-level regression test for the Windows flake in
// TestProvisionResume and TestProvisionNeverCopiesOverUnreadable: opening .rollout.lock takes longer than lockWait,
// with no holder, and TakePrivateLock must take the lock rather than report it busy by an unknown owner.
func TestTakePrivateLockSlowOpenIsNotBusy(t *testing.T) {
	dir := t.TempDir()
	testenv.StallOpens(t, filepath.Join(dir, lockName), lockWait+50*time.Millisecond)
	release, err := TakePrivateLock(dir, LockOwner{})
	if errors.Is(err, ErrBusy) {
		t.Fatalf("TakePrivateLock on a free lock with a slow open reported it busy (the Windows flake): %v", err)
	}
	if err != nil {
		t.Fatalf("TakePrivateLock on a free lock with a slow open = %v, want the lock", err)
	}
	release()
}
