package steward

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/git"
)

// TestSecretsLockIsPrivateLock: a secrets set or apply takes .rollout.lock through privateLock, so the lock holds this
// process's owner record, a second taker is refused with a busy error naming that owner, and release frees it,
// leaving the file.
func TestSecretsLockIsPrivateLock(t *testing.T) {
	dir := t.TempDir()
	s := &Secrets{Private: git.WorkTree{Dir: dir}, Now: func() time.Time { return lockTime }}
	release, err := s.lock()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, lockName)
	if o := readLockOwner(path); !o.Known() || !o.At.Equal(lockTime) {
		t.Fatalf("lock owner = %+v, want this process at %s", o, lockTime)
	}
	_, err = s.lock()
	checkNamesOwner(t, err)
	if _, err := privateLock(dir, lockTime); err == nil {
		t.Fatal("a rollout took the lock a secrets set holds")
	}
	release()
	if dirLockHeld(t, dir) {
		t.Fatal("lock held after release")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("release removed the lock file: %v", err)
	}
}
