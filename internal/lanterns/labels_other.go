//go:build !linux

package lanterns

import (
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// PublishLabels is a no-op outside Linux: vitals-axi v1 selectors require Linux
// boot IDs, PID namespaces and /proc start ticks. No PID-only labels are emitted.
func PublishLabels(_ string, _ store.DirectorView, _ time.Time) error { return nil }
