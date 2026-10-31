package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

var bucketTaskOverrides = []byte("task_overrides")

// TaskOverrideAuthor is who records task overrides: always the Director, quoting the Guildmaster
// (docs/tasks.md#task-overrides).
const TaskOverrideAuthor = "director"

// TaskOverrideOp is what a task override record does to its key: sets the task's value for it or unsets it.
type TaskOverrideOp string

const (
	TaskOverrideSet   TaskOverrideOp = "set"
	TaskOverrideUnset TaskOverrideOp = "unset"
)

// TaskOverride is one record of a task's overrides: the Director, quoting the Guildmaster, giving the task its own
// value for a task-scoped setting, or removing it again. Records are numbered per task from 1 and append-only, sets
// and unsets sharing one sequence; for each key the latest record wins (ActiveTaskOverrides).
// The store does not judge the key against the configuration, which it does not have; guildhall task override set
// does, before it records.
type TaskOverride struct {
	Repository string         `json:"repository"`
	TaskID     string         `json:"task_id"`
	Number     int            `json:"number"`
	Op         TaskOverrideOp `json:"op"`
	Key        string         `json:"key"`
	// Value is the task's value for Key; an unset has none.
	Value  string `json:"value,omitempty"`
	Text   string `json:"text"`
	Author string `json:"author"`
	// AddedAt is the commit time of the record's receipt.
	AddedAt time.Time `json:"added_at"`
}

// taskOverrideRequest is what AddTaskOverride is asked, which a replay must repeat exactly.
type taskOverrideRequest struct {
	Repository string         `json:"repository"`
	TaskID     string         `json:"task_id"`
	Op         TaskOverrideOp `json:"op"`
	Key        string         `json:"key"`
	Value      string         `json:"value"`
	Prior      int            `json:"prior"`
	Text       string         `json:"text"`
}

// AddTaskOverride records the task's next override record, receipt record task-override/<n>, on an open task: op
// set gives the task value for key, and op unset removes the task's value. prior is how many records the task already
// has for that key, so a command that did not see the latest one cannot stack a record on top of it: another count is
// refused with ErrExists. An empty key, one with whitespace or a step key (step.), a set without a value or an unset
// with one, blank text or text over MaxTextBytes, a task that is not open and an unset of a key that has no override
// in effect are refused with ErrInvalid; an unknown task is ErrNotFound.
func (s *Store) AddTaskOverride(mutationID, repository, taskID string, op TaskOverrideOp, key, value string, prior int, text string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	switch {
	case op != TaskOverrideSet && op != TaskOverrideUnset:
		return Receipt{}, invalid("task override op %q must be set or unset", op)
	case key == "":
		return Receipt{}, invalid("task override key must not be empty")
	case strings.ContainsFunc(key, unicode.IsSpace) || !utf8.ValidString(key):
		return Receipt{}, invalid("task override key %q must be valid UTF-8 without whitespace", key)
	case strings.HasPrefix(key, "step."):
		return Receipt{}, invalid("task override key %q is a step key; only settings can be overridden per task", key)
	case op == TaskOverrideSet && value == "":
		return Receipt{}, invalid("task override set of %s must have a value", key)
	case op == TaskOverrideUnset && value != "":
		return Receipt{}, invalid("task override unset of %s takes no value", key)
	case len(value) > MaxTextBytes:
		return Receipt{}, invalid("task override value is %d bytes; the limit is %d", len(value), MaxTextBytes)
	case !utf8.ValidString(value):
		return Receipt{}, invalid("task override value is not valid UTF-8")
	case prior < 0:
		return Receipt{}, invalid("task override prior count %d must not be negative", prior)
	}
	if err := validateText("override text", text, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	req := taskOverrideRequest{Repository: repository, TaskID: taskID, Op: op, Key: key, Value: value, Prior: prior, Text: text}
	return s.mutate(mutationID, "add-task-override", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketTaskOverrides)
		if err != nil {
			return err
		}
		var all []TaskOverride
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		mine := slices.DeleteFunc(slices.Clone(all), func(o TaskOverride) bool { return o.Key != key })
		set := len(mine) > 0 && mine[len(mine)-1].Op == TaskOverrideSet
		switch {
		case len(mine) != prior:
			return fmt.Errorf("store: %w: task %s/%s has %d override records for %s, not %d", ErrExists, repository, taskID, len(mine), key, prior)
		case op == TaskOverrideUnset && !set:
			return invalid("task %s/%s has no override of %s in effect", repository, taskID, key)
		}
		o := TaskOverride{Repository: repository, TaskID: taskID, Number: len(all) + 1, Op: op, Key: key, Value: value, Text: text, Author: TaskOverrideAuthor}
		// The record's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		o.AddedAt, rc.Record = rc.CommittedAt, TaskOverrideRecord(o.Number)
		return putJSON(b, taskOverrideKey(taskID, o.Number), o)
	})
}

// TaskOverrides lists a task's override records by number.
func (s *Store) TaskOverrides(repository, taskID string) ([]TaskOverride, error) {
	return list[TaskOverride](s, repository, bucketTaskOverrides, recordKey(taskID, ""))
}

// ActiveTaskOverrides returns, for each key, the latest of overrides, given by number, when it is a set: the task's
// overrides in effect, in key order.
func ActiveTaskOverrides(overrides []TaskOverride) []TaskOverride {
	latest := map[string]TaskOverride{}
	for _, o := range overrides {
		latest[o.Key] = o
	}
	var out []TaskOverride
	for _, o := range latest {
		if o.Op == TaskOverrideSet {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b TaskOverride) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// TaskOverrideRecord is the receipt record name of override record number.
func TaskOverrideRecord(number int) string { return "task-override/" + strconv.Itoa(number) }

// taskOverrideKey zero-pads the number so records sort numerically.
func taskOverrideKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
