package interp

import (
	"fmt"
	"strconv"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// resultRejection marks only failures attributable to the result's contract,
// never an operational failure reading or writing Guildhall's own records.
type resultRejection struct{ error }

func (e *resultRejection) Unwrap() error { return e.error }
func rejectOutput(err error) error       { return &resultRejection{err} }

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
