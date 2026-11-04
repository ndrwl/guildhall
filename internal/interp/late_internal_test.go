package interp

import (
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestLateMessage: only a message with a send time strictly after a published result's cutoff is late. A message
// with no send time, stored before send times were recorded, or sent at the cutoff, and every message while no
// result is published, count against the result.
func TestLateMessage(t *testing.T) {
	cutoff := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		sent   time.Time
		cutoff time.Time
		late   bool
	}{
		{"after the cutoff", cutoff.Add(time.Nanosecond), cutoff, true},
		{"at the cutoff", cutoff, cutoff, false},
		{"before the cutoff", cutoff.Add(-time.Second), cutoff, false},
		{"no send time", time.Time{}, cutoff, false},
		{"no result published", cutoff.Add(time.Hour), time.Time{}, false},
	} {
		if got := late(store.Message{ID: "m", SentAt: c.sent}, c.cutoff); got != c.late {
			t.Errorf("%s: late = %v, want %v", c.name, got, c.late)
		}
	}
}
