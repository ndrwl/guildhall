package store

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketHandoffDues is the nested bucket of the director bucket holding announced handoff crossings, keyed by
// zero-padded receipt sequence.
var bucketHandoffDues = []byte("handoff-dues")

// opRecordHandoffDue is the operation recorded on a handoff-due receipt.
const opRecordHandoffDue = "record-handoff-due"

// HandoffTrigger names an automatic handoff trigger (docs/director.md#automatic-handoff).
type HandoffTrigger string

const (
	// HandoffTasks is [director] handoff_after_tasks: tasks finished since the session was recorded.
	HandoffTasks HandoffTrigger = "tasks"
	// HandoffContext is [director] handoff_context_percent: the context the runtime last reported as used.
	HandoffContext HandoffTrigger = "context"
	// HandoffIdle is [director] handoff_idle: a quiet period with no turn signal and no wake.
	HandoffIdle HandoffTrigger = "idle"
)

// HandoffPolicy is the automatic handoff thresholds a session was started under; zero turns a trigger off.
type HandoffPolicy struct {
	// AfterTasks is the number of finished tasks each crossing of the task trigger takes.
	AfterTasks int `json:"after_tasks,omitempty"`
	// ContextPercent is the used context percent, 1 to 100, that crosses the context trigger.
	ContextPercent int `json:"context_percent,omitempty"`
	// ContextFile is the absolute path of the session's context report, empty when its runtime reports none.
	ContextFile string `json:"context_file,omitempty"`
}

// MaxHandoffReasonBytes bounds a handoff due's reason.
const MaxHandoffReasonBytes = 128

// HandoffDue is one announced crossing of an automatic handoff trigger (docs/store.md#handoff-dues): a wake line,
// or the supervisor's idle line, told the Director a handoff is due.
type HandoffDue struct {
	// Session is the session the crossing belongs to.
	Session int            `json:"session"`
	Trigger HandoffTrigger `json:"trigger"`
	// Crossing counts the trigger's crossings in the session, from 1.
	Crossing int `json:"crossing"`
	// Reason is the text typed in the suffix, such as "context 72%".
	Reason string `json:"reason"`
	// MessageID is the message whose wake carried the line; empty for the idle line.
	MessageID string `json:"message_id,omitempty"`
	// Sequence and Time are set by the store, Time in UTC.
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
}

func handoffDueMutationID(d HandoffDue) string {
	return fmt.Sprintf("handoff-due:%d:%s:%d", d.Session, d.Trigger, d.Crossing)
}

func handoffDueRecord(seq uint64) string {
	return "director/handoff-due/" + strconv.FormatUint(seq, 10)
}

// RecordHandoffDue records crossing d.Crossing of d.Trigger in session d.Session as announced, with the mutation
// id "handoff-due:<session>:<trigger>:<crossing>", so each crossing is recorded once: replaying the same due
// returns the original receipt and recording the crossing again with another reason or message is
// ErrMutationConflict. Sequence and Time are set by the store. The session must exist, otherwise ErrNotFound; an
// unknown trigger, a crossing below 1 or a reason that is empty or too long is ErrInvalid.
func (s *Store) RecordHandoffDue(d HandoffDue) (Receipt, error) {
	return Receipt{}, errors.New("store: RecordHandoffDue: not implemented")
}

// HandoffDues returns the announced crossings of session n in sequence order; none is an empty list.
func (s *Store) HandoffDues(n int) ([]HandoffDue, error) {
	return nil, errors.New("store: HandoffDues: not implemented")
}

// FinishedTasks counts the tasks moved to done or cancelled since session n was recorded, across repositories.
// No session n is ErrNotFound.
func (s *Store) FinishedTasks(n int) (int, error) {
	return 0, errors.New("store: FinishedTasks: not implemented")
}

var _ = bolt.Tx{}
