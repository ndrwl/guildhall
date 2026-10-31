package store

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// bucketReleasePins is the nested bucket of the director bucket holding the copy's release-pin events, keyed by
// zero-padded event sequence (docs/store.md#release-pins). It is additive, so it needs no schema bump.
var bucketReleasePins = []byte("release-pins")

// opRecordReleasePin is the operation recorded on a release-pin event's receipt.
const opRecordReleasePin = "record-release-pin"

// ReleasePinSource is the route by which a pin change was made (docs/releases.md#the-pin).
type ReleasePinSource string

const (
	// PinAdopt is `guildhall release adopt`, the copy's first pin.
	PinAdopt ReleasePinSource = "adopt"
	// PinUse is `guildhall release use`.
	PinUse ReleasePinSource = "use"
	// PinReload is a managed `config reload` applying a revision's release.
	PinReload ReleasePinSource = "reload"
	// PinRollback is `guildhall release rollback`.
	PinRollback ReleasePinSource = "rollback"
	// PinGuildmasterApproved is a managed pin the Guildmaster approved by hand.
	PinGuildmasterApproved ReleasePinSource = "guildmaster-approved"
)

func (s ReleasePinSource) valid() bool {
	switch s {
	case PinAdopt, PinUse, PinReload, PinRollback, PinGuildmasterApproved:
		return true
	}
	return false
}

// ReleasePin is one release-pin event: the copy's pin moved to Version, with sha256 SHA256, from Previous (both ""
// for the copy's first pin). It is never changed.
type ReleasePin struct {
	// Sequence numbers events from 1 and keys the record; the mutation id is "release-pin:<sequence>".
	Sequence        uint64           `json:"sequence"`
	Version         string           `json:"version"`
	SHA256          string           `json:"sha256"`
	PreviousVersion string           `json:"previous_version"`
	PreviousSHA256  string           `json:"previous_sha256"`
	Source          ReleasePinSource `json:"source"`
	// Receipt and Time are set by the store: the event's receipt sequence and time, in UTC.
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// releaseVersionPattern is a release version as internal/release admits one, so a store event never names a
// version no release directory could have.
var releaseVersionPattern = regexp.MustCompile(`^v[0-9A-Za-z][0-9A-Za-z.+-]*$`)

func validReleaseVersion(v string) bool {
	return v != "dev" && releaseVersionPattern.MatchString(v) && len(v) <= 128 && !strings.Contains(v, "..")
}

func releasePinRecord(seq uint64) string {
	return "director/release-pin/" + strconv.FormatUint(seq, 10)
}

// RecordReleasePin records p as the copy's next release-pin event, under the mutation id "release-pin:<sequence>",
// where sequence is one more than the newest event's, and returns it with its receipt. Its previous version and
// sha256 are the newest event's, set by the store: a pin given one is ErrInvalid unless it matches. A pin to the
// version and sha256 already newest is ErrInvalid, as nothing would change; so are an invalid version, a sha256 that
// is not 64 lowercase hex characters, an unknown source and store-set fields. Two events recorded at once conflict
// with ErrMutationConflict.
func (s *Store) RecordReleasePin(p ReleasePin) (ReleasePin, Receipt, error) {
	switch {
	case !validReleaseVersion(p.Version):
		return ReleasePin{}, Receipt{}, invalid("release pin version %q is not a release version", p.Version)
	case !p.Source.valid():
		return ReleasePin{}, Receipt{}, invalid("release pin source %q is not adopt, use, reload, rollback or guildmaster-approved", p.Source)
	case p.Sequence != 0 || p.Receipt != 0 || !p.Time.IsZero():
		return ReleasePin{}, Receipt{}, invalid("a release pin's sequence, receipt and time are set by the store")
	}
	if _, err := digest.ParseHex(p.SHA256); err != nil {
		return ReleasePin{}, Receipt{}, invalid("release pin sha256 %q must be 64 lowercase hex characters", p.SHA256)
	}
	last, err := s.LatestReleasePin()
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return ReleasePin{}, Receipt{}, err
	}
	if (p.PreviousVersion != "" || p.PreviousSHA256 != "") && (p.PreviousVersion != last.Version || p.PreviousSHA256 != last.SHA256) {
		return ReleasePin{}, Receipt{}, fmt.Errorf("%w: the pin moved from %s since it was read, not %s", ErrMutationConflict, last.Version, p.PreviousVersion)
	}
	if last.Sequence != 0 && last.Version == p.Version && last.SHA256 == p.SHA256 {
		return ReleasePin{}, Receipt{}, invalid("the pin is already %s", p.Version)
	}
	seq := last.Sequence + 1
	p.Sequence, p.PreviousVersion, p.PreviousSHA256 = seq, last.Version, last.SHA256
	r, err := s.mutate("release-pin:"+strconv.FormatUint(seq, 10), opRecordReleasePin, p, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = releasePinRecord(seq)
		pins, err := createDirectorBucket(tx, bucketReleasePins)
		if err != nil {
			return err
		}
		if pins.Get(seqKey(seq)) != nil {
			return fmt.Errorf("%w: release pin %d was recorded meanwhile", ErrMutationConflict, seq)
		}
		p.Receipt, p.Time = r.Sequence, s.now().UTC()
		return putJSON(pins, seqKey(seq), p)
	})
	if err != nil {
		return ReleasePin{}, Receipt{}, err
	}
	var out ReleasePin
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketReleasePins), seqKey(seq), &out) }); err != nil {
		return ReleasePin{}, Receipt{}, fmt.Errorf("store: release pin %d: %w", seq, err)
	}
	return out, r, nil
}

// LatestReleasePin is the copy's newest release-pin event, or ErrNotFound when it has none.
func (s *Store) LatestReleasePin() (ReleasePin, error) {
	var p ReleasePin
	if err := s.view(func(tx *bolt.Tx) error { return lastJSON(directorBucket(tx, bucketReleasePins), &p) }); err != nil {
		return ReleasePin{}, fmt.Errorf("store: latest release pin: %w", err)
	}
	return p, nil
}

// ReleasePins is every release-pin event of the copy, oldest first.
func (s *Store) ReleasePins() ([]ReleasePin, error) {
	out := []ReleasePin{}
	if err := s.view(func(tx *bolt.Tx) error { return scan(directorBucket(tx, bucketReleasePins), nil, &out) }); err != nil {
		return nil, fmt.Errorf("store: release pins: %w", err)
	}
	return out, nil
}
