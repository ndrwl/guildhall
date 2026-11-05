package steward

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The owner record of .rollout.lock: privateLock writes it, a busy error names it, and a file with no readable record
// is an unknown owner that is only ever read.

// checkNamesOwner checks that err is busy on a lock privateLock holds for this process, and names its pid and command.
func checkNamesOwner(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrBusy) || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error = %v, want one matching ErrBusy and fs.ErrExist", err)
	}
	for _, want := range []string{"busy", fmt.Sprintf("pid %d", os.Getpid()), os.Args[0], " since 2026-11-04T10:00:00Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

var lockTime = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func TestPrivateLockWritesOwnerRecord(t *testing.T) {
	dir := t.TempDir()
	release, err := privateLock(dir, lockTime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, lockName)
	o := readLockOwner(path)
	if o.PID != os.Getpid() || o.Token == "" || o.Command != strings.Join(os.Args, " ") || !o.At.Equal(lockTime) {
		t.Errorf("owner record = %+v", o)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("lock mode %v, want private", mode)
	}
	_, err = privateLock(dir, lockTime.Add(time.Hour))
	checkNamesOwner(t, err)
	var be *BusyError
	if !errors.As(err, &be) || be.Path != path || be.Owner != o {
		t.Errorf("busy error = %+v, want path %s and owner %+v", be, path, o)
	}
	release()
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 0 {
		t.Errorf("after release the lock file is %q, %v; want it in place and empty", b, err)
	}
	if dirLockHeld(t, dir) {
		t.Error("release left the lock held")
	}
}

// TestLeftoverLockFileIsTaken: a .rollout.lock no live process holds, whatever it says (an older binary's O_EXCL
// lock, free text, a partial or a whole record), is taken and overwritten with the new owner's record.
func TestLeftoverLockFileIsTaken(t *testing.T) {
	for name, content := range map[string]string{
		"empty": "", "free text": "live writer", "partial": `{"pid":12`, "no pid": `{"command":"x"}`,
		"old record": `{"pid":12,"command":"guildhall steward collect","at":"2026-01-02T03:04:05Z"}` + "\n" + strings.Repeat(" ", 200),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, lockName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			release, err := privateLock(dir, lockTime)
			if err != nil {
				t.Fatalf("privateLock over a leftover file = %v", err)
			}
			defer release()
			if o := readLockOwner(path); o.PID != os.Getpid() || !o.At.Equal(lockTime) {
				t.Errorf("owner record = %+v, want this process at %s", o, lockTime)
			}
		})
	}
}

// TestLockUnknownOwner: a held lock with an empty record, as just after a holder takes it, reads as an unknown owner
// since the file's modification time.
func TestLockUnknownOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockName)
	write := writeLockRecord
	writeLockRecord = func(*os.File, []byte) error { return nil }
	release, err := privateLock(dir, lockTime)
	writeLockRecord = write
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	_, err = privateLock(dir, lockTime)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, fs.ErrExist) ||
		!strings.Contains(err.Error(), "held by an unknown owner since 2026-01-02T03:04:05Z") || !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, want busy on %s by an unknown owner", err, path)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("lock now %q, %v; want it left empty", b, err)
	}
}

// TestPrivateLockFailedRecordLeavesLockFree: a record that cannot be written leaves the lock free.
func TestPrivateLockFailedRecordLeavesLockFree(t *testing.T) {
	write := writeLockRecord
	t.Cleanup(func() { writeLockRecord = write })
	writeLockRecord = func(*os.File, []byte) error { return errors.New("disk full") }
	dir := t.TempDir()
	if _, err := privateLock(dir, lockTime); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("privateLock = %v, want the write's error", err)
	}
	writeLockRecord = write
	if dirLockHeld(t, dir) {
		t.Error("a failed record write left the lock held")
	}
}

// TestApplyBusyNamesOwner (red-first): a rollout apply refused by a lock a live privateLock holds names its owner.
func TestApplyBusyNamesOwner(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation")
	release, err := privateLock(r.privateDir, lockTime)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s.calls()
	_, err = r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	checkNamesOwner(t, err)
	checkLockRefusal(t, s, r, err)
}
