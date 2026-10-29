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

	"github.com/ndrwl/guildhall/internal/digest"
)

// bucketStartQueue is the per-repository bucket of the start queue (docs/store.md#the-start-queue): one entry per
// task and step, keyed <task>/<step>.
var bucketStartQueue = []byte("start-queue")

// StartRequestKind names the request that asked for a queued start.
type StartRequestKind string

const (
	// StartByTask is a plain `task start`, or the supervisor's start-queue watcher advancing the task as it does.
	StartByTask StartRequestKind = "start"
	// StartByDecision is a `task start --decision` answer.
	StartByDecision StartRequestKind = "decision"
	// StartByRetry is a `step retry` of the step's latest attempt, StartRequest.Retried.
	StartByRetry StartRequestKind = "retry"
	// StartBySendBack is a `step send-back`.
	StartBySendBack StartRequestKind = "send-back"
	// StartByEngine is one of the engine's own advances, such as the launcher's after an attempt ends or a loop
	// back from a review.
	StartByEngine StartRequestKind = "engine"
)

// StartRequest is the request a start is made under, which a queued start records (AttemptCaps.Queue).
type StartRequest struct {
	Kind StartRequestKind `json:"request"`
	// Retried is the attempt a retry retries; zero for every other kind.
	Retried int `json:"retried,omitempty"`
}

func (q StartRequest) validate() error {
	switch q.Kind {
	case StartByTask, StartByDecision, StartBySendBack, StartByEngine:
		if q.Retried != 0 {
			return invalid("a %s start names no retried attempt", q.Kind)
		}
	case StartByRetry:
		if q.Retried < 1 {
			return invalid("a retry start names the retried attempt, not %d", q.Retried)
		}
	default:
		return invalid("start request %q is not one of start, decision, retry, send-back and engine", q.Kind)
	}
	return nil
}

// StartQueueEntry is one queued start: attempt Attempt of a task's step, which the running cap did not admit, queued
// at QueuedAt by the store's clock under the request that asked for it. A task has at most one entry per step;
// queuing the same attempt again keeps its first QueuedAt and request.
type StartQueueEntry struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	StartRequest
	QueuedAt time.Time `json:"queued_at"`
}

// QueuedStart is a start-queue entry as StartQueue reads it, with its task's priority read at the same time.
type QueuedStart struct {
	StartQueueEntry
	Priority Priority `json:"priority"`
}

// Is reports whether e queues a start of the task's step.
func (e QueuedStart) Is(repository, taskID, step string) bool {
	return e.Repository == repository && e.TaskID == taskID && e.Step == step
}

// OrderStartQueue sorts entries into queue order: priority, then queue time, then task id, then repository and step.
// It is the start queue's one ordering rule, shared by the store and its doubles; with priority read live, `task
// priority` reorders the queue at once.
func OrderStartQueue(entries []QueuedStart) {
	slices.SortStableFunc(entries, func(a, b QueuedStart) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), a.QueuedAt.Compare(b.QueuedAt), cmp.Compare(a.TaskID, b.TaskID),
			cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Step, b.Step))
	})
}

// ErrStartQueued reports a start queued rather than recorded; the error is a *StartQueued.
var ErrStartQueued = errors.New("start queued behind the running cap")

// StartQueued is the error a start returns when it is queued (AttemptCaps.Queue): Entry is its queue entry and
// Position its 1-based place in the queue at the time of the start. It wraps ErrStartQueued.
type StartQueued struct {
	Entry    StartQueueEntry
	Position int
}

func (e *StartQueued) Error() string {
	return fmt.Sprintf("%v: attempt %d of step %s of %s/%s is at place %d, queued at %s", ErrStartQueued, e.Entry.Attempt,
		e.Entry.Step, e.Entry.Repository, e.Entry.TaskID, e.Position, e.Entry.QueuedAt.Format(time.RFC3339))
}

func (e *StartQueued) Unwrap() error { return ErrStartQueued }

// StartQueueRecord is the receipt record of queuing a step's start and of leaving the queue: start-queue/<step>.
func StartQueueRecord(step string) string { return "start-queue/" + step }

// QueueStartMutation is the mutation id a queued start of attempt n of a task's step is written under,
// <repository>.<task>.queue-start.<step>-<n>, hashed when longer than a mutation id may be. The start's own mutation
// id stays unrecorded until it is admitted.
func QueueStartMutation(repository, taskID, step string, n int) string {
	id := fmt.Sprintf("%s.%s.queue-start.%s-%d", repository, taskID, step, n)
	if len(id) > 128 {
		id = "queue-start." + digest.Of([]byte(id)).Hex()
	}
	return id
}

func startQueueKey(taskID, step string) []byte { return recordKey(taskID, step) }

// enqueueStart writes or keeps the queue entry of the next attempt of a task's step and records it under
// QueueStartMutation, returning the entry and its place.
func (s *Store) enqueueStart(tx *bolt.Tx, repository, taskID, step string, req StartRequest) (*StartQueued, error) {
	last, err := lastAttemptNumber(tx, repository, taskID, step)
	if err != nil {
		return nil, err
	}
	b, err := createRepoBucket(tx, repository, bucketStartQueue)
	if err != nil {
		return nil, err
	}
	key := startQueueKey(taskID, step)
	e := StartQueueEntry{Repository: repository, TaskID: taskID, Step: step, Attempt: last + 1, StartRequest: req, QueuedAt: s.now().UTC()}
	var prior StartQueueEntry
	switch err := getJSON(b, key, &prior); {
	case err == nil && prior.Attempt == e.Attempt:
		e = prior
	case err != nil && !errors.Is(err, ErrNotFound):
		return nil, err
	}
	if err := putJSON(b, key, e); err != nil {
		return nil, err
	}
	qreq := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Step       string `json:"step"`
		Attempt    int    `json:"attempt"`
	}{repository, taskID, step, e.Attempt}
	const op = "queue-start"
	d, err := requestDigest(op, qreq)
	if err != nil {
		return nil, err
	}
	_, err = s.commit(tx, QueueStartMutation(repository, taskID, step, e.Attempt), op, d, func(_ *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = repository, taskID, e.Attempt, StartQueueRecord(step)
		return nil
	})
	if err != nil {
		return nil, err
	}
	q, err := readStartQueue(tx)
	if err != nil {
		return nil, err
	}
	return &StartQueued{Entry: e, Position: slices.IndexFunc(q, func(qs QueuedStart) bool { return qs.Is(repository, taskID, step) }) + 1}, nil
}

// dequeueStart removes a task's step from the start queue, if it is there.
func dequeueStart(tx *bolt.Tx, repository, taskID, step string) error {
	if b := repoBucket(tx, repository, bucketStartQueue); b != nil {
		return b.Delete(startQueueKey(taskID, step))
	}
	return nil
}

// lastAttemptNumber is the number of the latest attempt of a task's step, zero when it has none.
func lastAttemptNumber(tx *bolt.Tx, repository, taskID, step string) (int, error) {
	b := repoBucket(tx, repository, bucketAttempts)
	if b == nil {
		return 0, nil
	}
	prefix := recordKey(taskID, step, "")
	var last StageAttempt
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		if err := decode(k, v, &last); err != nil {
			return 0, err
		}
	}
	return last.Number, nil
}

// StartQueue reads the start queue of every repository in one transaction, in queue order (OrderStartQueue).
func (s *Store) StartQueue() ([]QueuedStart, error) {
	var q []QueuedStart
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		q, err = readStartQueue(tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: start queue: %w", err)
	}
	return q, nil
}

// readStartQueue reads every repository's queue entries with their tasks' priorities, in queue order. Entries of
// closed tasks, and entries whose attempt was since recorded, are left out, as readMergeQueue leaves out closed
// tasks, so they never hold the front.
func readStartQueue(tx *bolt.Tx) ([]QueuedStart, error) {
	q := []QueuedStart{}
	err := tx.Bucket(bucketRepos).ForEachBucket(func(repo []byte) error {
		b := repoBucket(tx, string(repo), bucketStartQueue)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var e StartQueueEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("start queue entry %q: %w", k, err)
			}
			t, err := getTask(tx, e.Repository, e.TaskID)
			if err != nil {
				return err
			}
			if t.State != TaskOpen {
				return nil
			}
			if last, err := lastAttemptNumber(tx, e.Repository, e.TaskID, e.Step); err != nil {
				return err
			} else if last >= e.Attempt {
				return nil
			}
			p := DefaultPriority
			var tp TaskPriority
			switch err := getJSON(repoBucket(tx, e.Repository, bucketTaskPriorities), []byte(e.TaskID), &tp); {
			case err == nil:
				p = tp.Priority
			case !errors.Is(err, ErrNotFound):
				return err
			}
			q = append(q, QueuedStart{StartQueueEntry: e, Priority: p})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	OrderStartQueue(q)
	return q, nil
}

// LeaveStartQueueMutation is the mutation id under which e leaves the start queue: it names e's queue time, so an
// entry queued again after it left leaves under a new id.
func LeaveStartQueueMutation(e StartQueueEntry) string {
	id := fmt.Sprintf("%s.%s.leave-start-queue.%s-%d.%d", e.Repository, e.TaskID, e.Step, e.Attempt, e.QueuedAt.UnixNano())
	if len(id) > 128 {
		id = "leave-start-queue." + digest.Of([]byte(id)).Hex()
	}
	return id
}

// LeaveStartQueue removes e from the start queue, as the supervisor's start-queue watcher does for a queued start
// that no longer starts or queues when its task advances. Only the entry e names is removed, matched by attempt and
// queue time; one that is gone or was queued again since changes nothing.
func (s *Store) LeaveStartQueue(mutationID string, e StartQueueEntry) (Receipt, error) {
	if err := validateIDs(e.Repository, e.TaskID, e.Step); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string    `json:"repository"`
		TaskID     string    `json:"task_id"`
		Step       string    `json:"step"`
		Attempt    int       `json:"attempt"`
		QueuedAt   time.Time `json:"queued_at"`
	}{e.Repository, e.TaskID, e.Step, e.Attempt, e.QueuedAt.UTC()}
	return s.mutate(mutationID, "leave-start-queue", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = e.Repository, e.TaskID, e.Attempt, StartQueueRecord(e.Step)
		if _, err := getTask(tx, e.Repository, e.TaskID); err != nil {
			return err
		}
		b := repoBucket(tx, e.Repository, bucketStartQueue)
		if b == nil {
			return nil
		}
		var cur StartQueueEntry
		switch err := getJSON(b, startQueueKey(e.TaskID, e.Step), &cur); {
		case errors.Is(err, ErrNotFound):
			return nil
		case err != nil:
			return err
		case cur.Attempt != e.Attempt || !cur.QueuedAt.Equal(e.QueuedAt):
			return nil
		}
		return b.Delete(startQueueKey(e.TaskID, e.Step))
	})
}
