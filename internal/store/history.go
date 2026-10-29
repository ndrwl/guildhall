package store

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
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

// TaskHistory is a task, its attempts, documents, decisions, rulings, full-review requests, dependency override and priority,
// together with
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
	// DependencyOverride is the task's dependency override, nil when it has none.
	DependencyOverride *DependencyOverride
	// Priority is the task's recorded priority, nil when none was set (DefaultPriority).
	Priority *TaskPriority
	// MergeHold is the task's hold-merge record, nil when its merge was never held.
	MergeHold *MergeHold
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
		var o DependencyOverride
		switch err := getJSON(repoBucket(tx, repository, bucketDependencyOverrides), []byte(taskID), &o); {
		case err == nil:
			h.DependencyOverride = &o
		case !errors.Is(err, ErrNotFound):
			return err
		}
		var p TaskPriority
		switch err := getJSON(repoBucket(tx, repository, bucketTaskPriorities), []byte(taskID), &p); {
		case err == nil:
			h.Priority = &p
		case !errors.Is(err, ErrNotFound):
			return err
		}
		var mh MergeHold
		switch err := getJSON(repoBucket(tx, repository, bucketMergeHolds), []byte(taskID), &mh); {
		case err == nil:
			h.MergeHold = &mh
		case !errors.Is(err, ErrNotFound):
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

// Receipts reads every mutation receipt in one transaction, without task filtering.
// It scans the whole receipt bucket; callers should reuse the result across tasks.
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

// AttemptName names one stage attempt within a repository.
type AttemptName struct {
	TaskID string
	Step   string
	Number int
}

// FinishTimes returns, for each finished attempt of a repository, the commit time of its finish-attempt receipt:
// when FinishAttempt first recorded its outcome, however intake changed the outcome later. It reads in one
// transaction without writing, matching receipts to attempts by request digest, so receipts committed before
// receipts named their record are matched too. It scans every receipt in the store, so its cost grows with the
// store's mutation count; read it once per repository and reuse it.
func (s *Store) FinishTimes(repository string) (map[AttemptName]time.Time, error) {
	out := map[AttemptName]time.Time{}
	err := s.view(func(tx *bolt.Tx) error {
		var attempts []StageAttempt
		if err := scan(repoBucket(tx, repository, bucketAttempts), nil, &attempts); err != nil {
			return err
		}
		finishes := map[string]AttemptName{}
		for _, a := range attempts {
			if a.Outcome == "" {
				continue
			}
			for _, outcome := range []AttemptOutcome{AttemptSucceeded, AttemptFailed, AttemptStopped} {
				req := StageAttempt{Repository: repository, TaskID: a.TaskID, Step: a.Step, Number: a.Number, Outcome: outcome}
				d, err := requestDigest("finish-attempt", req)
				if err != nil {
					return err
				}
				finishes[d] = AttemptName{TaskID: a.TaskID, Step: a.Step, Number: a.Number}
			}
		}
		if len(finishes) == 0 {
			return nil
		}
		return tx.Bucket(bucketReceipts).ForEach(func(k, v []byte) error {
			var r Receipt
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("receipt %q: %w", k, err)
			}
			if r.Operation != "finish-attempt" || r.Repository != repository {
				return nil
			}
			if name, ok := finishes[r.RequestDigest]; ok {
				if at, seen := out[name]; !seen || r.CommittedAt.Before(at) {
					out[name] = r.CommittedAt
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: finish times of %s: %w", repository, err)
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
