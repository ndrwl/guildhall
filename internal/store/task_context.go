package store

import (
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketTaskContexts = []byte("task_contexts")

// TaskContextAuthor is who records task context: always the Director, quoting the Guildmaster where the words are
// theirs (docs/tasks.md#task-context).
const TaskContextAuthor = "director"

// TaskContext is one entry of Guildmaster context the Director added to an open task: requirements, constraints or
// clarifications that every attempt of the task bound afterwards is given as its context input.
// Entries are numbered per task from 1 and append-only; a later entry supersedes an earlier one only by saying so.
type TaskContext struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Number     int    `json:"number"`
	Text       string `json:"text"`
	Author     string `json:"author"`
	// AddedAt is the commit time of the entry's receipt.
	AddedAt time.Time `json:"added_at"`
}

// AddTaskContext records the task's next context entry, receipt record task-context/<n>, on an open task.
// Blank text or text over MaxTextBytes is refused with ErrInvalid, as is a task that is not open; an unknown task is
// ErrNotFound.
func (s *Store) AddTaskContext(mutationID, repository, taskID, text string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if err := validateText("context text", text, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	req := TaskContext{Repository: repository, TaskID: taskID, Text: text, Author: TaskContextAuthor}
	return s.mutate(mutationID, "add-task-context", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketTaskContexts)
		if err != nil {
			return err
		}
		var all []TaskContext
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		c := req
		c.Number = len(all) + 1
		// The entry's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		c.AddedAt, rc.Record = rc.CommittedAt, TaskContextRecord(c.Number)
		return putJSON(b, taskContextKey(taskID, c.Number), c)
	})
}

// TaskContexts lists a task's context entries by number.
func (s *Store) TaskContexts(repository, taskID string) ([]TaskContext, error) {
	return list[TaskContext](s, repository, bucketTaskContexts, recordKey(taskID, ""))
}

// TaskContextRecord is the receipt record name of context entry number.
func TaskContextRecord(number int) string { return "task-context/" + strconv.Itoa(number) }

// taskContextKey zero-pads the number so entries sort numerically.
func taskContextKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
