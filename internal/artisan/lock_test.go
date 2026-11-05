package artisan_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// The artisan locks on real files: LockPathAt holds a lock at an offset that leaves the file's content readable
// through other handles, and LockPath is LockPathAt at offset 0.

const highOffset = 1 << 30

// refused checks that a second taker of path is refused within a short bound, by the context's deadline.
func refused(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // timing:allow a refused taker is expected to wait out this bound.
	defer cancel()
	f, err := artisan.LockPathAt(ctx, path, highOffset)
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
	f, err := artisan.LockPathAt(ctx, path, highOffset)
	if err != nil {
		t.Fatalf("taking a free lock: %v", err)
	}
	_ = f.Close()
}

func TestLockPathAtExcludesASecondHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	f, err := artisan.LockPathAt(context.Background(), path, highOffset)
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

func TestLockPathAtKeepsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(path, []byte("left over"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := artisan.LockPathAt(context.Background(), path, highOffset)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "left over" {
		t.Errorf("after locking the file is %q, %v; want it untruncated", b, err)
	}
}

func TestLockPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	release, err := artisan.LockPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // timing:allow a refused taker is expected to wait out this bound.
	_, err = artisan.LockPath(ctx, path)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second LockPath = %v, want the deadline", err)
	}
	release()
	release2, err := artisan.LockPath(context.Background(), path)
	if err != nil {
		t.Fatalf("LockPath after release: %v", err)
	}
	release2()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("release removed the file: %v", err)
	}
}

func TestLockAttempt(t *testing.T) {
	dir := t.TempDir()
	release, err := artisan.LockAttempt(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // timing:allow a refused taker is expected to wait out this bound.
	_, err = artisan.LockPath(ctx, filepath.Join(dir, artisan.LockFile))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("LockPath on a held attempt lock = %v, want the deadline", err)
	}
	release()
}

// A caller whose ctx is already done is refused before the lock file is opened, so it neither takes a free lock nor
// creates the file.
func TestLockPathAtCancelledCtxDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, err := artisan.LockPathAt(ctx, path, highOffset)
	if err == nil {
		_ = f.Close()
		t.Fatal("a cancelled ctx took the lock")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("LockPathAt with a cancelled ctx = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after a cancelled LockPathAt the lock file stat = %v, want it absent", err)
	}
}
