package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestReleasePins: each pin event records the version, sha256 and source with the previous event's version and
// sha256, under the next sequence; a pin to the newest again, a stale previous, and invalid fields are refused, and
// store doctor decodes the events.
func TestReleasePins(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.LatestReleasePin(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest pin of a new store: %v, want ErrNotFound", err)
	}
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	first, r, err := s.RecordReleasePin(ReleasePin{Version: "v1.0.0", SHA256: a, Source: PinAdopt})
	if err != nil || first.Sequence != 1 || first.PreviousVersion != "" || first.Receipt != r.Sequence || first.Time.IsZero() || r.MutationID != "release-pin:1" {
		t.Fatalf("first pin %+v, receipt %+v, %v", first, r, err)
	}
	second, _, err := s.RecordReleasePin(ReleasePin{Version: "v1.1.0", SHA256: b, Source: PinUse})
	if err != nil || second.Sequence != 2 || second.PreviousVersion != "v1.0.0" || second.PreviousSHA256 != a || second.Source != PinUse {
		t.Fatalf("second pin %+v, %v", second, err)
	}
	for name, p := range map[string]ReleasePin{
		"the newest again": {Version: "v1.1.0", SHA256: b, Source: PinUse},
		"dev":              {Version: "dev", SHA256: a, Source: PinUse},
		"not a version":    {Version: "1.0", SHA256: a, Source: PinUse},
		"a path":           {Version: "v1/../x", SHA256: a, Source: PinUse},
		"upper-case sha":   {Version: "v2.0.0", SHA256: strings.Repeat("A", 64), Source: PinUse},
		"unknown source":   {Version: "v2.0.0", SHA256: a, Source: "steward"},
		"store-set fields": {Version: "v2.0.0", SHA256: a, Source: PinUse, Time: time.Now()},
	} {
		if _, _, err := s.RecordReleasePin(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, _, err := s.RecordReleasePin(ReleasePin{Version: "v2.0.0", SHA256: a, Source: PinRollback, PreviousVersion: "v1.0.0", PreviousSHA256: a}); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("stale previous: %v, want ErrMutationConflict", err)
	}
	pins, err := s.ReleasePins()
	if err != nil || len(pins) != 2 || pins[0] != first || pins[1] != second {
		t.Fatalf("pins %+v, %v", pins, err)
	}
	if v, err := s.ManagedView(); err != nil || v.ReleasePin == nil || *v.ReleasePin != second {
		t.Fatalf("managed view pin %+v, %v", v.ReleasePin, err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor %+v, %v", rep, err)
	}
}
