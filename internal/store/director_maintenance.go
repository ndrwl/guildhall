package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketMaintenance is the nested bucket of the director bucket holding the Director's recorded maintenance runs,
// keyed by zero-padded run sequence (docs/store.md#director-maintenance).
var bucketMaintenance = []byte("director-maintenance")

// opRecordMaintenance is the operation recorded on a maintenance run's receipt.
const opRecordMaintenance = "record-maintenance"

// MaxMaintenanceSummaryBytes bounds a maintenance run's summary.
const MaxMaintenanceSummaryBytes = 1024

// MaintenanceKind is the part of Director maintenance a run did (docs/director.md#maintenance).
type MaintenanceKind string

const (
	// MaintenanceNotes is a note audit.
	MaintenanceNotes MaintenanceKind = "notes"
	// MaintenanceHallmark is a hallmark task launched for one repository. No run of this kind is recorded any more:
	// runs recorded before still read, as the fallback for a repository with no done hallmark task.
	MaintenanceHallmark MaintenanceKind = "hallmark"
)

// HallmarkRecordRetired is why recording a hallmark run is refused (docs/decisions/engine-owned-hallmark-cadence.md).
const HallmarkRecordRetired = "hallmarks are recorded when the task finishes"

// MaintenancePolicy is the maintenance cadences a session was started under; zero turns a part off.
type MaintenancePolicy struct {
	// NoteAuditEverySessions is the number of sessions after the last note audit that makes the next one due.
	NoteAuditEverySessions int `json:"note_audit_every_sessions"`
	// HallmarkAfterMerges is the number of merged messages of a repository after its last hallmark that makes the
	// next one due.
	HallmarkAfterMerges int `json:"hallmark_after_merges"`
}

// MaintenanceRun is one recorded maintenance run.
type MaintenanceRun struct {
	// Sequence numbers runs from 1 and keys the record; the mutation id is "director-maintenance:<sequence>".
	Sequence uint64          `json:"sequence"`
	Kind     MaintenanceKind `json:"kind"`
	// Repository and Task name the hallmark task of a hallmark run; a note audit has neither.
	Repository string `json:"repository,omitempty"`
	Task       string `json:"task,omitempty"`
	// Session is the Director session that recorded the run.
	Session int `json:"session"`
	// Summary is one line of at most MaxMaintenanceSummaryBytes: what was removed and what was proposed.
	Summary string `json:"summary"`
	// Receipt is the sequence of the run's receipt, which later merges are compared with, and Time its time in UTC;
	// both are set by the store.
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// same reports whether r records the same run as o, by what its caller gives.
func (r MaintenanceRun) same(o MaintenanceRun) bool {
	return r.Kind == o.Kind && r.Repository == o.Repository && r.Task == o.Task && r.Session == o.Session && r.Summary == o.Summary
}

func maintenanceRecord(seq uint64) string {
	return "director/maintenance/" + strconv.FormatUint(seq, 10)
}

// RecordMaintenance records run m of session m.Session as the next maintenance run, under the mutation id
// "director-maintenance:<sequence>", where sequence is one more than the newest run's: recording the run that is
// already the newest again replays it, returning the run and its receipt, and two different runs recorded at
// once conflict with ErrMutationConflict. A hallmark, which is recorded when its task finishes
// (docs/decisions/engine-owned-hallmark-cadence.md), a kind other than notes, a note audit naming a repository or
// task, a blank, multi-line or oversized summary, store-set fields and a session that is not the newest or is stopped
// are ErrInvalid; an unknown session is ErrNotFound.
func (s *Store) RecordMaintenance(m MaintenanceRun) (MaintenanceRun, Receipt, error) {
	if err := validateMaintenanceRun(m); err != nil {
		return MaintenanceRun{}, Receipt{}, err
	}
	var seq uint64 = 1
	if err := s.view(func(tx *bolt.Tx) error {
		var last MaintenanceRun
		switch err := lastJSON(directorBucket(tx, bucketMaintenance), &last); {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return err
		case last.same(m):
			seq = last.Sequence
		default:
			seq = last.Sequence + 1
		}
		return nil
	}); err != nil {
		return MaintenanceRun{}, Receipt{}, fmt.Errorf("store: maintenance runs: %w", err)
	}
	m.Sequence = seq
	r, err := s.mutate("director-maintenance:"+strconv.FormatUint(seq, 10), opRecordMaintenance, m, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = maintenanceRecord(seq)
		if err := checkWriterSession(tx, m.Session); err != nil {
			return err
		}
		runs, err := createDirectorBucket(tx, bucketMaintenance)
		if err != nil {
			return err
		}
		if runs.Get(seqKey(seq)) != nil {
			return fmt.Errorf("%w: maintenance run %d was recorded meanwhile", ErrMutationConflict, seq)
		}
		m.Receipt, m.Time = r.Sequence, s.now().UTC()
		return putJSON(runs, seqKey(seq), m)
	})
	if err != nil {
		return MaintenanceRun{}, Receipt{}, err
	}
	var out MaintenanceRun
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketMaintenance), seqKey(seq), &out) }); err != nil {
		return MaintenanceRun{}, Receipt{}, fmt.Errorf("store: maintenance run %d: %w", seq, err)
	}
	return out, r, nil
}

func validateMaintenanceRun(m MaintenanceRun) error {
	switch {
	case m.Session < 1:
		return invalid("director session number %d must be positive", m.Session)
	case m.Sequence != 0 || m.Receipt != 0 || !m.Time.IsZero():
		return invalid("maintenance run sequence, receipt and time are set by the store")
	case m.Kind == MaintenanceNotes:
		if m.Repository != "" || m.Task != "" {
			return invalid("a note audit names no repository or task")
		}
	case m.Kind == MaintenanceHallmark:
		return invalid("%s", HallmarkRecordRetired)
	default:
		return invalid("maintenance kind %q must be %q", m.Kind, MaintenanceNotes)
	}
	return validateNoteText("maintenance summary", m.Summary, MaxMaintenanceSummaryBytes)
}

// MaintenanceRuns returns every recorded maintenance run, oldest first; none is an empty list.
func (s *Store) MaintenanceRuns() ([]MaintenanceRun, error) {
	out := []MaintenanceRun{}
	if err := s.view(func(tx *bolt.Tx) error { return scan(directorBucket(tx, bucketMaintenance), nil, &out) }); err != nil {
		return nil, fmt.Errorf("store: maintenance runs: %w", err)
	}
	return out, nil
}

// MaintenanceView is what the status feed reads to say when maintenance is due (docs/director.md#maintenance).
type MaintenanceView struct {
	// Baseline is the earliest session with a maintenance policy that is not zero, where counting starts, and
	// BaselineReceipt the sequence of its start receipt; Baseline is 0 when no session has one, and then nothing
	// else is read.
	Baseline        int    `json:"baseline"`
	BaselineReceipt uint64 `json:"baseline_receipt"`
	// LastNotes is the newest note audit, nil when none is recorded.
	LastNotes *MaintenanceRun `json:"last_notes,omitempty"`
	// Repositories are every repository in the store, in id order.
	Repositories []RepositoryMaintenance `json:"repositories"`
}

// RepositoryMaintenance is one repository's hallmark count from its recorded runs. A repository's last hallmark is
// its done hallmark task (interp.LastHallmark), which the status feed counts from with MergedSequences; these runs
// are read only for a repository with none (docs/decisions/engine-owned-hallmark-cadence.md).
type RepositoryMaintenance struct {
	Repository string `json:"repository"`
	// LastHallmark is the repository's newest hallmark run, nil when none is recorded.
	LastHallmark *MaintenanceRun `json:"last_hallmark,omitempty"`
	// Merged counts the repository's merged messages sent after LastHallmark's receipt, or after the baseline's
	// start receipt when that is later or there is no hallmark.
	Merged int `json:"merged"`
	// MergedSequences are the sequences of the repository's merged messages sent after the baseline's start
	// receipt, in no particular order, so a reader can count from another point without a second scan.
	MergedSequences []uint64 `json:"-"`
}

// maintenanceView reads the maintenance view in tx.
func maintenanceView(tx *bolt.Tx) (MaintenanceView, error) {
	v := MaintenanceView{Repositories: []RepositoryMaintenance{}}
	if sessions := directorBucket(tx, bucketSessions); sessions != nil {
		c := sessions.Cursor()
		for k, raw := c.First(); k != nil; k, raw = c.Next() {
			var d DirectorSession
			if err := decode(k, raw, &d); err != nil {
				return v, err
			}
			if d.Maintenance != (MaintenancePolicy{}) {
				v.Baseline = d.Number
				break
			}
		}
	}
	if v.Baseline == 0 {
		return v, nil
	}
	var started Receipt
	if err := getJSON(tx.Bucket(bucketReceipts), []byte(directorSessionMutationID(v.Baseline)), &started); err != nil {
		return v, fmt.Errorf("start receipt of director session %d: %w", v.Baseline, err)
	}
	v.BaselineReceipt = started.Sequence
	hallmarks := map[string]MaintenanceRun{}
	if runs := directorBucket(tx, bucketMaintenance); runs != nil {
		if err := runs.ForEach(func(k, raw []byte) error {
			var m MaintenanceRun
			if err := decode(k, raw, &m); err != nil {
				return err
			}
			switch m.Kind {
			case MaintenanceNotes:
				v.LastNotes = &m
			case MaintenanceHallmark:
				hallmarks[m.Repository] = m
			}
			return nil
		}); err != nil {
			return v, err
		}
	}
	var repos []string
	_ = tx.Bucket(bucketRepos).ForEachBucket(func(k []byte) error { repos = append(repos, string(k)); return nil })
	for _, repo := range repos {
		rm := RepositoryMaintenance{Repository: repo}
		after := v.BaselineReceipt
		if h, ok := hallmarks[repo]; ok {
			rm.LastHallmark = &h
			after = max(after, h.Receipt)
		}
		if messages := repoBucket(tx, repo, bucketMessages); messages != nil {
			if err := messages.ForEach(func(k, raw []byte) error {
				var m struct {
					Type     string `json:"type"`
					Sequence uint64 `json:"sequence"`
				}
				if err := json.Unmarshal(raw, &m); err != nil {
					return fmt.Errorf("message %q: %w", k, err)
				}
				if m.Type != MessageMerged || m.Sequence <= v.BaselineReceipt {
					return nil
				}
				rm.MergedSequences = append(rm.MergedSequences, m.Sequence)
				if m.Sequence > after {
					rm.Merged++
				}
				return nil
			}); err != nil {
				return v, err
			}
		}
		v.Repositories = append(v.Repositories, rm)
	}
	return v, nil
}
