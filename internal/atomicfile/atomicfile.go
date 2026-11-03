// Package atomicfile publishes a file by renaming a complete copy over it and
// reads one that may be republished at the same instant. Rename moves a file
// or a directory with the same retry, for a rename that may meet a handle
// another process still holds.
//
// A reader sees no file, the whole earlier content or the whole later
// content, never a torn one. An open or rename that fails for a transient
// platform reason while another process renames or reads the same name is
// retried every 10 ms for up to 250 ms, and only an error that outlasts that,
// or any other error, is returned. On Windows the transient errors are
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

// Bounds of the retry.
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
	if err := retry(func() error { return os.Rename(tmp.Name(), path) }, transient); err != nil {
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
	}, transient)
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
	return renameWith(os.Rename, transient, oldpath, newpath)
}

// renameWith is Rename with its rename and its transient test supplied.
func renameWith(op func(string, string) error, isTransient func(error) bool, oldpath, newpath string) error {
	return retry(func() error { return op(oldpath, newpath) }, func(err error) bool {
		return !errors.Is(err, fs.ErrNotExist) && isTransient(err)
	})
}

// retry runs op until it succeeds, fails for a reason isTransient refuses, or
// patience has elapsed since the first call.
func retry(op func() error, isTransient func(error) bool) error {
	deadline := time.Now().Add(patience)
	for {
		err := op()
		if err == nil || !isTransient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(interval)
	}
}
