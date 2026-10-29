package store

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// Report is what Doctor checked in a store.
type Report struct {
	SchemaVersion int
	// Receipts counts committed mutations.
	Receipts int
	// WorkflowApprovals counts recorded workflow approvals.
	WorkflowApprovals int
	// DirectorSessions and Wakes count the Director's session and wake records (docs/store.md#director-sessions).
	DirectorSessions int
	Wakes            int
	Repositories     []RepositoryCounts
	// Problems lists bbolt integrity errors and undecodable records; empty means none were found.
	Problems []string
}

// RepositoryCounts counts one repository's records.
type RepositoryCounts struct {
	Repository   string
	Tasks        int
	Attempts     int
	Documents    int
	PaneBindings int
	Decisions    int
	Rulings      int
	Owners       int
	// UncontainedStarting counts the owners in starting with no containment
	// recorded: once their launcher is gone nothing can prove them exited
	// (docs/artisan.md#limits).
	UncontainedStarting int
	Messages            int
	UnreadMessages      int
}

// maxProblems bounds the problems Doctor collects from a badly damaged file.
const maxProblems = 100

// Doctor checks the file's page-level integrity, decodes every record and counts them.
// It never writes, so it is safe on a store opened read-only.
func (s *Store) Doctor() (Report, error) {
	var rep Report
	problem := func(format string, args ...any) {
		if len(rep.Problems) < maxProblems {
			rep.Problems = append(rep.Problems, fmt.Sprintf(format, args...))
		}
	}
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		if rep.SchemaVersion, err = schemaVersion(tx); err != nil {
			return err
		}
		for err := range tx.Check() {
			problem("integrity: %v", err)
		}
		_ = tx.Bucket(bucketReceipts).ForEach(func(k, v []byte) error {
			rep.Receipts++
			var r Receipt
			if err := decode(k, v, &r); err != nil {
				problem("receipt: %v", err)
			}
			return nil
		})
		if approvals := tx.Bucket(bucketWorkflowApprovals); approvals != nil {
			_ = approvals.ForEach(func(k, v []byte) error {
				rep.WorkflowApprovals++
				if err := decode(k, v, new(WorkflowApproval)); err != nil {
					problem("workflow approval: %v", err)
				}
				return nil
			})
		}
		for _, d := range []struct {
			name   []byte
			n      *int
			record func() any
		}{
			{bucketSessions, &rep.DirectorSessions, func() any { return new(DirectorSession) }},
			{bucketWakes, &rep.Wakes, func() any { return new(Wake) }},
			{bucketHandoffs, nil, func() any { return new(HandoffNote) }},
			{bucketNotes, nil, func() any { return new(Note) }},
			{bucketNoteVersions, nil, func() any { return new(NoteVersion) }},
			{bucketReloads, nil, func() any { return new(DirectorReload) }},
			{bucketHandoffDues, nil, func() any { return new(HandoffDue) }},
		} {
			if b := directorBucket(tx, d.name); b != nil {
				_ = b.ForEach(func(k, v []byte) error {
					if d.n != nil {
						*d.n++
					}
					if err := decode(k, v, d.record()); err != nil {
						problem("director %s: %v", d.name, err)
					}
					return nil
				})
			}
		}
		return tx.Bucket(bucketRepos).ForEachBucket(func(repo []byte) error {
			counts := RepositoryCounts{Repository: string(repo)}
			for name, n := range map[string]*int{
				string(bucketTasks): &counts.Tasks, string(bucketAttempts): &counts.Attempts, string(bucketDocuments): &counts.Documents,
				string(bucketPanes): &counts.PaneBindings, string(bucketDecisions): &counts.Decisions, string(bucketRulings): &counts.Rulings,
				string(bucketOwners): &counts.Owners, string(bucketMessages): &counts.Messages, string(bucketInbox): &counts.UnreadMessages,
			} {
				b := tx.Bucket(bucketRepos).Bucket(repo).Bucket([]byte(name))
				if b == nil {
					continue
				}
				_ = b.ForEach(func(k, v []byte) error {
					*n++
					rec, err := decodeRecord(name, k, v)
					if err != nil {
						problem("repository %s: %v", repo, err)
					} else if o, ok := rec.(*Ownership); ok && o.State == OwnerStarting && o.Containment == (Containment{}) {
						counts.UncontainedStarting++
					}
					return nil
				})
			}
			rep.Repositories = append(rep.Repositories, counts)
			return nil
		})
	})
	if err != nil {
		return Report{}, fmt.Errorf("store: doctor: %w", err)
	}
	return rep, nil
}

func decodeRecord(bucket string, k, v []byte) (any, error) {
	var rec any
	switch bucket {
	case string(bucketTasks):
		rec = new(Task)
	case string(bucketAttempts):
		rec = new(StageAttempt)
	case string(bucketDocuments):
		rec = new(DocumentRef)
	case string(bucketPanes):
		rec = new(PaneBinding)
	case string(bucketRulings):
		rec = new(Ruling)
	case string(bucketOwners):
		rec = new(Ownership)
	case string(bucketMessages):
		rec = new(Message)
	case string(bucketInbox):
		// Inbox entries index messages by id rather than holding JSON.
		if v == nil || !messageIDPattern.Match(v) {
			return nil, fmt.Errorf("%w: inbox entry %q", ErrNotStore, k)
		}
		return nil, nil
	default:
		rec = new(Decision)
	}
	return rec, decode(k, v, rec)
}
