package store

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Nested buckets of the director bucket holding a Director copy's management mode (docs/director-modes.md): its
// enrolment events and its managed-revision events, each keyed by zero-padded event sequence
// (docs/store.md#managed-mode). Both are created by their first event, so an existing schema 1 file accepts them
// unchanged.
var (
	bucketEnrolments       = []byte("enrolments")
	bucketManagedRevisions = []byte("managed-revisions")
)

// Operations recorded on managed-mode receipts.
const (
	opRecordEnrolment       = "record-enrolment"
	opRecordManagedRevision = "record-managed-revision"
)

// EnrolmentEvent is what one enrolment event records: the copy enrolled with a Steward, or left it.
type EnrolmentEvent string

const (
	// Enrolled is `guildhall director enrol`: the copy is managed by the named Steward.
	Enrolled EnrolmentEvent = "enrolled"
	// Left is `guildhall director leave`: the copy is standalone again.
	Left EnrolmentEvent = "left"
)

// revisionNamePattern is the name a left event gives the managed.toml it kept: managed.toml.rev-<first 16 hex
// digits of its sha256>, as a rollout keeps a revision.
var revisionNamePattern = regexp.MustCompile(`^managed\.toml\.rev-[0-9a-f]{16}$`)

// RevisionName is the name managed.toml with digest d is kept under when the copy leaves managed mode.
func RevisionName(d digest.Digest) string { return "managed.toml.rev-" + d.Hex()[:16] }

// Enrolment is one enrolment event. Events are ordered by sequence and never changed; the newest decides the
// copy's mode (docs/director-modes.md#mode).
type Enrolment struct {
	// Sequence numbers events from 1 and keys the record.
	Sequence uint64         `json:"sequence"`
	Event    EnrolmentEvent `json:"event"`
	// Steward is the label of the Steward an enrolled event enrolled with; a left event names none.
	Steward string `json:"steward,omitempty"`
	// Digest is the managed.toml digest: the one enrolled with, or the one a left event kept.
	Digest string `json:"digest"`
	// Revision is the name a left event kept managed.toml under (RevisionName); an enrolled event has none.
	Revision string `json:"revision,omitempty"`
	// Receipt is the sequence of the event's receipt and Time its time in UTC; both are set by the store.
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// same reports whether e records the same event as o, by what its caller gives.
func (e Enrolment) same(o Enrolment) bool {
	return e.Event == o.Event && e.Steward == o.Steward && e.Digest == o.Digest && e.Revision == o.Revision
}

// mutationID is e's content-derived mutation id: "director-enrol:<sequence>:<steward>:<16 hex>" or
// "director-leave:<sequence>:<16 hex>", so a retry of the same event replays it and a different one conflicts.
func (e Enrolment) mutationID() string {
	short := digest.Digest(e.Digest).Hex()[:16]
	seq := strconv.FormatUint(e.Sequence, 10)
	if e.Event == Enrolled {
		return "director-enrol:" + seq + ":" + e.Steward + ":" + short
	}
	return "director-leave:" + seq + ":" + short
}

// RevisionSource is how a managed-revision event came to be recorded.
type RevisionSource string

const (
	// SourceReload is `guildhall config reload`, run by a rollout over SSH or by hand: it authorizes the digest.
	SourceReload RevisionSource = "reload"
	// SourceObservedUntrusted is a digest the copy found on disk with no authorization; it authorizes nothing.
	SourceObservedUntrusted RevisionSource = "observed-untrusted"
	// SourceGuildmasterApproved is the Guildmaster's approval of an observed digest through its alert decision: it
	// authorizes the digest.
	SourceGuildmasterApproved RevisionSource = "guildmaster-approved"
)

// Authorizes reports whether a revision event of this source authorizes its digest.
func (s RevisionSource) Authorizes() bool { return s == SourceReload || s == SourceGuildmasterApproved }

// ManagedRevision is one managed-revision event: a managed.toml digest the copy authorized or observed, recorded
// under the enrolment it was managed by. Events are ordered by sequence and never changed.
type ManagedRevision struct {
	// Sequence numbers events from 1 and keys the record.
	Sequence uint64 `json:"sequence"`
	// Enrolment is the sequence of the enrolled event the revision was recorded under; set by the store. An
	// authorization holds only while that enrolment is the newest event.
	Enrolment uint64         `json:"enrolment"`
	Source    RevisionSource `json:"source"`
	// Digest is the managed.toml digest the event is about, and Previous the digest before it, if any.
	Digest   string `json:"digest"`
	Previous string `json:"previous,omitempty"`
	// Decision is the id of the alert decision a guildmaster-approved event answered; no other source has one.
	Decision string `json:"decision,omitempty"`
	// Receipt is the sequence of the event's receipt and Time its time in UTC; both are set by the store.
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// ManagedView is the copy's management facts: its newest enrolment event, if any, and every managed-revision
// event recorded under it, oldest first.
type ManagedView struct {
	Enrolment *Enrolment        `json:"enrolment,omitempty"`
	Revisions []ManagedRevision `json:"revisions"`
}

// Authorization is the newest revision event of the newest enrolment, when that is an enrolled event, that
// authorizes digest d; nil when there is none (docs/director-modes.md#guard-rails).
func (v ManagedView) Authorization(d string) *ManagedRevision {
	if v.Enrolment == nil || v.Enrolment.Event != Enrolled {
		return nil
	}
	for i := len(v.Revisions) - 1; i >= 0; i-- {
		if r := v.Revisions[i]; r.Digest == d && r.Source.Authorizes() {
			return &r
		}
	}
	return nil
}

// Latest is the newest revision event of the newest enrolment, nil when there is none.
func (v ManagedView) Latest() *ManagedRevision {
	if len(v.Revisions) == 0 {
		return nil
	}
	r := v.Revisions[len(v.Revisions)-1]
	return &r
}

// ManagedView reads the copy's management facts.
func (s *Store) ManagedView() (ManagedView, error) {
	var v ManagedView
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		v, err = managedView(tx)
		return err
	})
	if err != nil {
		return ManagedView{}, fmt.Errorf("store: managed view: %w", err)
	}
	return v, nil
}

func managedView(tx *bolt.Tx) (ManagedView, error) {
	v := ManagedView{Revisions: []ManagedRevision{}}
	var e Enrolment
	switch err := lastJSON(directorBucket(tx, bucketEnrolments), &e); {
	case errors.Is(err, ErrNotFound):
		return v, nil
	case err != nil:
		return v, err
	}
	v.Enrolment = &e
	if e.Event != Enrolled {
		return v, nil
	}
	if b := directorBucket(tx, bucketManagedRevisions); b != nil {
		if err := b.ForEach(func(k, raw []byte) error {
			var r ManagedRevision
			if err := decode(k, raw, &r); err != nil {
				return err
			}
			if r.Enrolment == e.Sequence {
				v.Revisions = append(v.Revisions, r)
			}
			return nil
		}); err != nil {
			return v, err
		}
	}
	return v, nil
}

// RecordEnrolment records e as the copy's next enrolment event, under its content-derived mutation id
// (Enrolment.mutationID): recording the event that is already the newest again replays it, returning the event
// and its receipt, so enrol and leave are idempotent. An enrolled event follows no event or a left one and names a
// Steward label and the managed digest; a left event follows an enrolled one and names the digest it kept and its
// revision name (RevisionName). Any other event, transition or a store-set field is ErrInvalid; two different
// events recorded at once conflict with ErrMutationConflict.
func (s *Store) RecordEnrolment(e Enrolment) (Enrolment, Receipt, error) {
	if err := validateEnrolment(e); err != nil {
		return Enrolment{}, Receipt{}, err
	}
	var last Enrolment
	var seq uint64 = 1
	if err := s.view(func(tx *bolt.Tx) error {
		switch err := lastJSON(directorBucket(tx, bucketEnrolments), &last); {
		case errors.Is(err, ErrNotFound):
			last = Enrolment{}
		case err != nil:
			return err
		case last.same(e):
			seq = last.Sequence
		default:
			seq = last.Sequence + 1
		}
		return nil
	}); err != nil {
		return Enrolment{}, Receipt{}, fmt.Errorf("store: enrolments: %w", err)
	}
	if seq != last.Sequence {
		if err := enrolmentFollows(last, e); err != nil {
			return Enrolment{}, Receipt{}, err
		}
	}
	e.Sequence = seq
	r, err := s.mutate(e.mutationID(), opRecordEnrolment, e, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = "director/enrolment/" + strconv.FormatUint(seq, 10)
		events, err := createDirectorBucket(tx, bucketEnrolments)
		if err != nil {
			return err
		}
		if k, _ := events.Cursor().Last(); k != nil && string(k) != string(seqKey(seq-1)) || k == nil && seq != 1 {
			return fmt.Errorf("%w: enrolment event %d was recorded meanwhile", ErrMutationConflict, seq)
		}
		e.Receipt, e.Time = r.Sequence, s.now().UTC()
		return putJSON(events, seqKey(seq), e)
	})
	if err != nil {
		return Enrolment{}, Receipt{}, err
	}
	var out Enrolment
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketEnrolments), seqKey(seq), &out) }); err != nil {
		return Enrolment{}, Receipt{}, fmt.Errorf("store: enrolment event %d: %w", seq, err)
	}
	return out, r, nil
}

// enrolmentFollows refuses e as the event after last, the newest (zero for none).
func enrolmentFollows(last, e Enrolment) error {
	switch {
	case e.Event == Enrolled && last.Event == Enrolled:
		return invalid("the copy is already enrolled with steward %q (managed digest %s); run guildhall director leave first", last.Steward, last.Digest)
	case e.Event == Left && last.Event != Enrolled:
		return invalid("the copy is not enrolled, so it cannot leave")
	}
	return nil
}

func validateEnrolment(e Enrolment) error {
	switch {
	case e.Sequence != 0 || e.Receipt != 0 || !e.Time.IsZero():
		return invalid("enrolment sequence, receipt and time are set by the store")
	case e.Event != Enrolled && e.Event != Left:
		return invalid("enrolment event %q must be %q or %q", e.Event, Enrolled, Left)
	}
	if _, err := digest.Parse(e.Digest); err != nil {
		return invalid("enrolment managed digest: %v", err)
	}
	if e.Event == Enrolled {
		if e.Revision != "" {
			return invalid("an enrolled event keeps no revision")
		}
		if err := validateIDs(e.Steward); err != nil {
			return invalid("steward label %q must be lowercase letters, digits and hyphens, starting with a letter", e.Steward)
		}
		return nil
	}
	switch {
	case e.Steward != "":
		return invalid("a left event names no steward")
	case e.Revision != RevisionName(digest.Digest(e.Digest)):
		return invalid("a left event keeps managed.toml as %s, not %q", RevisionName(digest.Digest(e.Digest)), e.Revision)
	case !revisionNamePattern.MatchString(e.Revision):
		return invalid("revision name %q must be managed.toml.rev-<16 hex>", e.Revision)
	}
	return nil
}

// RecordManagedRevision records m as a managed-revision event of the copy's current enrolment, which must be an
// enrolled event, under the mutation id "managed-revision:<enrolment>:<source>:<digest hex>": one event per
// enrolment, source and digest, so recording it again returns the event already recorded and its receipt, whatever
// Previous it names. A guildmaster-approved event names its alert decision, and no other source does; a bad
// digest, an unknown source or a store-set field is ErrInvalid, and so is a copy that is not enrolled.
func (s *Store) RecordManagedRevision(m ManagedRevision) (ManagedRevision, Receipt, error) {
	if err := validateManagedRevision(m); err != nil {
		return ManagedRevision{}, Receipt{}, err
	}
	var v ManagedView
	if err := s.view(func(tx *bolt.Tx) error {
		var err error
		v, err = managedView(tx)
		return err
	}); err != nil {
		return ManagedRevision{}, Receipt{}, fmt.Errorf("store: managed revisions: %w", err)
	}
	if v.Enrolment == nil || v.Enrolment.Event != Enrolled {
		return ManagedRevision{}, Receipt{}, invalid("the copy is not enrolled, so it records no managed revision")
	}
	m.Enrolment = v.Enrolment.Sequence
	id := "managed-revision:" + strconv.FormatUint(m.Enrolment, 10) + ":" + string(m.Source) + ":" + digest.Digest(m.Digest).Hex()
	for _, r := range v.Revisions {
		if r.Source == m.Source && r.Digest == m.Digest {
			rec, err := s.Receipt(id)
			return r, rec, err
		}
	}
	var seq uint64
	r, err := s.mutate(id, opRecordManagedRevision, m, func(tx *bolt.Tx, r *Receipt) error {
		cur, err := managedView(tx)
		if err != nil {
			return err
		}
		if cur.Enrolment == nil || cur.Enrolment.Sequence != m.Enrolment || cur.Enrolment.Event != Enrolled {
			return fmt.Errorf("%w: the copy's enrolment changed meanwhile", ErrMutationConflict)
		}
		revisions, err := createDirectorBucket(tx, bucketManagedRevisions)
		if err != nil {
			return err
		}
		if seq, err = revisions.NextSequence(); err != nil {
			return err
		}
		r.Record = "director/managed-revision/" + strconv.FormatUint(seq, 10)
		m.Sequence, m.Receipt, m.Time = seq, r.Sequence, s.now().UTC()
		return putJSON(revisions, seqKey(seq), m)
	})
	if err != nil {
		return ManagedRevision{}, Receipt{}, err
	}
	var out ManagedRevision
	if err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketManagedRevisions)
		if seq == 0 {
			// A replayed mutation ran no apply: find the event its receipt recorded.
			if b == nil {
				return ErrNotFound
			}
			c := b.Cursor()
			for k, raw := c.Last(); k != nil; k, raw = c.Prev() {
				if err := decode(k, raw, &out); err != nil {
					return err
				}
				if out.Receipt == r.Sequence {
					return nil
				}
			}
			return ErrNotFound
		}
		return getJSON(b, seqKey(seq), &out)
	}); err != nil {
		return ManagedRevision{}, Receipt{}, fmt.Errorf("store: managed revision: %w", err)
	}
	return out, r, nil
}

func validateManagedRevision(m ManagedRevision) error {
	switch {
	case m.Sequence != 0 || m.Enrolment != 0 || m.Receipt != 0 || !m.Time.IsZero():
		return invalid("managed revision sequence, enrolment, receipt and time are set by the store")
	case m.Source != SourceReload && m.Source != SourceObservedUntrusted && m.Source != SourceGuildmasterApproved:
		return invalid("managed revision source %q must be %q, %q or %q", m.Source, SourceReload, SourceObservedUntrusted, SourceGuildmasterApproved)
	case (m.Source == SourceGuildmasterApproved) != (m.Decision != ""):
		return invalid("a %s revision names its alert decision, and no other source does", SourceGuildmasterApproved)
	}
	if _, err := digest.Parse(m.Digest); err != nil {
		return invalid("managed revision digest: %v", err)
	}
	if m.Previous != "" {
		if _, err := digest.Parse(m.Previous); err != nil {
			return invalid("managed revision previous digest: %v", err)
		}
	}
	if m.Decision != "" {
		if err := validateIDs(m.Decision); err != nil {
			return err
		}
	}
	return nil
}
