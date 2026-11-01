package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestReleasePrunes: each release-pruned event records the version, sha256 and reason under the next sequence; a
// damaged release's empty sha256 is allowed, invalid fields are refused, and store doctor decodes the events.
func TestReleasePrunes(t *testing.T) {
	s, _ := newStore(t)
	if got, err := s.ReleasePrunes(); err != nil || len(got) != 0 {
		t.Fatalf("prunes of a new store: %+v, %v", got, err)
	}
	a := strings.Repeat("a", 64)
	first, err := s.RecordReleasePruned(ReleasePruned{Version: "v1.0.0", SHA256: a, Reason: "older than the newest three"})
	if err != nil || first.Sequence != 1 || first.Receipt == 0 || first.Time.IsZero() || first.SHA256 != a {
		t.Fatalf("first prune %+v, %v", first, err)
	}
	second, err := s.RecordReleasePruned(ReleasePruned{Version: "v1.1.0", Reason: "damaged"})
	if err != nil || second.Sequence != 2 || second.SHA256 != "" {
		t.Fatalf("second prune %+v, %v", second, err)
	}
	for name, p := range map[string]ReleasePruned{
		"dev":              {Version: "dev", SHA256: a, Reason: "x"},
		"a path":           {Version: "v1/../x", SHA256: a, Reason: "x"},
		"upper-case sha":   {Version: "v2.0.0", SHA256: strings.Repeat("A", 64), Reason: "x"},
		"no reason":        {Version: "v2.0.0", SHA256: a},
		"long reason":      {Version: "v2.0.0", SHA256: a, Reason: strings.Repeat("x", MaxPrunedReasonBytes+1)},
		"store-set fields": {Version: "v2.0.0", SHA256: a, Reason: "x", Time: time.Now()},
	} {
		if _, err := s.RecordReleasePruned(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	got, err := s.ReleasePrunes()
	if err != nil || len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("prunes %+v, %v", got, err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor %+v, %v", rep, err)
	}
}
