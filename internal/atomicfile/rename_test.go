package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// dirWithFile makes a directory holding one file and returns it with a path
// beside it that does not exist yet.
func dirWithFile(t *testing.T) (oldpath, newpath string) {
	t.Helper()
	root := t.TempDir()
	oldpath = filepath.Join(root, "old")
	if err := os.Mkdir(oldpath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldpath, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return oldpath, filepath.Join(root, "new")
}

// assertMoved checks that the directory dirWithFile made is at newpath and
// not at oldpath.
func assertMoved(t *testing.T, oldpath, newpath string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(newpath, "f")); err != nil || string(b) != "x" {
		t.Fatalf("new path holds %q, %v; want the moved file", b, err)
	}
	if _, err := os.Stat(oldpath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old path after rename: %v, want it gone", err)
	}
}

func TestRenameRetriesTransient(t *testing.T) {
	oldpath, newpath := dirWithFile(t)
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		err := renameWith(func(o, n string) error {
			calls++
			if calls <= 2 {
				return errRace
			}
			return os.Rename(o, n)
		}, isRace, oldpath, newpath)
		if err != nil || calls != 3 {
			t.Fatalf("renameWith = %v after %d calls, want nil after 3", err, calls)
		}
	})
	assertMoved(t, oldpath, newpath)
}

func TestRenameMissingSourceReturnsAtOnce(t *testing.T) {
	root := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		err := renameWith(func(o, n string) error {
			calls++
			return os.Rename(o, n)
		}, func(error) bool { return true }, filepath.Join(root, "missing"), filepath.Join(root, "new"))
		if elapsed := time.Since(start); !errors.Is(err, fs.ErrNotExist) || calls != 1 || elapsed != 0 {
			t.Fatalf("renameWith of a missing source = %v after %d calls and %v, want fs.ErrNotExist at once", err, calls, elapsed)
		}
	})
}

func TestRenameLastingRaceIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		err := renameWith(func(string, string) error { calls++; return errRace }, isRace, "old", "new")
		elapsed := time.Since(start)
		if !errors.Is(err, errRace) || elapsed < patience || elapsed > patience+time.Second {
			t.Fatalf("renameWith of a lasting race = %v after %v, want the race after about %v", err, elapsed, patience)
		}
		// The first call and the call exactly at the deadline precede expiry.
		if max := int(patience/interval) + 2; calls < 2 || calls > max {
			t.Fatalf("renameWith called op %d times, want between 2 and %d", calls, max)
		}
	})
}

func TestRenameDirectory(t *testing.T) {
	oldpath, newpath := dirWithFile(t)
	if err := Rename(oldpath, newpath); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	assertMoved(t, oldpath, newpath)
	if err := Rename(oldpath, newpath+"2"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Rename of a missing source = %v, want fs.ErrNotExist", err)
	}
}
