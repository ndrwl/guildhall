package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

var bucketTaskPriorities = []byte("task-priorities")

// Priority is a task's priority, p0 (urgent) to p5 (someday); a lower number comes first (docs/tasks.md#priority).
type Priority int

// The priority levels. A task with no recorded priority is DefaultPriority.
const (
	PriorityUrgent  Priority = 0
	PriorityHighest Priority = 1
	PriorityHigh    Priority = 2
	PriorityNormal  Priority = 3
	PriorityLow     Priority = 4
	PrioritySomeday Priority = 5

	DefaultPriority = PriorityNormal
)

var priorityWords = [...]string{"urgent", "highest", "high", "normal", "low", "someday"}

// String is the level's name, "p<n>".
func (p Priority) String() string { return "p" + strconv.Itoa(int(p)) }

// Word is the level's word, such as "highest".
func (p Priority) Word() string {
	if !p.valid() {
		return ""
	}
	return priorityWords[p]
}

func (p Priority) valid() bool { return p >= PriorityUrgent && p <= PrioritySomeday }

// MarshalText writes the level as "p<n>".
func (p Priority) MarshalText() ([]byte, error) {
	if !p.valid() {
		return nil, fmt.Errorf("priority %d is not p0 to p5", int(p))
	}
	return []byte(p.String()), nil
}

// UnmarshalText reads a level as ParsePriority does.
func (p *Priority) UnmarshalText(b []byte) error {
	v, err := ParsePriority(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// ParsePriority reads a level given as "p1", "1" or "highest", in any letter case.
func ParsePriority(s string) (Priority, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	for i, w := range priorityWords {
		if v == w {
			return Priority(i), nil
		}
	}
	if d := strings.TrimPrefix(v, "p"); len(d) == 1 && d[0] >= '0' && d[0] <= '5' {
		return Priority(d[0] - '0'), nil
	}
	return 0, fmt.Errorf("%w: priority %q must be p0 to p5, 0 to 5, or one of %s", ErrInvalid, s, strings.Join(priorityWords[:], ", "))
}

// TaskPriority is a task's recorded priority: the latest level and every level set, in order. The Task record
// itself never changes (R1.62), so a priority lives beside it.
type TaskPriority struct {
	Repository string     `json:"repository"`
	TaskID     string     `json:"task_id"`
	Priority   Priority   `json:"priority"`
	History    []Priority `json:"history"`
}

// TaskPriorityRecord is the receipt record of a task's priority.
const TaskPriorityRecord = "task-priority"

// SetTaskPriority records level p as an open task's priority, receipt record task-priority, appending it to the
// history. An unknown task is ErrNotFound and a finished one or a level outside p0 to p5 is ErrInvalid.
func (s *Store) SetTaskPriority(mutationID, repository, taskID string, p Priority) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if !p.valid() {
		return Receipt{}, invalid("priority %d is not p0 to p5", int(p))
	}
	req := struct {
		Repository string   `json:"repository"`
		TaskID     string   `json:"task_id"`
		Priority   Priority `json:"priority"`
	}{repository, taskID, p}
	return s.mutate(mutationID, "set-task-priority", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, TaskPriorityRecord
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketTaskPriorities)
		if err != nil {
			return err
		}
		tp := TaskPriority{Repository: repository, TaskID: taskID}
		if err := getJSON(b, []byte(taskID), &tp); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		tp.Priority, tp.History = p, append(tp.History, p)
		return putJSON(b, []byte(taskID), tp)
	})
}

// TaskPriority returns a task's recorded priority, or ErrNotFound when none was ever set.
func (s *Store) TaskPriority(repository, taskID string) (TaskPriority, error) {
	var tp TaskPriority
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketTaskPriorities), []byte(taskID), &tp)
	})
	if err != nil {
		return TaskPriority{}, fmt.Errorf("store: priority of %s/%s: %w", repository, taskID, err)
	}
	return tp, nil
}

// PriorityOf returns a task's priority, DefaultPriority when none was set.
func (s *Store) PriorityOf(repository, taskID string) (Priority, error) {
	tp, err := s.TaskPriority(repository, taskID)
	switch {
	case errors.Is(err, ErrNotFound):
		return DefaultPriority, nil
	case err != nil:
		return 0, err
	}
	return tp.Priority, nil
}

// TaskPriorities returns every recorded priority of a repository, keyed by task id.
func (s *Store) TaskPriorities(repository string) (map[string]Priority, error) {
	all, err := list[TaskPriority](s, repository, bucketTaskPriorities, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]Priority{}
	for _, tp := range all {
		out[tp.TaskID] = tp.Priority
	}
	return out, nil
}
