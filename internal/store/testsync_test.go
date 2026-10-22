package store

import (
	"path/filepath"
	"testing"
)

// withSyncSkipped runs the test with SkipSyncForTests's switch set to skip and
// restores the switch afterwards.
func withSyncSkipped(t *testing.T, skip bool) {
	t.Helper()
	was := syncSkipped.Load()
	syncSkipped.Store(skip)
	t.Cleanup(func() { syncSkipped.Store(was) })
}

// syncedReopen writes a task, closes the store and reads the task back from a
// fresh open, returning whether the first open synced its commits.
func syncedReopen(t *testing.T) bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s, err := Open(path, testOptions())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	synced := !s.db.NoSync
	mustCreate(t, s, "m1", sampleTask("repo", "t1"))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := openTest(t, path)
	if _, err := r.Task("repo", "t1"); err != nil {
		t.Fatalf("Task after reopen: %v", err)
	}
	return synced
}

func TestOpenSyncsEveryCommitByDefault(t *testing.T) {
	withSyncSkipped(t, false)
	if !syncedReopen(t) {
		t.Fatal("a store opened without SkipSyncForTests skips fsync")
	}
}

func TestSkipSyncForTestsSkipsFsync(t *testing.T) {
	withSyncSkipped(t, false)
	SkipSyncForTests()
	if !syncSkipped.Load() {
		t.Fatal("SkipSyncForTests did not set the switch")
	}
	if syncedReopen(t) {
		t.Fatal("a store opened after SkipSyncForTests still fsyncs every commit")
	}
}
