package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	if err := validateHandoffDue(d); err != nil {
		return Receipt{}, err
	}
	return s.mutate(handoffDueMutationID(d), opRecordHandoffDue, d, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = handoffDueRecord(r.Sequence)
		sessions := directorBucket(tx, bucketSessions)
		if sessions == nil || sessions.Get(sessionKey(d.Session)) == nil {
			return fmt.Errorf("director session %d: %w", d.Session, ErrNotFound)
		}
		dues, err := createDirectorBucket(tx, bucketHandoffDues)
		if err != nil {
			return err
		}
		d.Sequence, d.Time = r.Sequence, s.now().UTC()
		return putJSON(dues, seqKey(d.Sequence), d)
	})
}

func validateHandoffDue(d HandoffDue) error {
	switch {
	case d.Session < 1:
		return invalid("director session number %d must be positive", d.Session)
	case d.Trigger != HandoffTasks && d.Trigger != HandoffContext && d.Trigger != HandoffIdle:
		return invalid("handoff trigger %q is not %q, %q or %q", d.Trigger, HandoffTasks, HandoffContext, HandoffIdle)
	case d.Crossing < 1:
		return invalid("handoff crossing %d must be positive", d.Crossing)
	case d.Sequence != 0 || !d.Time.IsZero():
		return invalid("handoff due sequence and time are set by the store")
	case strings.ContainsFunc(d.Reason, unicode.IsControl):
		return invalid("handoff due reason must be one line")
	}
	if d.MessageID != "" {
		if err := ValidateMessageID(d.MessageID); err != nil {
			return err
		}
	}
	return validateText("handoff due reason", d.Reason, MaxHandoffReasonBytes)
}

// HandoffDues returns the announced crossings of session n in sequence order; none is an empty list.
func (s *Store) HandoffDues(n int) ([]HandoffDue, error) {
	out := []HandoffDue{}
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketHandoffDues)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var d HandoffDue
			if err := decode(k, v, &d); err != nil {
				return err
			}
			if d.Session == n {
				out = append(out, d)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: handoff dues of session %d: %w", n, err)
	}
	return out, nil
}

// FinishedTasks counts the tasks moved to done or cancelled since session n was recorded, across repositories.
// No session n is ErrNotFound.
func (s *Store) FinishedTasks(n int) (int, error) {
	count := 0
	err := s.view(func(tx *bolt.Tx) error {
		receipts := tx.Bucket(bucketReceipts)
		var started Receipt
		if err := getJSON(receipts, []byte(directorSessionMutationID(n)), &started); err != nil || started.Operation != opStartDirectorSession {
			return fmt.Errorf("director session %d: %w", n, ErrNotFound)
		}
		return receipts.ForEach(func(k, raw []byte) error {
			var r Receipt
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("receipt %q: %w", k, err)
			}
			if r.Operation == opSetTaskState && r.Sequence > started.Sequence {
				count++
			}
			return nil
		})
	})
	if err != nil {
		return 0, fmt.Errorf("store: finished tasks of session %d: %w", n, err)
	}
	return count, nil
}
