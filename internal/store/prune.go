package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketPruned holds one prune record per pruned task, keyed by task id (docs/store.md#pruned-tasks).
var bucketPruned = []byte("pruned")

// PruneRecord is the receipt record name of a task's prune.
const PruneRecord = "prune"

// opPruneTask is the operation recorded on a prune receipt.
const opPruneTask = "prune-task"

// What a prune did with a task's working directory.
const (
	// WorktreeRemoved is a worktree git.Program.Retire removed, or found already retired.
	WorktreeRemoved = "removed"
	// WorktreeKept is a worktree Retire kept because something in it would be lost; Kept says what.
	WorktreeKept = "kept"
	// WorktreeNotWorkspace is a work directory that is not a worktree of the repository, left untouched.
	WorktreeNotWorkspace = "not-a-worktree"
)

// TaskPrune records that `guildhall prune` reclaimed the disk space of a done or cancelled task's working directory and attempt
// directories (docs/running.md#pruning). It is written only after removal finishes, so a task with no record, or
// one whose record is not Complete, is pruned again by the next run; every recorded document stays.
type TaskPrune struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	// PrunedAt is when the newest prune was recorded, set by the store.
	PrunedAt time.Time `json:"pruned_at"`
	// BytesFreed is what the newest prune removed.
	BytesFreed int64 `json:"bytes_freed"`
	// Worktree is WorktreeRemoved, WorktreeKept or WorktreeNotWorkspace, and Kept the reason for the last two.
	Worktree string `json:"worktree"`
	Kept     string `json:"kept,omitempty"`
	// Complete is set when nothing prunable was left: the worktree is removed and every attempt directory holds only
	// the files a prune keeps.
	Complete bool `json:"complete"`
	// Runs counts the prunes recorded for the task, so each run's mutation id differs from the last one's.
	Runs int `json:"runs"`
}

// PruneTask records a prune of the done or cancelled task p names, receipt record prune. p.Runs is the record's run
// count the caller read, 0 when there was none; the stored record counts one more. It refuses with ErrInvalid a task
// that is neither done nor cancelled, an unknown worktree outcome and a negative count, with ErrNotFound an unknown task, and with ErrExists a
// stored count other than p.Runs or a task already pruned completely.
func (s *Store) PruneTask(mutationID string, p TaskPrune) (Receipt, error) {
	if err := validateIDs(p.Repository, p.TaskID); err != nil {
		return Receipt{}, err
	}
	switch {
	case p.Runs < 0:
		return Receipt{}, invalid("prune runs %d must not be negative", p.Runs)
	case p.BytesFreed < 0:
		return Receipt{}, invalid("bytes freed %d must not be negative", p.BytesFreed)
	case p.Worktree != WorktreeRemoved && p.Worktree != WorktreeKept && p.Worktree != WorktreeNotWorkspace:
		return Receipt{}, invalid("worktree outcome %q must be %q, %q or %q", p.Worktree, WorktreeRemoved, WorktreeKept, WorktreeNotWorkspace)
	case (p.Worktree == WorktreeRemoved) != (p.Kept == ""):
		return Receipt{}, invalid("a %s worktree has no reason; a kept one or one that is not a worktree has one", WorktreeRemoved)
	}
	if p.Kept != "" {
		if err := validateText("kept", p.Kept, MaxTextBytes); err != nil {
			return Receipt{}, err
		}
	}
	p.PrunedAt = time.Time{}
	return s.mutate(mutationID, opPruneTask, p, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = p.Repository, p.TaskID, PruneRecord
		t, err := getTask(tx, p.Repository, p.TaskID)
		if err != nil {
			return err
		}
		if t.State != TaskDone && t.State != TaskCancelled {
			return invalid("task %s/%s is %s; only a done or cancelled task is pruned", p.Repository, p.TaskID, t.State)
		}
		b, err := createRepoBucket(tx, p.Repository, bucketPruned)
		if err != nil {
			return err
		}
		var prior TaskPrune
		if err := getJSON(b, []byte(p.TaskID), &prior); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		switch {
		case prior.Runs != p.Runs:
			return fmt.Errorf("%w: prune of %s/%s is at run %d, not %d", ErrExists, p.Repository, p.TaskID, prior.Runs, p.Runs)
		case prior.Complete:
			return fmt.Errorf("%w: task %s/%s is already pruned completely", ErrExists, p.Repository, p.TaskID)
		}
		rec := p
		rec.Runs, rec.PrunedAt = p.Runs+1, s.now().UTC()
		return putJSON(b, []byte(p.TaskID), rec)
	})
}

// CloseTimes returns, for each task moved to done or cancelled, the commit time of its task-state or cancel-task
// receipt. It reads in one transaction without writing and scans every receipt in the store, so its cost grows with
// the store's mutation count; read it once and reuse it.
func (s *Store) CloseTimes() (map[TaskRef]time.Time, error) {
	out := map[TaskRef]time.Time{}
	err := s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketReceipts).ForEach(func(k, v []byte) error {
			var r Receipt
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("receipt %q: %w", k, err)
			}
			if r.Operation != opSetTaskState && r.Operation != opCancelTask {
				return nil
			}
			ref := TaskRef{Repository: r.Repository, ID: r.TaskID}
			if at, seen := out[ref]; !seen || r.CommittedAt.Before(at) {
				out[ref] = r.CommittedAt
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: close times: %w", err)
	}
	return out, nil
}

// TaskPrune returns a task's prune record, or ErrNotFound when it was never pruned.
func (s *Store) TaskPrune(repository, taskID string) (TaskPrune, error) {
	var p TaskPrune
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketPruned), []byte(taskID), &p)
	})
	if err != nil {
		return TaskPrune{}, fmt.Errorf("store: prune of %s/%s: %w", repository, taskID, err)
	}
	return p, nil
}
