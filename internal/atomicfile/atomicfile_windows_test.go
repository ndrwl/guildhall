package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTransientWindows(t *testing.T) {
	for err, want := range map[error]bool{
		&fs.PathError{Op: "open", Path: "result", Err: windows.ERROR_SHARING_VIOLATION}:          true,
		&os.LinkError{Op: "rename", Old: "tmp", New: "result", Err: windows.ERROR_ACCESS_DENIED}: true,
		&fs.PathError{Op: "open", Path: "result", Err: windows.ERROR_FILE_NOT_FOUND}:             false,
	} {
		if got := transient(err); got != want {
			t.Errorf("transient(%v) = %v, want %v", err, got, want)
		}
	}
}

// holdExclusive opens path sharing nothing, as a rename in progress does,
// and releases it after d.
func holdExclusive(t *testing.T, path string, d time.Duration) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(d, func() { _ = windows.CloseHandle(h) })
}

func TestReadWaitsForExclusiveHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, []byte("whole\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	holdExclusive(t, path, 50*time.Millisecond)
	if got, err := Read(path); err != nil || string(got) != "whole\n" {
		t.Fatalf("Read while held = %q, %v; want the content once released", got, err)
	}
}

func TestWriteWaitsForReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, func() { _ = f.Close() })
	if err := Write(path, []byte("new\n")); err != nil {
		t.Fatalf("Write while a reader holds the file: %v", err)
	}
}
