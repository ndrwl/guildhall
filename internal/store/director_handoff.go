package store

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Nested buckets of the director bucket holding handoff notes and reload requests, each keyed by zero-padded
// receipt sequence.
var (
	bucketHandoffs = []byte("handoffs")
	bucketReloads  = []byte("reloads")
)

// Operations recorded on handoff and reload receipts.
const (
	opRecordHandoffNote     = "record-handoff-note"
	opRequestDirectorReload = "request-director-reload"
	opApplyDirectorReload   = "apply-director-reload"
)

// MaxHandoffNoteBytes bounds a handoff note's content.
const MaxHandoffNoteBytes = 64 << 10

// HandoffNote is one handoff note the Director recorded for its next session (docs/store.md#handoff-notes).
// The note itself is a document file, not a store blob (R1.13); the record names it and its digest.
type HandoffNote struct {
	// Session is the number of the session that recorded the note.
	Session int `json:"session"`
	// Sequence is set by the store to the note's receipt sequence.
	Sequence uint64 `json:"sequence"`
	// Path is set by the store to "director/handoff-<sequence>.md", slash-separated and relative to the
	// Director's document directory.
	Path string `json:"path"`
	// SHA256 is the lowercase hex sha256 of the note's content.
	SHA256 string `json:"sha256"`
	// RecordedAt is set by the store, in UTC.
	RecordedAt time.Time `json:"recorded_at"`
}

// ReloadState is where a `director reload` request is.
type ReloadState string

const (
	// ReloadRequested is a request the supervisor has not yet applied.
	ReloadRequested ReloadState = "requested"
	// ReloadApplied is a request a new session applied. It never changes again.
	ReloadApplied ReloadState = "applied"
)

// DirectorReload is one `director reload` request (docs/store.md#reload-requests): at most one is requested at
// a time, since requests made before it is applied collapse into it.
type DirectorReload struct {
	// Sequence is set by the store to the sequence of the receipt that recorded the request.
	Sequence uint64      `json:"sequence"`
	State    ReloadState `json:"state"`
	// Now is set for `director reload --now`: the supervisor ends the runtime at the end of the current turn
	// rather than at a quiet point.
	Now bool `json:"now,omitempty"`
	// RequestedAt is set by the store, in UTC.
	RequestedAt time.Time `json:"requested_at"`
	// OldSession and NewSession are the newest session before the reload, 0 when there was none, and the
	// session that applied it; AppliedAt is when it was applied, in UTC.
	OldSession int       `json:"old_session,omitempty"`
	NewSession int       `json:"new_session,omitempty"`
	AppliedAt  time.Time `json:"applied_at,omitzero"`
}

// seqKey keys handoff notes and reloads by receipt sequence, as wakes are keyed.
func seqKey(seq uint64) []byte { return wakeKey(seq) }

func handoffRecord(seq uint64) string { return "director/handoff/" + strconv.FormatUint(seq, 10) }
func reloadRecord(seq uint64) string  { return "director/reload/" + strconv.FormatUint(seq, 10) }

// HandoffPath is the document path of the handoff note recorded at receipt sequence seq.
func HandoffPath(seq uint64) string { return fmt.Sprintf("director/handoff-%d.md", seq) }

// RecordHandoffNote records a handoff note of session n whose content has the lowercase hex sha256, with the
// mutation id "handoff:<n>:<sha256>". The store sets the note's Sequence, Path and RecordedAt and, before
// committing, calls write with Path, which writes the note's file there; an error from write records nothing,
// so a recorded note's file was written first. Session n must be the newest session and not stopped, otherwise
// ErrInvalid, or ErrNotFound when there is no session n. Replaying returns the original note and receipt
// without calling write.
func (s *Store) RecordHandoffNote(n int, sha256 string, write func(path string) error) (HandoffNote, Receipt, error) {
	if n < 1 {
		return HandoffNote{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	if _, err := digest.ParseHex(sha256); err != nil {
		return HandoffNote{}, Receipt{}, invalid("handoff note sha256 %q must be 64 lowercase hex characters", sha256)
	}
	req := struct {
		Session int    `json:"session"`
		SHA256  string `json:"sha256"`
	}{n, sha256}
	r, err := s.mutate(fmt.Sprintf("handoff:%d:%s", n, sha256), opRecordHandoffNote, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = handoffRecord(r.Sequence)
		d, err := latestSession(tx)
		switch {
		case errors.Is(err, ErrNotFound):
			return fmt.Errorf("director session %d: %w", n, ErrNotFound)
		case err != nil:
			return err
		case d.Number < n:
			return fmt.Errorf("director session %d: %w", n, ErrNotFound)
		case d.Number > n:
			return invalid("director session %d is superseded by session %d", n, d.Number)
		case d.State == DirectorStopped:
			return invalid("director session %d is stopped", n)
		}
		handoffs, err := createDirectorBucket(tx, bucketHandoffs)
		if err != nil {
			return err
		}
		note := HandoffNote{Session: n, Sequence: r.Sequence, Path: HandoffPath(r.Sequence), SHA256: sha256, RecordedAt: s.now().UTC()}
		if err := write(note.Path); err != nil {
			return err
		}
		return putJSON(handoffs, seqKey(note.Sequence), note)
	})
	if err != nil {
		return HandoffNote{}, Receipt{}, err
	}
	var note HandoffNote
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketHandoffs), seqKey(r.Sequence), &note) }); err != nil {
		return HandoffNote{}, Receipt{}, fmt.Errorf("store: handoff note %d: %w", r.Sequence, err)
	}
	return note, r, nil
}

// LatestHandoffNote returns the newest handoff note, or ErrNotFound when the store has none.
func (s *Store) LatestHandoffNote() (HandoffNote, error) {
	var note HandoffNote
	if err := s.view(func(tx *bolt.Tx) error { return lastJSON(directorBucket(tx, bucketHandoffs), &note) }); err != nil {
		return HandoffNote{}, fmt.Errorf("store: latest handoff note: %w", err)
	}
	return note, nil
}

// lastJSON decodes the last record of b, or returns ErrNotFound for a missing or empty bucket.
func lastJSON(b *bolt.Bucket, v any) error {
	if b == nil {
		return ErrNotFound
	}
	k, data := b.Cursor().Last()
	if k == nil {
		return ErrNotFound
	}
	return decode(k, data, v)
}

// RequestDirectorReload records a `director reload` request, setting Now for --now, and returns the requested
// reload. A request made while another is requested collapses into it: a plain request returns it unchanged
// and recording nothing, and --now sets its Now under the mutation id "director-reload-now:<sequence>". A new
// request has the mutation id "director-reload:<after>:<turn|now>", where after is the sequence of the newest
// applied reload, or 0, so concurrent identical requests replay into one.
func (s *Store) RequestDirectorReload(now bool) (DirectorReload, error) {
	latest, err := s.LatestDirectorReload()
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return DirectorReload{}, err
	case latest.State == ReloadRequested && (latest.Now || !now):
		return latest, nil
	}
	id := fmt.Sprintf("director-reload:%d:%s", latest.Sequence, map[bool]string{false: "turn", true: "now"}[now])
	if latest.State == ReloadRequested {
		id = fmt.Sprintf("director-reload-now:%d", latest.Sequence)
	}
	req := struct {
		Now bool `json:"now"`
	}{now}
	_, err = s.mutate(id, opRequestDirectorReload, req, func(tx *bolt.Tx, r *Receipt) error {
		reloads, err := createDirectorBucket(tx, bucketReloads)
		if err != nil {
			return err
		}
		var cur DirectorReload
		switch err := lastJSON(reloads, &cur); {
		case err == nil && cur.State == ReloadRequested:
			// A request recorded since the read collapses into, or is upgraded to, the pending one.
			r.Record = reloadRecord(cur.Sequence)
			cur.Now = cur.Now || now
			return putJSON(reloads, seqKey(cur.Sequence), cur)
		case err != nil && !errors.Is(err, ErrNotFound):
			return err
		}
		r.Record = reloadRecord(r.Sequence)
		return putJSON(reloads, seqKey(r.Sequence), DirectorReload{Sequence: r.Sequence, State: ReloadRequested, Now: now, RequestedAt: s.now().UTC()})
	})
	if err != nil {
		return DirectorReload{}, err
	}
	return s.LatestDirectorReload()
}

// LatestDirectorReload returns the newest reload request, requested or applied, or ErrNotFound when the store
// has none. A reload is pending when the newest one is requested.
func (s *Store) LatestDirectorReload() (DirectorReload, error) {
	var r DirectorReload
	if err := s.view(func(tx *bolt.Tx) error { return lastJSON(directorBucket(tx, bucketReloads), &r) }); err != nil {
		return DirectorReload{}, fmt.Errorf("store: latest director reload: %w", err)
	}
	return r, nil
}

// ApplyDirectorReload records the requested reload at sequence seq applied by session newSession, which
// followed session oldSession (0 for none), with the mutation id "director-reload-applied:<seq>". It is refused
// with ErrNotFound for no reload at seq and ErrInvalid for a newSession below 1 or an oldSession not below
// newSession; applying it again with other sessions is ErrMutationConflict. Replaying returns the applied reload.
func (s *Store) ApplyDirectorReload(seq uint64, oldSession, newSession int) (DirectorReload, error) {
	if newSession < 1 || oldSession < 0 || oldSession >= newSession {
		return DirectorReload{}, invalid("a reload applied by session %d must follow an earlier session, not %d", newSession, oldSession)
	}
	req := struct {
		Sequence   uint64 `json:"sequence"`
		OldSession int    `json:"old_session"`
		NewSession int    `json:"new_session"`
	}{seq, oldSession, newSession}
	_, err := s.mutate(fmt.Sprintf("director-reload-applied:%d", seq), opApplyDirectorReload, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = reloadRecord(seq)
		reloads := directorBucket(tx, bucketReloads)
		var d DirectorReload
		if err := getJSON(reloads, seqKey(seq), &d); err != nil {
			return fmt.Errorf("director reload %d: %w", seq, err)
		}
		if d.State != ReloadRequested {
			return invalid("director reload %d is already %s", seq, d.State)
		}
		d.State, d.OldSession, d.NewSession, d.AppliedAt = ReloadApplied, oldSession, newSession, s.now().UTC()
		return putJSON(reloads, seqKey(seq), d)
	})
	if err != nil {
		return DirectorReload{}, err
	}
	var d DirectorReload
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketReloads), seqKey(seq), &d) }); err != nil {
		return DirectorReload{}, fmt.Errorf("store: director reload %d: %w", seq, err)
	}
	return d, nil
}
