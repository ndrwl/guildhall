package store

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// mergeBatchKey holds the queue's batch record in the merge-queue bucket, beside the front.
var mergeBatchKey = []byte(" batch")

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
	// Stays counts the times the task became the front or a member of the front's batch.
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
// order, and the front, nil when no task holds it. Batch is the batch the standing front leads, nil when it leads
// none, as always while batching is off. Out are the entries of tasks out of the queue that keep their
// ready time, and Holds every task's hold-merge, held or not.
type MergeQueue struct {
	Entries []QueuedEntry     `json:"entries"`
	Front   *MergeFront       `json:"front,omitempty"`
	Batch   *MergeBatch       `json:"batch,omitempty"`
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

// LeaveMergeQueue takes a task out of the queue, releasing the front if it holds it, with the batch it leads, and
// taking it out of a batch it is a member of. With keepReady the entry is
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
		if err := leaveBatch(b, taskID, true); err != nil {
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

// releaseFront gives up the front taskID holds, with the batch it leads; a landed batch stays until its members
// finish.
func releaseFront(b *bolt.Bucket, taskID string) error {
	var f MergeFront
	switch err := getJSON(b, mergeFrontKey, &f); {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	case f.TaskID == taskID:
		var mb MergeBatch
		switch err := getJSON(b, mergeBatchKey, &mb); {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return err
		case mb.State != BatchLanded:
			if err := b.Delete(mergeBatchKey); err != nil {
				return err
			}
		}
		return b.Delete(mergeFrontKey)
	}
	return nil
}

// ReleaseMergeFront gives up the front a task holds, with the batch it leads, leaving it queued, as a held task does;
// a task not at the front changes nothing.
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
		return claimFront(tx, repository, taskID, 0, 1)
	})
}

// ClaimMergeBatch claims the front for taskID as ClaimMergeFront does and, with size above 1, makes it the leader of
// a batch (docs/interpreter.md#the-merge-queue): in the same write it claims the next tasks in queue order that are
// not held, up to size members with the leader, each counting a stay, and records the batch assembling. Held tasks
// are skipped and keep their place. slice is the leader's slice; the other members' are recorded with their links.
// A size of 1 or less claims the front alone and records no batch, exactly as ClaimMergeFront. A claim by the task
// at the front changes nothing, and a claim by any other task is refused with ErrMergeQueued, recording nothing.
func (s *Store) ClaimMergeBatch(mutationID, repository, taskID string, slice, size int) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if slice < 1 {
		return Receipt{}, invalid("slice %d is below 1", slice)
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Slice      int    `json:"slice"`
		Size       int    `json:"size"`
	}{repository, taskID, slice, size}
	return s.mutate(mutationID, "claim-merge-batch", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		return claimFront(tx, repository, taskID, slice, size)
	})
}

// claimFront claims a free front for taskID, the first queued task not held, and, with size above 1, the batch it
// leads with the next tasks not held, counting each one's stay. The task at the front claims nothing again.
func claimFront(tx *bolt.Tx, repository, taskID string, slice, size int) error {
	q, err := readMergeQueue(tx, repository)
	if err != nil {
		return err
	}
	if q.Front != nil && q.Front.TaskID == taskID {
		return nil
	}
	// A landed batch holds the queue until each of its members has finished.
	if q.Batch != nil && q.Batch.State == BatchLanded {
		return fmt.Errorf("%w: %s/%s waits for the landed merge batch task %s led to finish", ErrMergeQueued, repository, taskID, q.Batch.Leader)
	}
	// Held tasks keep their place but are skipped: the first task not held may claim a free front.
	first := slices.IndexFunc(q.Entries, func(e QueuedEntry) bool { return !e.Held })
	if q.Front != nil || first < 0 || q.Entries[first].TaskID != taskID {
		return fmt.Errorf("%w: %s/%s is at place %d", ErrMergeQueued, repository, taskID, q.Position(taskID))
	}
	b := repoBucket(tx, repository, bucketMergeQueue)
	// A batch whose front no longer stands is stale: a new batch replaces it, and a lone front clears it.
	if err := b.Delete(mergeBatchKey); err != nil {
		return err
	}
	var members []MergeBatchMember
	for _, qe := range q.Entries[first:] {
		if len(members) == max(size, 1) {
			break
		}
		if qe.Held {
			continue
		}
		e := qe.MergeQueueEntry
		e.Stays++
		if err := putJSON(b, []byte(e.TaskID), e); err != nil {
			return err
		}
		m := MergeBatchMember{TaskID: e.TaskID, Stay: e.Stays}
		if e.TaskID == taskID {
			m.Slice = slice
		}
		members = append(members, m)
	}
	if err := putJSON(b, mergeFrontKey, MergeFront{TaskID: taskID, Stay: members[0].Stay}); err != nil {
		return err
	}
	if size <= 1 {
		return nil
	}
	return putJSON(b, mergeBatchKey, MergeBatch{Leader: taskID, Stay: members[0].Stay, Members: members, State: BatchAssembling})
}

// MergeBatchState is where a batch is in its run through the queue (docs/interpreter.md#the-merge-queue).
type MergeBatchState string

// The batch states. A batch is assembled by its members' replays, tested on its last link's checks, and landed or
// found red; a red batch is bisected on its members' link checks and its green members tested again, and a batch
// whose base moved before it landed is assembled again.
const (
	BatchAssembling MergeBatchState = "assembling"
	BatchTesting    MergeBatchState = "testing"
	BatchLanding    MergeBatchState = "landing"
	BatchLanded     MergeBatchState = "landed"
	BatchRed        MergeBatchState = "red"
)

// batchMoves are the states a batch may move to from each state; staying in a state is always allowed.
var batchMoves = map[MergeBatchState][]MergeBatchState{
	BatchAssembling: {BatchTesting},
	BatchTesting:    {BatchAssembling, BatchLanding, BatchRed},
	BatchLanding:    {BatchAssembling, BatchLanded},
	BatchLanded:     {},
	BatchRed:        {BatchAssembling, BatchTesting, BatchLanding},
}

// The verdicts a member of a red batch records of its own link's checks (SetMergeBatchVerdict).
const (
	VerdictPassed = "passed"
	VerdictFailed = "failed"
)

// MergeBatchMember is one task of a batch, in its stay'th time at or behind the front. Slice is the slice the member
// merges, 0 until it is recorded; Link is the member's link head, its head replayed onto the previous member's link
// (the first member's onto the base), empty until it is published. Ready reports that the member passed its merge
// gates for its link while the batch is landing; Finished that it recorded the landed train's merge and left the
// queue. Verdict is the member's read of its own link's checks once the batch's train was red, passed or failed,
// empty until recorded and cleared with its link.
type MergeBatchMember struct {
	TaskID   string `json:"task_id"`
	Slice    int    `json:"slice,omitempty"`
	Stay     int    `json:"stay"`
	Link     string `json:"link,omitempty"`
	Ready    bool   `json:"ready,omitempty"`
	Finished bool   `json:"finished,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
}

// MergeTrain is a batch's train pull request: the remote and pull request it is, the head it was last pushed at, and
// the merge commit once it merged.
type MergeTrain struct {
	Remote      string `json:"remote"`
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Head        string `json:"head"`
	MergeCommit string `json:"merge_commit,omitempty"`
}

// MergeBatch is the batch the front leads: its members in queue order, the leader first; the base tip it was
// assembled on, empty until recorded; its state; and its train pull request, nil until it is opened. Stay is the
// leader's front stay the batch was claimed in.
type MergeBatch struct {
	Leader  string             `json:"leader"`
	Stay    int                `json:"stay"`
	Base    string             `json:"base,omitempty"`
	Members []MergeBatchMember `json:"members"`
	State   MergeBatchState    `json:"state"`
	Train   *MergeTrain        `json:"train,omitempty"`
}

// Member returns the 1-based place of task in the batch, 0 when it is not a member.
func (b MergeBatch) Member(task string) int {
	return slices.IndexFunc(b.Members, func(m MergeBatchMember) bool { return m.TaskID == task }) + 1
}

// leaveBatch takes taskID out of the batch: the leader's leaving ends the batch, and a member's removes it alone and
// clears the links of the members after it, which replay onto its predecessor's link, sending a batch testing or
// landing back to assembling. A member leaving a red batch, as its culprit does, takes the members after it out too,
// leaving them queued, so the members before it are tested again on their own links. A landed batch keeps its members: with finish, as when a member leaves the queue after
// recording the train's merge, the member is marked finished, and the batch is removed once every member is.
func leaveBatch(b *bolt.Bucket, taskID string, finish bool) error {
	var mb MergeBatch
	switch err := getJSON(b, mergeBatchKey, &mb); {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	case mb.State == BatchLanded:
		i := mb.Member(taskID)
		if i == 0 || !finish {
			return nil
		}
		mb.Members[i-1].Finished = true
		if !slices.ContainsFunc(mb.Members, func(m MergeBatchMember) bool { return !m.Finished }) {
			return b.Delete(mergeBatchKey)
		}
		return putJSON(b, mergeBatchKey, mb)
	case mb.Leader == taskID:
		return b.Delete(mergeBatchKey)
	}
	i := mb.Member(taskID)
	if i == 0 {
		return nil
	}
	if mb.State == BatchRed {
		mb.Members = mb.Members[:i-1]
		return putJSON(b, mergeBatchKey, mb)
	}
	mb.Members = slices.Delete(mb.Members, i-1, i)
	clearLinks(&mb, i-1)
	return putJSON(b, mergeBatchKey, mb)
}

// clearLinks clears the links and verdicts of the batch's members from index i on, which replayed onto a link that
// no longer precedes them; a batch testing, landing or red goes back to assembling, since its train no longer holds
// them all.
func clearLinks(mb *MergeBatch, i int) {
	for j := i; j < len(mb.Members); j++ {
		mb.Members[j].Link, mb.Members[j].Verdict = "", ""
	}
	if mb.State == BatchTesting || mb.State == BatchLanding || mb.State == BatchRed {
		mb.State = BatchAssembling
	}
	clearReady(mb)
}

// clearReady clears every member's readiness unless the batch is landing, the only state readiness holds in.
func clearReady(mb *MergeBatch) {
	if mb.State == BatchLanding {
		return
	}
	for j := range mb.Members {
		mb.Members[j].Ready = false
	}
}

// standingBatch reads the standing batch of repository, failing with ErrInvalid when none stands or taskID is not
// its member.
func standingBatch(tx *bolt.Tx, repository, taskID string) (*bolt.Bucket, MergeBatch, error) {
	q, err := readMergeQueue(tx, repository)
	if err != nil {
		return nil, MergeBatch{}, err
	}
	if q.Batch == nil || q.Batch.Member(taskID) == 0 {
		return nil, MergeBatch{}, invalid("task %s/%s is not a member of a standing merge batch", repository, taskID)
	}
	return repoBucket(tx, repository, bucketMergeQueue), *q.Batch, nil
}

// LeaveMergeBatch takes a task out of its batch and leaves it queued, as a member does on conflicts, a hold, a cancel
// or a running attempt, clearing the links after it; the leader's leaving ends the batch but keeps its front. A task
// in no batch, or in a landed one, changes nothing.
func (s *Store) LeaveMergeBatch(mutationID, repository, taskID string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
	}{repository, taskID}
	return s.mutate(mutationID, "leave-merge-batch", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		if b := repoBucket(tx, repository, bucketMergeQueue); b != nil {
			return leaveBatch(b, taskID, false)
		}
		return nil
	})
}

// SetMergeBatchLink records a member's slice and link head in the standing batch. The task must be a member, the
// slice at least 1 and the link a full commit id, and the batch not landed. A changed link clears the member's
// verdict and the links of the members after it, since they replay onto it again, and sends a batch testing, landing
// or red back to assembling.
func (s *Store) SetMergeBatchLink(mutationID, repository, taskID string, slice int, link string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if slice < 1 {
		return Receipt{}, invalid("slice %d is below 1", slice)
	}
	if !isCommitID(link) {
		return Receipt{}, invalid("link %q is not a full commit id", link)
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Slice      int    `json:"slice"`
		Link       string `json:"link"`
	}{repository, taskID, slice, link}
	return s.mutate(mutationID, "set-merge-batch-link", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		b, mb, err := standingBatch(tx, repository, taskID)
		if err != nil {
			return err
		}
		if mb.State == BatchLanded {
			return invalid("the merge batch of %s has landed", repository)
		}
		i := mb.Member(taskID) - 1
		if mb.Members[i].Link != link {
			mb.Members[i].Verdict = ""
			clearLinks(&mb, i+1)
		}
		mb.Members[i].Slice, mb.Members[i].Link = slice, link
		return putJSON(b, mergeBatchKey, mb)
	})
}

// SetMergeBatchState moves the standing batch leader leads to state, along the moves batchMoves allows, and records
// base, the tip the batch is assembled on, when it is not empty. A task that does not lead the batch, an unknown
// state, a move not allowed and a base that is not a full commit id are ErrInvalid. Leaving landing clears every
// member's readiness.
func (s *Store) SetMergeBatchState(mutationID, repository, leader string, state MergeBatchState, base string) (Receipt, error) {
	if err := validateIDs(repository, leader); err != nil {
		return Receipt{}, err
	}
	if _, ok := batchMoves[state]; !ok {
		return Receipt{}, invalid("unknown merge batch state %q", state)
	}
	if base != "" && !isCommitID(base) {
		return Receipt{}, invalid("base %q is not a full commit id", base)
	}
	req := struct {
		Repository string          `json:"repository"`
		Leader     string          `json:"leader"`
		State      MergeBatchState `json:"state"`
		Base       string          `json:"base,omitempty"`
	}{repository, leader, state, base}
	return s.mutate(mutationID, "set-merge-batch-state", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, leader, MergeQueueRecord
		b, mb, err := standingBatch(tx, repository, leader)
		if err != nil {
			return err
		}
		if mb.Leader != leader {
			return invalid("task %s/%s does not lead the merge batch of %s; %s does", repository, leader, repository, mb.Leader)
		}
		if mb.State != state && !slices.Contains(batchMoves[mb.State], state) {
			return invalid("the merge batch of %s cannot move from %s to %s", repository, mb.State, state)
		}
		mb.State = state
		if base != "" {
			mb.Base = base
		}
		clearReady(&mb)
		return putJSON(b, mergeBatchKey, mb)
	})
}

// SetMergeBatchTrain records the train pull request of the standing batch leader leads: its remote, number, URL and
// head, and its merge commit once it merged. A task that does not lead the batch, a number below 1, a head or merge
// commit that is not a full commit id, a train that changes once its merge is recorded, and a merge commit for a
// batch that is not landing are ErrInvalid. A train pushed to a new head while the batch is landing sends it back to
// testing.
func (s *Store) SetMergeBatchTrain(mutationID, repository, leader string, train MergeTrain) (Receipt, error) {
	if err := validateIDs(repository, leader); err != nil {
		return Receipt{}, err
	}
	switch {
	case train.Remote == "" || len(train.Remote) > 2048 || len(train.URL) > 2048:
		return Receipt{}, invalid("a train names its remote, and its remote and URL are at most 2048 bytes")
	case train.Number < 1:
		return Receipt{}, invalid("train pull request number %d is below 1", train.Number)
	case !isCommitID(train.Head):
		return Receipt{}, invalid("train head %q is not a full commit id", train.Head)
	case train.MergeCommit != "" && !isCommitID(train.MergeCommit):
		return Receipt{}, invalid("train merge commit %q is not a full commit id", train.MergeCommit)
	}
	req := struct {
		Repository string     `json:"repository"`
		Leader     string     `json:"leader"`
		Train      MergeTrain `json:"train"`
	}{repository, leader, train}
	return s.mutate(mutationID, "set-merge-batch-train", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, leader, MergeQueueRecord
		b, mb, err := standingBatch(tx, repository, leader)
		if err != nil {
			return err
		}
		switch {
		case mb.Leader != leader:
			return invalid("task %s/%s does not lead the merge batch of %s; %s does", repository, leader, repository, mb.Leader)
		case mb.Train != nil && mb.Train.MergeCommit != "":
			if *mb.Train != train {
				return invalid("the train of the merge batch of %s has merged", repository)
			}
			return nil
		case train.MergeCommit != "" && mb.State != BatchLanding:
			return invalid("the merge batch of %s is %s, not landing", repository, mb.State)
		}
		if mb.Train != nil && mb.Train.Head != train.Head && mb.State == BatchLanding {
			mb.State = BatchTesting
			clearReady(&mb)
		}
		mb.Train = &train
		return putJSON(b, mergeBatchKey, mb)
	})
}

// SetMergeBatchReady marks a member of the standing batch ready to land at link, its recorded link: the member passed
// its merge gates for it. The batch must be landing; a task that is not a member, or a link that is not its own, is
// ErrInvalid.
func (s *Store) SetMergeBatchReady(mutationID, repository, taskID, link string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if !isCommitID(link) {
		return Receipt{}, invalid("link %q is not a full commit id", link)
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Link       string `json:"link"`
	}{repository, taskID, link}
	return s.mutate(mutationID, "set-merge-batch-ready", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		b, mb, err := standingBatch(tx, repository, taskID)
		if err != nil {
			return err
		}
		i := mb.Member(taskID) - 1
		switch {
		case mb.State != BatchLanding:
			return invalid("the merge batch of %s is %s, not landing", repository, mb.State)
		case mb.Members[i].Link != link:
			return invalid("link %s is not the link %q of task %s/%s", link, mb.Members[i].Link, repository, taskID)
		}
		mb.Members[i].Ready = true
		return putJSON(b, mergeBatchKey, mb)
	})
}

// SetMergeBatchVerdict records verdict, VerdictPassed or VerdictFailed, as a member's read of the checks of link, its
// recorded link, while the standing batch is red (docs/interpreter.md#red-batches). A batch that is not red, a task
// that is not a member, a link that is not its own, an unknown verdict, and a verdict that changes once recorded are
// ErrInvalid.
func (s *Store) SetMergeBatchVerdict(mutationID, repository, taskID, link, verdict string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if !isCommitID(link) {
		return Receipt{}, invalid("link %q is not a full commit id", link)
	}
	if verdict != VerdictPassed && verdict != VerdictFailed {
		return Receipt{}, invalid("unknown link verdict %q", verdict)
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Link       string `json:"link"`
		Verdict    string `json:"verdict"`
	}{repository, taskID, link, verdict}
	return s.mutate(mutationID, "set-merge-batch-verdict", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, MergeQueueRecord
		b, mb, err := standingBatch(tx, repository, taskID)
		if err != nil {
			return err
		}
		i := mb.Member(taskID) - 1
		switch m := mb.Members[i]; {
		case mb.State != BatchRed:
			return invalid("the merge batch of %s is %s, not red", repository, mb.State)
		case m.Link != link:
			return invalid("link %s is not the link %q of task %s/%s", link, m.Link, repository, taskID)
		case m.Verdict != "" && m.Verdict != verdict:
			return invalid("task %s/%s already recorded its link %s %s", repository, taskID, link, m.Verdict)
		}
		mb.Members[i].Verdict = verdict
		return putJSON(b, mergeBatchKey, mb)
	})
}

// isCommitID reports whether id is a full 40- or 64-character lowercase hexadecimal commit id.
func isCommitID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
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
	var batch *MergeBatch
	err := b.ForEach(func(k, v []byte) error {
		switch string(k) {
		case string(mergeFrontKey):
			front = &MergeFront{}
			return json.Unmarshal(v, front)
		case string(mergeBatchKey):
			batch = &MergeBatch{}
			return json.Unmarshal(v, batch)
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
		// A batch stands with the front stay it was claimed in; members that left the queue or closed drop out, and
		// the links after the first that did are cleared, since they replayed onto a link that is gone.
		if batch != nil && batch.State != BatchLanded && batch.Leader == front.TaskID && batch.Stay == front.Stay {
			gone := func(m MergeBatchMember) bool {
				return !slices.ContainsFunc(q.Entries, func(e QueuedEntry) bool { return e.TaskID == m.TaskID })
			}
			if i := slices.IndexFunc(batch.Members, gone); i >= 0 {
				clearLinks(batch, i)
			}
			batch.Members = slices.DeleteFunc(batch.Members, gone)
			q.Batch = batch
		}
	}
	// A landed batch stands, front or not, while a member that has not finished is still queued: its train merged, so
	// the member records the merge and leaves the queue at its next advance.
	if batch != nil && batch.State == BatchLanded && slices.ContainsFunc(batch.Members, func(m MergeBatchMember) bool {
		return !m.Finished && slices.ContainsFunc(q.Entries, func(e QueuedEntry) bool { return e.TaskID == m.TaskID })
	}) {
		q.Batch = batch
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
