package store

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Snapshot labels a read with the store state it describes: the sequence and commit time
// of the newest mutation committed anywhere in the store when the read began.
// A store with no mutations has the zero Snapshot.
type Snapshot struct {
	Sequence    uint64    `json:"sequence"`
	CommittedAt time.Time `json:"committed_at"`
}

// TaskHistory is a task, its attempts, documents, decisions, rulings and full-review requests, together with
// the receipt of every mutation that touched it; ownership records are read separately.
// Its contents are read in one transaction and describe exactly the store state AsOf names.
// Its lists are empty rather than nil when the task has no such records.
type TaskHistory struct {
	AsOf        Snapshot
	Task        Task
	Attempts    []StageAttempt
	Documents   []DocumentRef
	Decisions   []Decision
	Rulings     []Ruling
	FullReviews []FullReview
	// Receipts are the receipts whose repository and task are this task's, in sequence order:
	// its records' mutations and the messages sent to or acknowledged for it.
	Receipts []Receipt
}

// TaskHistory reads one task's history without writing; it returns ErrNotFound for an unknown task.
// It scans every receipt in the store, so its cost grows with the store's mutation count.
func (s *Store) TaskHistory(repository, taskID string) (TaskHistory, error) {
	h := TaskHistory{Attempts: []StageAttempt{}, Documents: []DocumentRef{}, Decisions: []Decision{}, Rulings: []Ruling{}, FullReviews: []FullReview{}, Receipts: []Receipt{}}
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		if h.Task, err = getTask(tx, repository, taskID); err != nil {
			return err
		}
		prefix := recordKey(taskID, "")
		if err := scan(repoBucket(tx, repository, bucketAttempts), prefix, &h.Attempts); err != nil {
			return err
		}
		if err := scan(repoBucket(tx, repository, bucketDocuments), prefix, &h.Documents); err != nil {
			return err
		}
		if err := scan(repoBucket(tx, repository, bucketDecisions), prefix, &h.Decisions); err != nil {
			return err
		}
		if err := scan(repoBucket(tx, repository, bucketRulings), prefix, &h.Rulings); err != nil {
			return err
		}
		if err := scan(repoBucket(tx, repository, bucketFullReviews), prefix, &h.FullReviews); err != nil {
			return err
		}
		return tx.Bucket(bucketReceipts).ForEach(func(k, v []byte) error {
			var r Receipt
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("receipt %q: %w", k, err)
			}
			if r.Sequence > h.AsOf.Sequence {
				h.AsOf = Snapshot{Sequence: r.Sequence, CommittedAt: r.CommittedAt}
			}
			if r.Repository == repository && r.TaskID == taskID {
				h.Receipts = append(h.Receipts, r)
			}
			return nil
		})
	})
	if err != nil {
		return TaskHistory{}, fmt.Errorf("store: history of %s/%s: %w", repository, taskID, err)
	}
	slices.SortFunc(h.Receipts, func(a, b Receipt) int { return cmp.Compare(a.Sequence, b.Sequence) })
	return h, nil
}

func (s *Store) Receipts() ([]Receipt, error) {
	out := []Receipt{}
	err := s.view(func(tx *bolt.Tx) error {
		return scan(tx.Bucket(bucketReceipts), nil, &out)
	})
	if err != nil {
		return nil, fmt.Errorf("store: receipts: %w", err)
	}
	return out, nil
}

// scan appends the records of bucket b whose keys start with prefix, in key order; a nil bucket has none.
func scan[T any](b *bolt.Bucket, prefix []byte, out *[]T) error {
	if b == nil {
		return nil
	}
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		var rec T
		if err := decode(k, v, &rec); err != nil {
			return err
		}
		*out = append(*out, rec)
	}
	return nil
}
