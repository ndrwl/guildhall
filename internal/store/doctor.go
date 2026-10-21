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
	Repositories      []RepositoryCounts
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
					if err := decodeRecord(name, k, v); err != nil {
						problem("repository %s: %v", repo, err)
					} else if name == string(bucketOwners) && uncontainedStarting(v) {
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

func decodeRecord(bucket string, k, v []byte) error {
	switch bucket {
	case string(bucketTasks):
		return decode(k, v, new(Task))
	case string(bucketAttempts):
		return decode(k, v, new(StageAttempt))
	case string(bucketDocuments):
		return decode(k, v, new(DocumentRef))
	case string(bucketPanes):
		return decode(k, v, new(PaneBinding))
	case string(bucketRulings):
		return decode(k, v, new(Ruling))
	case string(bucketOwners):
		return decode(k, v, new(Ownership))
	case string(bucketMessages):
		return decode(k, v, new(Message))
	case string(bucketInbox):
		// Inbox entries index messages by id rather than holding JSON.
		if v == nil || !messageIDPattern.Match(v) {
			return fmt.Errorf("%w: inbox entry %q", ErrNotStore, k)
		}
		return nil
	default:
		return decode(k, v, new(Decision))
	}
}

// uncontainedStarting reports whether a decoded ownership record is in
// starting with no containment recorded.
func uncontainedStarting(v []byte) bool {
	var o Ownership
	return decode(nil, v, &o) == nil && o.State == OwnerStarting && o.Containment == (Containment{})
}
