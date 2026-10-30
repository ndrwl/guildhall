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

// Operations recorded on reload receipts.
const (
	opRequestDirectorReload = "request-director-reload"
	opApplyDirectorReload   = "apply-director-reload"
)

// HandoffNote is one retired handoff note a Director recorded for its next session before notes replaced them
// (docs/decisions/0094-director-notes.md). Nothing writes or shows them any more; stores keep them and
// `store doctor` still decodes them (docs/store.md#handoff-notes).
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
	// OldRelease and OldSHA256 are the release and binary sha256 the old session's supervisor recorded, and
	// NewRelease and NewSHA256 the new session's, copied from the two session records when the reload is applied.
	OldRelease string `json:"old_release,omitempty"`
	OldSHA256  string `json:"old_sha256,omitempty"`
	NewRelease string `json:"new_release,omitempty"`
	NewSHA256  string `json:"new_sha256,omitempty"`
	// RefusedSHA256 and RefusedReason are set when the supervisor refused to move onto the guildhall binary on
	// PATH, whose sha256 RefusedSHA256 is, and reloaded on its own binary instead.
	RefusedSHA256 string `json:"refused_sha256,omitempty"`
	RefusedReason string `json:"refused_reason,omitempty"`
}

// ReloadRefusal is a guildhall binary a supervisor refused to move onto at a reload: its sha256, "" when it could
// not be hashed, and a one-line reason. The zero refusal is none.
type ReloadRefusal struct {
	SHA256 string `json:"sha256,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// MaxRefusedReasonBytes bounds a reload refusal's reason.
const MaxRefusedReasonBytes = 1024

// seqKey keys handoff notes, note versions and reloads by receipt sequence, as wakes are keyed.
func seqKey(seq uint64) []byte { return wakeKey(seq) }

func reloadRecord(seq uint64) string { return "director/reload/" + strconv.FormatUint(seq, 10) }

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
// newSession; applying it again with other sessions or another refusal is ErrMutationConflict. Replaying returns the
// applied reload. The old and new release and binary sha256 are copied from the two sessions' records, as recorded
// (none for a session not recorded), so whichever binary applies the reload records the same; refused, when it is
// not the zero refusal, is the PATH binary the old session's supervisor refused to move onto.
func (s *Store) ApplyDirectorReload(seq uint64, oldSession, newSession int, refused ReloadRefusal) (DirectorReload, error) {
	if newSession < 1 || oldSession < 0 || oldSession >= newSession {
		return DirectorReload{}, invalid("a reload applied by session %d must follow an earlier session, not %d", newSession, oldSession)
	}
	if _, err := digest.ParseHex(refused.SHA256); refused.SHA256 != "" && err != nil {
		return DirectorReload{}, invalid("a refused binary's sha256 %q must be 64 lowercase hex characters", refused.SHA256)
	}
	if err := validateText("reload refusal reason", refused.Reason, MaxRefusedReasonBytes); refused != (ReloadRefusal{}) && err != nil {
		return DirectorReload{}, err
	}
	// The refusal is left out when there is none, so the request digest of a reload without one is unchanged.
	req := struct {
		Sequence   uint64         `json:"sequence"`
		OldSession int            `json:"old_session"`
		NewSession int            `json:"new_session"`
		Refused    *ReloadRefusal `json:"refused,omitempty"`
	}{Sequence: seq, OldSession: oldSession, NewSession: newSession}
	if refused != (ReloadRefusal{}) {
		req.Refused = &refused
	}
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
		sessions := directorBucket(tx, bucketSessions)
		for _, side := range []struct {
			n               int
			release, sha256 *string
		}{{oldSession, &d.OldRelease, &d.OldSHA256}, {newSession, &d.NewRelease, &d.NewSHA256}} {
			var ds DirectorSession
			switch err := getJSON(sessions, sessionKey(side.n), &ds); {
			case side.n == 0 || errors.Is(err, ErrNotFound):
			case err != nil:
				return err
			default:
				*side.release, *side.sha256 = ds.Release, ds.BinarySHA256
			}
		}
		d.RefusedSHA256, d.RefusedReason = refused.SHA256, refused.Reason
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
