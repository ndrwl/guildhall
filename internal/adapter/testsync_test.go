package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// withResultSyncSkipped runs the test with SkipSyncForTests's switch set to
// skip and counts the syncs WriteResult makes, restoring both afterwards.
func withResultSyncSkipped(t *testing.T, skip bool) *int {
	t.Helper()
	was, sync := resultSyncSkipped.Load(), syncFile
	resultSyncSkipped.Store(skip)
	n := new(int)
	syncFile = func(f *os.File) error {
		*n++
		return sync(f)
	}
	t.Cleanup(func() {
		resultSyncSkipped.Store(was)
		syncFile = sync
	})
	return n
}

// writeTestResult writes a result to a fresh path and reads it back.
func writeTestResult(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "result.json")
	if err := WriteResult(path, Result{Identity: testID, Status: StatusSucceeded, Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadResult(path, testID); err != nil || r.Summary != "done" {
		t.Fatalf("ReadResult = %+v, %v", r, err)
	}
}

func TestWriteResultSyncsByDefault(t *testing.T) {
	n := withResultSyncSkipped(t, false)
	writeTestResult(t)
	if *n != 1 {
		t.Fatalf("WriteResult synced %d times without SkipSyncForTests, want 1", *n)
	}
}

func TestSkipSyncForTestsSkipsResultFsync(t *testing.T) {
	n := withResultSyncSkipped(t, false)
	SkipSyncForTests()
	if !resultSyncSkipped.Load() {
		t.Fatal("SkipSyncForTests did not set the switch")
	}
	writeTestResult(t)
	if *n != 0 {
		t.Fatalf("WriteResult synced %d times after SkipSyncForTests, want 0", *n)
	}
}

func TestSkipSyncForTestsPanicsOutsideATestBinary(t *testing.T) {
	withResultSyncSkipped(t, false)
	was := inTestBinary
	inTestBinary = func() bool { return false }
	t.Cleanup(func() { inTestBinary = was })
	defer func() {
		if recover() == nil {
			t.Error("SkipSyncForTests outside a test binary did not panic")
		}
		if resultSyncSkipped.Load() {
			t.Error("SkipSyncForTests outside a test binary set the switch")
		}
	}()
	SkipSyncForTests()
}
