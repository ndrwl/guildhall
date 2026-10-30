package store

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketTaskLinks = []byte("task_links")

// TaskLinkAuthor is who records task links: always the Director, quoting the Guildmaster (docs/tasks.md#task-links).
const TaskLinkAuthor = "director"

// TaskLinkOp is what a task link record does to its source: adds it to the task's linked documents or removes it.
type TaskLinkOp string

const (
	TaskLinkAdd    TaskLinkOp = "add"
	TaskLinkRemove TaskLinkOp = "remove"
)

// TaskLink is one record of a task's links: the Director, quoting the Guildmaster, adding another task's recorded
// document to the documents every attempt of the task bound afterwards is given, or removing it again.
// Records are numbered per task from 1 and append-only, adds and removes sharing one sequence; for each source the
// latest record wins (ActiveTaskLinks).
type TaskLink struct {
	Repository string     `json:"repository"`
	TaskID     string     `json:"task_id"`
	Number     int        `json:"number"`
	Op         TaskLinkOp `json:"op"`
	// Source is the linked document's reference as recorded, its path and digest pinned when the record was made;
	// a recorded document never changes (PutDocument), so every attempt is given the bytes the link names.
	Source DocumentRef `json:"source"`
	Text   string      `json:"text"`
	Author string      `json:"author"`
	// AddedAt is the commit time of the record's receipt.
	AddedAt time.Time `json:"added_at"`
}

// taskLinkRequest is what AddTaskLink is asked, which a replay must repeat exactly.
type taskLinkRequest struct {
	Repository string     `json:"repository"`
	TaskID     string     `json:"task_id"`
	Op         TaskLinkOp `json:"op"`
	Source     [3]string  `json:"source"`
	Prior      int        `json:"prior"`
	Text       string     `json:"text"`
}

// AddTaskLink records the task's next link record, receipt record task-link/<n>, on an open task: op add links
// source, the document record named by source's repository, task and name, and op remove unlinks it; the record
// copies the source's path and digest. prior is how many records the task already has for that source, so a command
// that did not see the latest one cannot stack a record on top of it: another count is refused with ErrExists.
// Blank text or text over MaxTextBytes, a task that is not open, a source that is the task itself and a remove of a
// source that is not linked are refused with ErrInvalid, an add of one already linked with ErrExists; an unknown task
// or source document is ErrNotFound.
func (s *Store) AddTaskLink(mutationID, repository, taskID string, op TaskLinkOp, source DocumentRef, prior int, text string) (Receipt, error) {
	if err := validateIDs(repository, taskID, source.Repository, source.TaskID, source.Name); err != nil {
		return Receipt{}, err
	}
	if op != TaskLinkAdd && op != TaskLinkRemove {
		return Receipt{}, invalid("task link op %q must be add or remove", op)
	}
	if prior < 0 {
		return Receipt{}, invalid("task link prior count %d must not be negative", prior)
	}
	if err := validateText("link text", text, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	src := TaskLinkSource(source)
	if source.Repository == repository && source.TaskID == taskID {
		return Receipt{}, invalid("task %s/%s cannot link its own document %s; its attempts reach it already", repository, taskID, src)
	}
	req := taskLinkRequest{Repository: repository, TaskID: taskID, Op: op, Source: [3]string{source.Repository, source.TaskID, source.Name}, Prior: prior, Text: text}
	return s.mutate(mutationID, "add-task-link", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		var doc DocumentRef
		if err := getJSON(repoBucket(tx, source.Repository, bucketDocuments), recordKey(source.TaskID, source.Name), &doc); err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("store: source document %s: %w", src, ErrNotFound)
			}
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketTaskLinks)
		if err != nil {
			return err
		}
		var all []TaskLink
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		mine := slices.DeleteFunc(slices.Clone(all), func(l TaskLink) bool { return TaskLinkSource(l.Source) != src })
		linked := len(mine) > 0 && mine[len(mine)-1].Op == TaskLinkAdd
		switch {
		case len(mine) != prior:
			return fmt.Errorf("store: %w: task %s/%s has %d link records for %s, not %d", ErrExists, repository, taskID, len(mine), src, prior)
		case op == TaskLinkAdd && linked:
			return fmt.Errorf("store: %w: task %s/%s already links %s", ErrExists, repository, taskID, src)
		case op == TaskLinkRemove && !linked:
			return invalid("task %s/%s does not link %s", repository, taskID, src)
		}
		l := TaskLink{Repository: repository, TaskID: taskID, Number: len(all) + 1, Op: op, Text: text, Author: TaskLinkAuthor,
			Source: DocumentRef{Repository: doc.Repository, TaskID: doc.TaskID, Name: doc.Name, Path: doc.Path, SHA256: doc.SHA256}}
		// The record's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		l.AddedAt, rc.Record = rc.CommittedAt, TaskLinkRecord(l.Number)
		return putJSON(b, taskLinkKey(taskID, l.Number), l)
	})
}

// TaskLinks lists a task's link records by number.
func (s *Store) TaskLinks(repository, taskID string) ([]TaskLink, error) {
	return list[TaskLink](s, repository, bucketTaskLinks, recordKey(taskID, ""))
}

// ActiveTaskLinks returns the add records of links, given by number, whose source no later record removes: the
// task's linked documents, in the order they were linked.
func ActiveTaskLinks(links []TaskLink) []TaskLink {
	latest := map[string]TaskLink{}
	for _, l := range links {
		latest[TaskLinkSource(l.Source)] = l
	}
	var out []TaskLink
	for _, l := range links {
		if latest[TaskLinkSource(l.Source)].Number == l.Number && l.Op == TaskLinkAdd {
			out = append(out, l)
		}
	}
	return out
}

// TaskLinkSource names a linked document as <repo>/<task>/<name>, the form guildhall task link --document takes.
func TaskLinkSource(d DocumentRef) string { return d.Repository + "/" + d.TaskID + "/" + d.Name }

// TaskLinkRecord is the receipt record name of link record number.
func TaskLinkRecord(number int) string { return "task-link/" + strconv.Itoa(number) }

// taskLinkKey zero-pads the number so records sort numerically.
func taskLinkKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
