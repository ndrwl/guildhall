package testenv

import (
	"testing"
	"time"
)

// WaitFor checks ready immediately and then polls until it succeeds, failing
// with what it waited for after a CI-scaled 30-second safety ceiling.
// The polling interval controls cost, never whether a test passes.
// Prefer a channel when the producer can signal the event directly.
func WaitFor(t testing.TB, what string, ready func() bool) {
	t.Helper()
	end := time.Now().Add(Ceiling(30 * time.Second))
	for ; !ready(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
