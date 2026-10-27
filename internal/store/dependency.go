package store

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var bucketDependencyOverrides = []byte("dependency-overrides")

// DependencyOverride is the Director's decision that a task starts without waiting for its dependencies to merge
// (docs/tasks.md#dependencies). It is recorded once and lasts for the rest of the task's life; the Task record itself
// never changes (R1.62).
type DependencyOverride struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Reason     string `json:"reason"`
}

// DependencyOverrideRecord is the receipt record of a task's dependency override.
const DependencyOverrideRecord = "dependency-override"

// OverrideDependencies records the Director's dependency override, receipt record dependency-override, on an open
// task that has dependencies. A blank or oversized reason is refused with ErrInvalid, as is a closed task or one
// with no depends_on; an unknown task is ErrNotFound and a second override of the task, under another mutation id,
// is ErrExists.
func (s *Store) OverrideDependencies(mutationID, repository, taskID, reason string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if err := validateText("reason", reason, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	o := DependencyOverride{Repository: repository, TaskID: taskID, Reason: reason}
	return s.mutate(mutationID, "override-dependencies", o, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, DependencyOverrideRecord
		t, err := openTask(tx, repository, taskID)
		if err != nil {
			return err
		}
		if len(t.DependsOn) == 0 {
			return invalid("task %s/%s has no dependencies to override", repository, taskID)
		}
		b, err := createRepoBucket(tx, repository, bucketDependencyOverrides)
		if err != nil {
			return err
		}
		if b.Get([]byte(taskID)) != nil {
			return fmt.Errorf("%w: dependency override of %s/%s", ErrExists, repository, taskID)
		}
		return putJSON(b, []byte(taskID), o)
	})
}

// DependencyOverride returns a task's dependency override, or ErrNotFound when it has none.
func (s *Store) DependencyOverride(repository, taskID string) (DependencyOverride, error) {
	var o DependencyOverride
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketDependencyOverrides), []byte(taskID), &o)
	})
	if err != nil {
		return DependencyOverride{}, fmt.Errorf("store: dependency override of %s/%s: %w", repository, taskID, err)
	}
	return o, nil
}
