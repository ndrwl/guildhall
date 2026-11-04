package interp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

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

// checkMessages refuses a succeeded result of attempt n of step s while a message to the attempt's own inbox that
// was sent before the result was published is unacknowledged (resultCutoff). A message sent after it, which the
// Artisan could never have read, does not count and stays unacknowledged; intake reports it (reportLateSteering). A
// store read that fails is operational, never a rejection.
func (r *run) checkMessages(s workflow.Step, n int) error {
	cutoff, err := r.resultCutoff(s.ID, n)
	if err != nil {
		return err
	}
	// The inbox is in send order, so the messages sent before the cutoff come first within the page.
	msgs, more, err := r.e.Records.Inbox(r.artisanInbox(s.ID, n), 0, maxUnackedListed)
	if err != nil {
		return fmt.Errorf("inbox of attempt %d of %s: %w", n, s.ID, err)
	}
	u := &unacknowledged{more: more}
	for _, m := range msgs {
		if late(m, cutoff) {
			// Every later message in send order was sent later still.
			u.more = false
			break
		}
		u.ids = append(u.ids, m.ID)
	}
	if len(u.ids) == 0 {
		return nil
	}
	return rejectOutput(u)
}

func (r *run) artisanInbox(step string, n int) store.Recipient {
	return store.Recipient{Role: store.RecipientArtisan, Repository: r.task.Repository, TaskID: r.task.ID, Step: step, Attempt: n}
}

// resultCutoff is the publication time of attempt n of step's result file, its modification time, or the zero time
// when it is not published, as when `guildhall result submit` checks a draft: then every message counts.
func (r *run) resultCutoff(step string, n int) (time.Time, error) {
	fi, err := os.Stat(r.resultFile(step, n))
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("result of attempt %d of %s: %w", n, step, err)
	}
	return fi.ModTime(), nil
}

// late reports whether m was sent after cutoff, a result's publication time. A message with no send time, stored
// before send times were recorded, and a message sent at the cutoff itself are not late, so a doubtful order refuses.
func late(m store.Message, cutoff time.Time) bool {
	return !cutoff.IsZero() && !m.SentAt.IsZero() && m.SentAt.After(cutoff)
}

// reportLateSteering tells the Director, once per attempt, of the unacknowledged messages to attempt n of step s that
// were sent after its result was published, so intake did not count them and the Artisan never read them. They stay
// unacknowledged, so a replay finds them again, and the message id comes from the attempt, so it sends nothing new.
func (r *run) reportLateSteering(s workflow.Step, n int) error {
	cutoff, err := r.resultCutoff(s.ID, n)
	if err != nil || cutoff.IsZero() {
		return err
	}
	var ids []string
	more := false
	for after := uint64(0); ; {
		msgs, next, err := r.e.Records.Inbox(r.artisanInbox(s.ID, n), after, store.MaxInboxLimit)
		if err != nil {
			return fmt.Errorf("inbox of attempt %d of %s: %w", n, s.ID, err)
		}
		for _, m := range msgs {
			if !late(m, cutoff) {
				continue
			}
			if len(ids) == maxUnackedListed {
				more = true
				break
			}
			ids = append(ids, m.ID)
		}
		if more || !next || len(msgs) == 0 {
			break
		}
		after = msgs[len(msgs)-1].Sequence
	}
	if len(ids) == 0 {
		return nil
	}
	noun, verb := "message", "it"
	if len(ids) > 1 || more {
		noun, verb = "messages", "them"
	}
	list := strings.Join(ids, ", ")
	if more {
		list += " and more"
	}
	text := fmt.Sprintf("Steering %s %s to attempt %d of %s arrived after the result, so the Artisan did not read %s and intake did not count %s.", noun, list, n, s.ID, verb, verb)
	return r.notify(fmt.Sprintf("late-steering.%s-%d", s.ID, n), store.MessageLateSteering, text)
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
