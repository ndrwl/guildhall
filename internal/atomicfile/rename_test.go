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
		}, isRace, oldpath, newpath, defaultBounds)
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
		}, func(error) bool { return true }, filepath.Join(root, "missing"), filepath.Join(root, "new"), defaultBounds)
		if elapsed := time.Since(start); !errors.Is(err, fs.ErrNotExist) || calls != 1 || elapsed != 0 {
			t.Fatalf("renameWith of a missing source = %v after %d calls and %v, want fs.ErrNotExist at once", err, calls, elapsed)
		}
	})
}

func TestRenameLastingRaceIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		err := renameWith(func(string, string) error { calls++; return errRace }, isRace, "old", "new", defaultBounds)
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

// deniedFor returns a rename that fails with errRace until d has passed since
// its first call, then renames; calls counts its calls.
func deniedFor(d time.Duration, calls *int) func(string, string) error {
	var first time.Time
	return func(o, n string) error {
		*calls++
		if first.IsZero() {
			first = time.Now()
		}
		if time.Since(first) < d {
			return errRace
		}
		return os.Rename(o, n)
	}
}

// TestRenameWithinOutlastsALongerDenial is the regression test for d238: a
// directory held for longer than the default bound, as a scanner or the
// teardown of a just-run image may hold a release directory on Windows, is
// still renamed when the caller allows longer.
func TestRenameWithinOutlastsALongerDenial(t *testing.T) {
	oldpath, newpath := dirWithFile(t)
	synctest.Test(t, func(t *testing.T) {
		const denial, bound = 400 * time.Millisecond, 2 * time.Second
		start, calls := time.Now(), 0
		err := renameWith(deniedFor(denial, &calls), isRace, oldpath, newpath, within(bound))
		elapsed := time.Since(start)
		if err != nil || elapsed < denial || elapsed > denial+backoffCap {
			t.Fatalf("renameWith denied for %v within %v = %v after %v and %d calls, want success soon after the denial", denial, bound, err, elapsed, calls)
		}
		// Polling backs off from 10 ms to 100 ms, so it calls op far less
		// often than every 10 ms.
		if calls > int(denial/interval)/2 {
			t.Fatalf("renameWith called op %d times over %v, want polling to back off", calls, denial)
		}
	})
	assertMoved(t, oldpath, newpath)
}

func TestRenameWithinIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const bound = 300 * time.Millisecond
		start, calls := time.Now(), 0
		err := renameWith(deniedFor(time.Hour, &calls), isRace, "old", "new", within(bound))
		elapsed := time.Since(start)
		if !errors.Is(err, errRace) || elapsed < bound || elapsed > bound+backoffCap {
			t.Fatalf("renameWith of a lasting race within %v = %v after %v, want the race after about %v", bound, err, elapsed, bound)
		}
	})
}

func TestRenameWithinMissingSourceReturnsAtOnce(t *testing.T) {
	root := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		err := RenameWithin(filepath.Join(root, "missing"), filepath.Join(root, "new"), time.Hour)
		if elapsed := time.Since(start); !errors.Is(err, fs.ErrNotExist) || elapsed != 0 {
			t.Fatalf("RenameWithin of a missing source = %v after %v, want fs.ErrNotExist at once", err, elapsed)
		}
	})
}

func TestRenameWithinDirectory(t *testing.T) {
	oldpath, newpath := dirWithFile(t)
	if err := RenameWithin(oldpath, newpath, time.Second); err != nil {
		t.Fatalf("RenameWithin: %v", err)
	}
	assertMoved(t, oldpath, newpath)
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
