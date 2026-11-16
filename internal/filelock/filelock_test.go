package filelock_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/filelock"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// The file lock on real files: LockPath holds a lock at an offset that leaves the file's content readable through
// other handles.

const highOffset = 1 << 30

// refused checks that a second taker of path is refused within a short bound, by the context's deadline.
func refused(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // timing:allow a refused taker is expected to wait out this bound.
	defer cancel()
	f, err := filelock.LockPath(ctx, path, highOffset)
	if err == nil {
		_ = f.Close()
		t.Fatal("a second handle took a held lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second taker = %v, want the deadline", err)
	}
}

// taken checks that path's lock is free: a taker gets it at once.
func taken(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
	defer cancel()
	f, err := filelock.LockPath(ctx, path, highOffset)
	if err != nil {
		t.Fatalf("taking a free lock: %v", err)
	}
	_ = f.Close()
}

// open opens path for locking, as a second handle would.
func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestLockPathExcludesASecondHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	f, err := filelock.LockPath(context.Background(), path, highOffset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("owner record\n"), 0); err != nil {
		t.Fatal(err)
	}
	refused(t, path)

	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(b) != "owner record\n" {
		t.Errorf("another handle read %q, %v; want the holder's record", b, err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	taken(t, path)
}

func TestLockPathKeepsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(path, []byte("left over"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := filelock.LockPath(context.Background(), path, highOffset)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "left over" {
		t.Errorf("after locking the file is %q, %v; want it untruncated", b, err)
	}
}

// A caller whose ctx is already done is refused before the lock file is opened, so it neither takes a free lock nor
// creates the file.
func TestLockPathCancelledCtxDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, err := filelock.LockPath(ctx, path, highOffset)
	if err == nil {
		_ = f.Close()
		t.Fatal("a cancelled ctx took the lock")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("LockPath with a cancelled ctx = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after a cancelled LockPath the lock file stat = %v, want it absent", err)
	}
}

// Lock on an open file always makes one attempt, so a ctx already done still takes a free lock, and refuses a held
// one with the ctx's error.
func TestLockDoneCtxTriesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := open(t, path)
	if err := filelock.Lock(ctx, first, highOffset); err != nil {
		t.Fatalf("Lock with a done ctx on a free lock = %v, want the lock", err)
	}
	second := open(t, path)
	if err := filelock.Lock(ctx, second, highOffset); !errors.Is(err, context.Canceled) {
		t.Errorf("Lock with a done ctx on a held lock = %v, want context.Canceled", err)
	}
}

// Unlock frees the lock while the holder's file stays open, so a second handle takes it at once.
func TestUnlockFreesTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first := open(t, path)
	if err := filelock.Lock(context.Background(), first, highOffset); err != nil {
		t.Fatal(err)
	}
	refused(t, path)
	if err := filelock.Unlock(first, highOffset); err != nil {
		t.Fatalf("Unlock = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := open(t, path)
	if err := filelock.Lock(ctx, second, highOffset); err != nil {
		t.Errorf("Lock after Unlock = %v, want the lock at once", err)
	}
}

// Wait blocks while another handle holds the lock and takes it once the holder closes its file.
func TestWaitBlocksUntilTheHolderCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	holder, err := filelock.LockPath(context.Background(), path, highOffset)
	if err != nil {
		t.Fatal(err)
	}
	waiter := open(t, path)
	done := make(chan error, 1)
	go func() { done <- filelock.Wait(waiter, highOffset) }()
	select {
	case err := <-done:
		t.Fatalf("Wait returned %v while the lock was held", err)
	case <-time.After(50 * time.Millisecond): // timing:allow a blocked waiter is expected to still be waiting after this bound.
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after the holder closed = %v", err)
		}
	case <-time.After(testenv.Ceiling(30 * time.Second)):
		t.Fatal("Wait still blocked after the holder closed")
	}
	refused(t, path)
}
