package adapter

import (
	"os"
	"sync/atomic"
	"testing"
)

// resultSyncSkipped is set by SkipSyncForTests and read by every later
// WriteResult.
var resultSyncSkipped atomic.Bool

// inTestBinary reports whether this process is a test binary; a test replaces
// it to show SkipSyncForTests refuses outside one.
var inTestBinary = testing.Testing

// syncFile flushes a result's temporary file to disk; a test replaces it to
// count the syncs WriteResult makes.
var syncFile = (*os.File).Sync

// SkipSyncForTests makes every WriteResult this process makes afterwards skip
// the fsync of the result's temporary file, for test binaries whose tests do
// not test durability against power loss, as store.SkipSyncForTests does for
// stores. Everything else is unchanged: the temporary file, the hard link that
// publishes it, the refusal to replace a result and the cancellation checks.
// Only a machine that loses power can lose a result written this way.
//
// It panics outside a test binary (testing.Testing), so production results are
// always synced. A test binary calls it once from TestMain, before any result
// is written.
func SkipSyncForTests() {
	if !inTestBinary() {
		panic("adapter: SkipSyncForTests called outside a test binary")
	}
	resultSyncSkipped.Store(true)
}
