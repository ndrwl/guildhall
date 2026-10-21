package atomicfile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

func TestWriteReplacesAndLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result")
	for _, body := range []string{"first\n", "second\n"} {
		if err := atomicfile.Write(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if got, err := atomicfile.Read(path); err != nil || string(got) != body {
			t.Fatalf("Read = %q, %v; want %q", got, err, body)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the published file", len(entries))
	}
	if err := atomicfile.Write(filepath.Join(dir, "missing", "result"), nil); err == nil {
		t.Fatal("Write into a missing directory succeeded")
	}
}

func TestReadMissingIsImmediate(t *testing.T) {
	start := time.Now()
	_, err := atomicfile.Read(filepath.Join(t.TempDir(), "result"))
	if !errors.Is(err, fs.ErrNotExist) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("Read of a missing file = %v after %v, want fs.ErrNotExist at once", err, time.Since(start))
	}
}

// Each concurrent read/write pair finishes before the next starts, so Windows
// renames face one bounded read rather than near-continuous contention.
// See docs/contracts/atomicfile.md for the contract and test expectations.
func TestReadDuringWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	bodies := []string{"first version\n", "second version, longer\n"}
	if err := atomicfile.Write(path, []byte(bodies[0])); err != nil {
		t.Fatal(err)
	}
	for i := range 128 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := atomicfile.Write(path, []byte(bodies[i%2])); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			got, err := atomicfile.Read(path)
			if err != nil {
				t.Errorf("Read: %v", err)
				return
			}
			if s := string(got); s != bodies[0] && s != bodies[1] {
				t.Errorf("Read = %q, a torn version", s)
			}
		}()
		wg.Wait()
		if t.Failed() {
			return
		}
	}
}
