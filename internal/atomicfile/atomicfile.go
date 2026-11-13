// Package atomicfile publishes a file by renaming a complete copy over it and
// reads one that may be republished at the same instant. Rename moves a file
// or a directory with the same retry, for a rename that may meet a handle
// another process still holds.
//
// A reader sees no file, the whole earlier content or the whole later
// content, never a torn one. An open or rename that fails for a transient
// platform reason while another process renames or reads the same name is
// retried every 10 ms for up to 250 ms, and only an error that outlasts that,
// or any other error, is returned. RenameWithin takes a longer bound from its
// caller. On Windows the transient errors are
// ERROR_SHARING_VIOLATION and ERROR_ACCESS_DENIED; on other platforms a
// rename never races an open, so none is. A missing file is never retried:
// fs.ErrNotExist is an answer, not a race.
//
// See docs/contracts/atomicfile.md.
package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Default bounds of the retry; interval is also where RenameWithin's polling
// starts.
const (
	patience = 250 * time.Millisecond
	interval = 10 * time.Millisecond
)

// Write replaces path with data: it writes a temporary file beside path,
// mode 0600 and not synced, then renames it over path. On failure the
// temporary file is removed and any earlier file at path is unchanged.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := retry(func() error { return os.Rename(tmp.Name(), path) }, transient, defaultBounds); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Open opens path for reading.
func Open(path string) (*os.File, error) {
	var f *os.File
	err := retry(func() (err error) {
		f, err = os.Open(path)
		return err
	}, transient, defaultBounds)
	return f, err
}

// Read returns the content of path.
func Read(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	// The file was opened read-only, so a close error loses nothing.
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// Rename renames oldpath to newpath, a file or a directory, retrying a
// transient failure within the same bounds as Write. A missing oldpath is
// returned at once. It takes no lock.
func Rename(oldpath, newpath string) error {
	return renameWith(os.Rename, transient, oldpath, newpath, defaultBounds)
}

// RenameWithin is Rename with a bound the caller supplies: a transient failure
// is retried until patience has elapsed, polling every 10 ms at first and
// backing off, doubling, to every 100 ms. A missing oldpath is returned at
// once. It is for a rename that may meet a handle held for longer than the
// default bound, such as a directory whose files a scanner is reading.
func RenameWithin(oldpath, newpath string, patience time.Duration) error {
	return renameWith(os.Rename, transient, oldpath, newpath, within(patience))
}

// bounds is how long retry keeps trying and how often it polls: from
// interval, doubling up to maxInterval.
type bounds struct{ patience, maxInterval time.Duration }

// backoffCap is the longest wait between RenameWithin's tries.
const backoffCap = 100 * time.Millisecond

// defaultBounds are the bounds of Write, Open, Read and Rename: every 10 ms for
// up to 250 ms.
var defaultBounds = bounds{patience: patience, maxInterval: interval}

// within is the bounds of RenameWithin with patience p.
func within(p time.Duration) bounds { return bounds{patience: p, maxInterval: backoffCap} }

// renameWith is Rename with its rename, its transient test and its bounds
// supplied.
func renameWith(op func(string, string) error, isTransient func(error) bool, oldpath, newpath string, b bounds) error {
	return retry(func() error { return op(oldpath, newpath) }, func(err error) bool {
		return !errors.Is(err, fs.ErrNotExist) && isTransient(err)
	}, b)
}

// retry runs op until it succeeds, fails for a reason isTransient refuses, or
// b.patience has elapsed since the first call. It waits interval after the
// first failure, doubling the wait each time up to b.maxInterval.
func retry(op func() error, isTransient func(error) bool, b bounds) error {
	deadline := time.Now().Add(b.patience)
	wait := interval
	for {
		err := op()
		if err == nil || !isTransient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, max(b.maxInterval, interval))
	}
}
