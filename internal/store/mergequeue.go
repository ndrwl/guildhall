package store

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Per-repository buckets of the merge queue (docs/interpreter.md#the-merge-queue).
var (
	bucketMergeQueue = []byte("merge-queue")
	bucketMergeHolds = []byte("merge-holds")
)

// mergeFrontKey holds the queue's front record in the merge-queue bucket, beside one entry per task id; no task id
// starts with a space.
var mergeFrontKey = []byte(" front")

// MergeQueueEntry is one task's place in its repository's merge queue. An entry outlives the task's stay in the
// queue: a build send-back takes the task out but keeps the entry, so the task rejoins at its first ready time, and
// only a merge or a cancellation removes it.
type MergeQueueEntry struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	// ReadyAt is the commit time of the receipt that first authorized the task's merge; it never changes.
	ReadyAt time.Time `json:"ready_at"`
	// Authorization is the decision or authorization id the task is queued under; empty while it is out.
	Authorization string `json:"authorization,omitempty"`
	// PR is the URL of the task's pull request when it last joined.
	PR string `json:"pr,omitempty"`
	// Queued reports whether the task waits in the queue now.
	Queued bool `json:"queued"`
	// Stays counts the times the task became the front.
	Stays int `json:"stays"`
}

// MergeFront is the task at the front of a repository's merge queue, in its stay'th time there.
type MergeFront struct {
	TaskID string `json:"task_id"`
	Stay   int    `json:"stay"`
}

// MergeHold is the Director's hold-merge of a task: while Held, the task stays authorized and keeps its ready time
// but is skipped by the queue. History holds every value set, in order.
type MergeHold struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Held       bool   `json:"held"`
	History    []bool `json:"history"`
}

// MergeHoldRecord is the receipt record of a task's hold-merge.
const MergeHoldRecord = "merge-hold"

// MergeQueueRecord is the receipt record of a task's merge-queue entry and of the front it claims or releases.
const MergeQueueRecord = "merge-queue"

// QueuedEntry is a merge-queue entry as MergeQueue reads it, with the task's priority and hold.
type QueuedEntry struct {
	MergeQueueEntry
	Priority Priority `json:"priority"`
	Held     bool     `json:"held"`
	Front    bool     `json:"front"`
}

// MergeQueue is a repository's merge queue as one read: the tasks waiting in it, front first and then in queue
// order, and the front, nil when no task holds it. Out are the entries of tasks out of the queue that keep their
// ready time, and Holds every task's hold-merge, held or not.
type MergeQueue struct {
	Entries []QueuedEntry     `json:"entries"`
	Front   *MergeFront       `json:"front,omitempty"`
	Out     []MergeQueueEntry `json:"-"`
	Holds   map[string]bool   `json:"-"`
}

// Position returns the 1-based place of task in the queue, 0 when it is not queued.
func (q MergeQueue) Position(task string) int {
	return slices.IndexFunc(q.Entries, func(e QueuedEntry) bool { return e.TaskID == task }) + 1
}

// ErrMergeQueued reports a front claim refused because another task is ahead; nothing is recorded.
var ErrMergeQueued = errors.New("another task is ahead in the merge queue")

// OrderMergeQueue sorts entries into queue order: priority, then ready time, then task id. It is the queue's one
// ordering rule, shared by the store and its doubles.
func OrderMergeQueue(entries []QueuedEntry) {
	slices.SortStableFunc(entries, func(a, b QueuedEntry) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), a.ReadyAt.Compare(b.ReadyAt), cmp.Compare(a.TaskID, b.TaskID))
	})
}

// FrontStands reports whether front still holds the front of entries: its task is queued. A held front still stands,
// since its task may be mid-advance, pushing or merging; it releases the front itself at its next advance.
func FrontStands(front *MergeFront, entries []QueuedEntry) bool {
	if front == nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e QueuedEntry) bool { return e.TaskID == front.TaskID })
}

// JoinMergeQueue queues an open task under authorization, the decision or authorization id that allowed its merge.
// A task joining for the first time takes as its ready time the commit time of its receipt with record, which must
// be the answer of a decision ("decision/<id>") or the recording of a document ("document/<name>"); a task that
// rejoins keeps its first ready time. pr is the URL of the task's pull request, shown with the entry. Joining under
// the authorization it is queued under changes nothing.
func (s *Store) JoinMergeQueue(mutationID, repository, taskID, authorization, record, pr string) (Receipt, error) {
	if err := validateIDs(repository, taskID, authorization); err != nil {
		return Receipt{}, err
	}
	if err := validateText("record", record, 256); err != nil {
		return Receipt{}, err
	}
	if len(pr) > 2048 {
		return Receipt{}, invalid("pull request URL is over 2048 bytes")
	}
	req := struct {
		Repository    string `json:"repository"`
		TaskID        string `json:"task_id"`
		Authorization string `json:"authorization"`
		Record        string `json:"record"`
		PR            string `json:"pr,omitempty"`
	}{repository, taskID, authorization, record, pr}
	return s.mutate(mutationID, "join-merge-queue", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketMergeQueue)
		if err != nil {
			return err
		}
		e := MergeQueueEntry{Repository: repository, TaskID: taskID}
		if err := getJSON(b, []byte(taskID), &e); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if e.ReadyAt.IsZero() {
			if e.ReadyAt, err = readyAt(tx, repository, taskID, record); err != nil {
				return err
			}
		}
		e.Authorization, e.Queued, e.PR = authorization, true, pr
		return putJSON(b, []byte(taskID), e)
	})
}

// readyAt is the commit time of the latest receipt of the task whose record is record and that answers a decision
// or records a document.
func readyAt(tx *bolt.Tx, repository, taskID, record string) (time.Time, error) {
	var at time.Time
	var seq uint64
	err := tx.Bucket(bucketReceipts).ForEach(func(k, v []byte) error {
		var r Receipt
		if err := json.Unmarshal(v, &r); err != nil {
			return fmt.Errorf("receipt %q: %w", k, err)
		}
		if r.Repository == repository && r.TaskID == taskID && r.Record == record && (r.Operation == "answer-decision" || r.Operation == "put-document") && r.Sequence > seq {
			at, seq = r.CommittedAt, r.Sequence
		}
		return nil
	})
	if err == nil && seq == 0 {
		err = invalid("task %s/%s has no receipt of record %s to be ready from", repository, taskID, record)
	}
	return at, err
}

// LeaveMergeQueue takes a task out of the queue, releasing the front if it holds it. With keepReady the entry is
// kept, so the task rejoins at its first ready time; without, as for a task that merged or was cancelled, it is
// removed. Leaving a queue the task is not in changes nothing.
func (s *Store) LeaveMergeQueue(mutationID, repository, taskID string, keepReady bool) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		KeepReady  bool   `json:"keep_ready"`
	}{repository, taskID, keepReady}
	return s.mutate(mutationID, "leave-merge-queue", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		if _, err := getTask(tx, repository, taskID); err != nil {
			return err
		}
		b := repoBucket(tx, repository, bucketMergeQueue)
		if b == nil {
			return nil
		}
		if err := releaseFront(b, taskID); err != nil {
			return err
		}
		var e MergeQueueEntry
		switch err := getJSON(b, []byte(taskID), &e); {
		case errors.Is(err, ErrNotFound):
			return nil
		case err != nil:
			return err
		case !keepReady:
			return b.Delete([]byte(taskID))
		}
		e.Authorization, e.Queued = "", false
		return putJSON(b, []byte(taskID), e)
	})
}

func releaseFront(b *bolt.Bucket, taskID string) error {
	var f MergeFront
	switch err := getJSON(b, mergeFrontKey, &f); {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	case f.TaskID == taskID:
		return b.Delete(mergeFrontKey)
	}
	return nil
}

// ReleaseMergeFront gives up the front a task holds, leaving it queued, as a held task does; a task not at the front
// changes nothing.
func (s *Store) ReleaseMergeFront(mutationID, repository, taskID string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
	}{repository, taskID}
	return s.mutate(mutationID, "release-merge-front", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		if b := repoBucket(tx, repository, bucketMergeQueue); b != nil {
			return releaseFront(b, taskID)
		}
		return nil
	})
}

// ClaimMergeFront makes a queued task the front of its repository's queue, in one write, so two tasks advancing at
// once never both hold it. The front stands while its task is queued, held or not, until the task releases it;
// otherwise the first task in queue order that is not held may claim it. A claim by any other task is refused with ErrMergeQueued, recording nothing, and a claim by the
// task at the front changes nothing. A claim counts the task's stays.
func (s *Store) ClaimMergeFront(mutationID, repository, taskID string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
	}{repository, taskID}
	return s.mutate(mutationID, "claim-merge-front", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		q, err := readMergeQueue(tx, repository)
		if err != nil {
			return err
		}
		if q.Front != nil && q.Front.TaskID == taskID {
			return nil
		}
		// Held tasks keep their place but are skipped: the first task not held may claim a free front.
		first := slices.IndexFunc(q.Entries, func(e QueuedEntry) bool { return !e.Held })
		if q.Front != nil || first < 0 || q.Entries[first].TaskID != taskID {
			return fmt.Errorf("%w: %s/%s is at place %d", ErrMergeQueued, repository, taskID, q.Position(taskID))
		}
		b := repoBucket(tx, repository, bucketMergeQueue)
		e := q.Entries[first].MergeQueueEntry
		e.Stays++
		if err := putJSON(b, []byte(taskID), e); err != nil {
			return err
		}
		return putJSON(b, mergeFrontKey, MergeFront{TaskID: taskID, Stay: e.Stays})
	})
}

// MergeQueue reads a repository's merge queue in one transaction.
func (s *Store) MergeQueue(repository string) (MergeQueue, error) {
	var q MergeQueue
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		q, err = readMergeQueue(tx, repository)
		return err
	})
	if err != nil {
		return MergeQueue{}, fmt.Errorf("store: merge queue of %s: %w", repository, err)
	}
	return q, nil
}

// readMergeQueue reads the queue: entries of open tasks with their priority and hold, in queue order with a standing
// front first. A front whose task left the queue or is closed does not stand and is read as no front; a held front
// stands until its task releases it.
func readMergeQueue(tx *bolt.Tx, repository string) (MergeQueue, error) {
	q := MergeQueue{Entries: []QueuedEntry{}, Out: []MergeQueueEntry{}, Holds: map[string]bool{}}
	var holds []MergeHold
	if err := scan(repoBucket(tx, repository, bucketMergeHolds), nil, &holds); err != nil {
		return MergeQueue{}, err
	}
	for _, h := range holds {
		q.Holds[h.TaskID] = h.Held
	}
	b := repoBucket(tx, repository, bucketMergeQueue)
	if b == nil {
		return q, nil
	}
	var front *MergeFront
	err := b.ForEach(func(k, v []byte) error {
		if string(k) == string(mergeFrontKey) {
			front = &MergeFront{}
			return json.Unmarshal(v, front)
		}
		var e MergeQueueEntry
		if err := json.Unmarshal(v, &e); err != nil {
			return fmt.Errorf("merge queue entry %q: %w", k, err)
		}
		t, err := getTask(tx, repository, e.TaskID)
		if err != nil {
			return err
		}
		switch {
		case t.State != TaskOpen:
		case !e.Queued:
			q.Out = append(q.Out, e)
		default:
			p := DefaultPriority
			var tp TaskPriority
			switch err := getJSON(repoBucket(tx, repository, bucketTaskPriorities), []byte(e.TaskID), &tp); {
			case err == nil:
				p = tp.Priority
			case !errors.Is(err, ErrNotFound):
				return err
			}
			q.Entries = append(q.Entries, QueuedEntry{MergeQueueEntry: e, Priority: p, Held: q.Holds[e.TaskID]})
		}
		return nil
	})
	if err != nil {
		return MergeQueue{}, err
	}
	OrderMergeQueue(q.Entries)
	if FrontStands(front, q.Entries) {
		i := slices.IndexFunc(q.Entries, func(e QueuedEntry) bool { return e.TaskID == front.TaskID })
		f := q.Entries[i]
		f.Front = true
		q.Entries = append([]QueuedEntry{f}, slices.Delete(q.Entries, i, i+1)...)
		q.Front = front
	}
	return q, nil
}

// SetMergeHold records whether an open task's merge is held, receipt record merge-hold, appending the value to its
// history. An unknown task is ErrNotFound and a closed one ErrInvalid.
func (s *Store) SetMergeHold(mutationID, repository, taskID string, held bool) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Held       bool   `json:"held"`
	}{repository, taskID, held}
	return s.mutate(mutationID, "set-merge-hold", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeHoldRecord
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketMergeHolds)
		if err != nil {
			return err
		}
		h := MergeHold{Repository: repository, TaskID: taskID}
		if err := getJSON(b, []byte(taskID), &h); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		h.Held, h.History = held, append(h.History, held)
		return putJSON(b, []byte(taskID), h)
	})
}

// MergeHold returns a task's hold-merge record, or ErrNotFound when it was never held.
func (s *Store) MergeHold(repository, taskID string) (MergeHold, error) {
	var h MergeHold
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketMergeHolds), []byte(taskID), &h)
	})
	if err != nil {
		return MergeHold{}, fmt.Errorf("store: merge hold of %s/%s: %w", repository, taskID, err)
	}
	return h, nil
}
