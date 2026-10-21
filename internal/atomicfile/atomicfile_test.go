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

// TestReadDuringWrite republishes one file for a second while another
// goroutine reads it: every read and every write succeeds and sees a whole
// version. On Windows a plain open and rename fail here with sharing
// violations and access denied. The reader closes and pauses 1 ms between
// reads, holding the file about 1% of the time, so a republish gets its
// 250 ms bound; a reader that never pauses would test the platform instead.
func TestReadDuringWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result")
	bodies := []string{"first version\n", "second version, longer\n"}
	if err := atomicfile.Write(path, []byte(bodies[0])); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	var writes int
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ; time.Now().Before(deadline); writes++ {
			if err := atomicfile.Write(path, []byte(bodies[writes%2])); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			got, err := atomicfile.Read(path)
			if err != nil {
				t.Errorf("Read: %v", err)
				return
			}
			if s := string(got); s != bodies[0] && s != bodies[1] {
				t.Errorf("Read = %q, a torn version", s)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	if writes <= 100 {
		t.Errorf("writer republished %d times in a second, want more than 100", writes)
	}
}
