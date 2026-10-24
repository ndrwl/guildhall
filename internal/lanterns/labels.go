package lanterns

import (
	"time"
)

// Labels is the vitals-axi v1 identity file. Linux is the only platform on which
// that consumer supports verified process selectors.
type Labels struct {
	SchemaVersion int       `json:"schemaVersion"`
	BootID        string    `json:"bootId"`
	PIDNamespace  string    `json:"pidNamespace"`
	GeneratedAt   time.Time `json:"generatedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Entries       []Entry   `json:"entries"`
}

// Entry names one verified generation, not authority to control it.
type Entry struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Source   string   `json:"source"`
	Selector Selector `json:"selector"`
}

// Selector anchors a label and its descendants to a PID and kernel start ticks.
type Selector struct {
	Kind        string `json:"kind"`
	PID         int    `json:"pid"`
	StartTicks  string `json:"startTicks"`
	Descendants bool   `json:"descendants"`
}
