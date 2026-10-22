package store

import (
	"sync/atomic"
	"testing"
)

// syncSkipped is set by SkipSyncForTests and read by every later Open.
var syncSkipped atomic.Bool

// SkipSyncForTests makes every store this process opens afterwards skip
// bbolt's fsync of each commit, for test binaries whose tests do not test
// durability against power loss. Records, receipts, locking and replay behave
// as they do with sync: a commit is still in the file once it returns, and a
// killed process loses nothing the operating system already holds. Only a
// machine that loses power can lose a commit.
//
// It panics outside a test binary (testing.Testing), so production stores
// always sync every commit. A test binary calls it once from TestMain, before
// any store is opened; the test binary's own children, which run TestMain
// again, skip sync the same way.
func SkipSyncForTests() {
	if !testing.Testing() {
		panic("store: SkipSyncForTests called outside a test binary")
	}
	syncSkipped.Store(true)
}
