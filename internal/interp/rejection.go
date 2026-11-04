package interp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// resultRejection marks only failures attributable to the result's contract,
// never an operational failure reading or writing Guildhall's own records.
type resultRejection struct{ error }

func (e *resultRejection) Unwrap() error { return e.error }
func rejectOutput(err error) error       { return &resultRejection{err} }

// maxUnackedListed bounds the message ids an unacknowledged-message rejection names.
const maxUnackedListed = 10

// unacknowledged is the rejection of a succeeded result while steering messages to its attempt are unacknowledged
// (docs/messaging.md#steering-an-attempt): ids names them, in send order, at most maxUnackedListed, and more reports
// that others remain. The Artisan clears it by acknowledging each, so `guildhall result submit` spends no
// resubmission on it.
type unacknowledged struct {
	ids  []string
	more bool
}

func (e *unacknowledged) Error() string {
	noun := "message"
	if len(e.ids) > 1 || e.more {
		noun = "messages"
	}
	list := strings.Join(e.ids, ", ")
	if e.more {
		list += " and more"
	}
	return fmt.Sprintf("unacknowledged steering %s to this attempt: %s; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again", noun, list)
}

// checkMessages refuses a succeeded result of attempt n of step s while a message to the attempt's own inbox is
// unacknowledged. A store read that fails is operational, never a rejection.
func (r *run) checkMessages(s workflow.Step, n int) error {
	to := store.Recipient{Role: store.RecipientArtisan, Repository: r.task.Repository, TaskID: r.task.ID, Step: s.ID, Attempt: n}
	msgs, more, err := r.e.Records.Inbox(to, 0, maxUnackedListed)
	if err != nil {
		return fmt.Errorf("inbox of attempt %d of %s: %w", n, s.ID, err)
	}
	if len(msgs) == 0 {
		return nil
	}
	u := &unacknowledged{more: more}
	for _, m := range msgs {
		u.ids = append(u.ids, m.ID)
	}
	return rejectOutput(u)
}

func (r *run) rejectResult(s workflow.Step, a *store.StageAttempt, cause error) error {
	// An interrupt is not evidence that the result is invalid.
	if err := r.ctx.Err(); err != nil {
		return err
	}
	reason := cause.Error()
	if _, err := r.e.Records.RejectAttempt(r.mutation("intake-rejection", s.ID, strconv.Itoa(a.Number)), r.task.Repository, r.task.ID, s.ID, a.Number, reason); err != nil {
		return fmt.Errorf("interp: reject result of %s attempt %d: %w", s.ID, a.Number, err)
	}
	a.Outcome, a.IntakeRejection = store.AttemptFailed, reason
	// No intake marker is required: the durable failed outcome drives the normal
	// replay-safe Director notification, including after a crash right here.
	return nil
}
