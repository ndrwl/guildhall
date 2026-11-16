package artisan_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// The artisan locks are thin wrappers over internal/filelock, whose own tests cover the lock; these check that each
// wrapper takes and releases it.

func TestLockPathAtLocksAtTheOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	f, err := artisan.LockPathAt(context.Background(), path, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("owner record\n"), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // timing:allow a refused taker is expected to wait out this bound.
	second, err := artisan.LockPathAt(ctx, path, 1<<30)
	cancel()
	if err == nil {
		_ = second.Close()
		t.Fatal("second LockPathAt took a held lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second LockPathAt = %v, want the deadline", err)
	}
	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(b) != "owner record\n" {
		t.Errorf("another handle read %q, %v; want the holder's record", b, err)
	}
	_ = f.Close()
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
