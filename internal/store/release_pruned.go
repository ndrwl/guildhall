package store

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// bucketReleasePrunes is the nested bucket of the director bucket holding the copy's release-pruned events, keyed
// by zero-padded event sequence (docs/store.md#release-prunes). It is additive, so it needs no schema bump.
var bucketReleasePrunes = []byte("release-prunes")

// opRecordReleasePruned is the operation recorded on a release-pruned event's receipt.
const opRecordReleasePruned = "record-release-pruned"

// MaxPrunedReasonBytes bounds a release-pruned event's reason.
const MaxPrunedReasonBytes = 1024

// ReleasePruned is one release-pruned event: `guildhall release prune`, run from this copy, removed the installed
// release Version, whose binary had sha256 SHA256 ("" when a damaged release's could not be read), for Reason, why
// it was eligible. It is never changed.
type ReleasePruned struct {
	// Sequence numbers events from 1 and keys the record; the mutation id is "release-pruned:<sequence>".
	Sequence uint64 `json:"sequence"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Reason   string `json:"reason"`
	// Receipt and Time are set by the store: the event's receipt sequence and time, in UTC.
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

func releasePrunedRecord(seq uint64) string {
	return "director/release-pruned/" + strconv.FormatUint(seq, 10)
}

// RecordReleasePruned records p as the copy's next release-pruned event, under the mutation id
// "release-pruned:<sequence>", where sequence is one more than the newest event's, and returns it. An invalid
// version, a sha256 that is neither empty nor 64 lowercase hex characters, an empty or overlong reason and
// store-set fields are ErrInvalid; two events recorded at once conflict with ErrMutationConflict.
func (s *Store) RecordReleasePruned(p ReleasePruned) (ReleasePruned, error) {
	switch {
	case !validReleaseVersion(p.Version):
		return ReleasePruned{}, invalid("pruned release version %q is not a release version", p.Version)
	case p.Sequence != 0 || p.Receipt != 0 || !p.Time.IsZero():
		return ReleasePruned{}, invalid("a release-pruned event's sequence, receipt and time are set by the store")
	case p.Reason == "":
		return ReleasePruned{}, invalid("a release-pruned event needs the reason the release was eligible")
	}
	if _, err := digest.ParseHex(p.SHA256); p.SHA256 != "" && err != nil {
		return ReleasePruned{}, invalid("pruned release sha256 %q must be 64 lowercase hex characters", p.SHA256)
	}
	if err := validateText("release-pruned reason", p.Reason, MaxPrunedReasonBytes); err != nil {
		return ReleasePruned{}, err
	}
	var last ReleasePruned
	if err := s.view(func(tx *bolt.Tx) error { return lastJSON(directorBucket(tx, bucketReleasePrunes), &last) }); err != nil && !errors.Is(err, ErrNotFound) {
		return ReleasePruned{}, fmt.Errorf("store: latest release-pruned event: %w", err)
	}
	seq := last.Sequence + 1
	p.Sequence = seq
	_, err := s.mutate("release-pruned:"+strconv.FormatUint(seq, 10), opRecordReleasePruned, p, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = releasePrunedRecord(seq)
		b, err := createDirectorBucket(tx, bucketReleasePrunes)
		if err != nil {
			return err
		}
		if b.Get(seqKey(seq)) != nil {
			return fmt.Errorf("%w: release-pruned event %d was recorded meanwhile", ErrMutationConflict, seq)
		}
		p.Receipt, p.Time = r.Sequence, s.now().UTC()
		return putJSON(b, seqKey(seq), p)
	})
	if err != nil {
		return ReleasePruned{}, err
	}
	var out ReleasePruned
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketReleasePrunes), seqKey(seq), &out) }); err != nil {
		return ReleasePruned{}, fmt.Errorf("store: release-pruned event %d: %w", seq, err)
	}
	return out, nil
}

// ReleasePrunes is every release-pruned event of the copy, oldest first.
func (s *Store) ReleasePrunes() ([]ReleasePruned, error) {
	out := []ReleasePruned{}
	if err := s.view(func(tx *bolt.Tx) error { return scan(directorBucket(tx, bucketReleasePrunes), nil, &out) }); err != nil {
		return nil, fmt.Errorf("store: release-pruned events: %w", err)
	}
	return out, nil
}
