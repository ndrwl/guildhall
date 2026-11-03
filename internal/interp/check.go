package interp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// ErrAttemptNotRunning reports a result check for an attempt that is not its step's running attempt: its task is
// closed, it does not exist, it has an outcome, or a later attempt of its step does.
var ErrAttemptNotRunning = errors.New("attempt is not running")

// ErrReadOnly reports a write a read-only run tried; CheckResult runs read-only, so this is a bug, never a rejection.
var ErrReadOnly = errors.New("a result check writes nothing")

// Rejection is a result intake would refuse: Reason is exactly the text it would record as the attempt's
// IntakeRejection.
type Rejection struct{ Reason string }

func (e *Rejection) Error() string { return e.Reason }

// CheckResult runs intake's checks on data, a result of the running attempt key, and writes nothing (`guildhall
// result check` and `submit`, docs/contracts/cli.md). It parses data against the attempt's identity, as intake reads
// its result file, and checks a succeeded result's outputs with the same code intake runs (checkResult), against the
// attempt's own store records, workflow revision, slices, binding and git state at the moment it runs. A result
// intake would refuse is a *Rejection with intake's reason; an attempt that is not running is ErrAttemptNotRunning;
// any other error, such as a store or git failure, is operational and returned as it is. Intake still checks at
// the end of the attempt, and its verdict is final.
func (e *Engine) CheckResult(ctx context.Context, key artisan.AttemptKey, data []byte) error {
	ro := *e
	ro.Records = readOnlyRecords{e.Records}
	ro.Outbox = nil
	ro.Launcher = nil
	ev := Event{Kind: AttemptResult, Repository: key.Repository, Task: key.TaskID, Step: key.Step, Attempt: key.Attempt}
	r, err := ro.begin(ctx, ev)
	if errors.Is(err, ErrTaskClosed) {
		return fmt.Errorf("interp: %w: %v", ErrAttemptNotRunning, err)
	}
	if err != nil {
		return err
	}
	s, err := r.runningStep(key.Step, key.Attempt)
	if err != nil {
		return err
	}
	res, err := adapter.ParseResult(data, r.identity(s.ID, key.Attempt))
	if err != nil {
		if errors.Is(err, adapter.ErrMalformedResult) || errors.Is(err, adapter.ErrIdentityMismatch) {
			return &Rejection{Reason: err.Error()}
		}
		return err
	}
	if err := r.checkResult(s, key.Attempt, res); err != nil {
		if rejection := (*resultRejection)(nil); errors.As(err, &rejection) {
			return &Rejection{Reason: err.Error()}
		}
		return err
	}
	return nil
}

// runningStep is the step, a workflow step or a synthetic one (syntheticStep), whose running attempt is n: it exists,
// has no outcome and is the step's latest.
func (r *run) runningStep(step string, n int) (workflow.Step, error) {
	a := r.attempt(step, n)
	if a == nil {
		return workflow.Step{}, fmt.Errorf("interp: %w: no attempt %d of step %q", ErrAttemptNotRunning, n, step)
	}
	if a.Outcome != "" {
		return workflow.Step{}, fmt.Errorf("interp: %w: attempt %d of step %s is %s", ErrAttemptNotRunning, n, step, a.Outcome)
	}
	if latest := r.latestAttempt(step); latest.Number != n {
		return workflow.Step{}, fmt.Errorf("interp: %w: attempt %d of step %s is not its latest, %d", ErrAttemptNotRunning, n, step, latest.Number)
	}
	if i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == step }); i >= 0 {
		return r.def.Steps[i], nil
	}
	if s, ok := r.syntheticStep(step); ok {
		return s, nil
	}
	return workflow.Step{}, fmt.Errorf("interp: %w: step %q is not a step of the task's workflow", ErrAttemptNotRunning, step)
}

// readOnlyRecords is Records that refuses every write with ErrReadOnly, so a result check cannot change the store
// even if a check grew a write.
type readOnlyRecords struct{ Records }

func (readOnlyRecords) StartAttempt(string, string, string, string) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) StartCappedAttempt(string, string, string, string, store.AttemptCaps) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) StartDirectorAttempt(string, string, string, string, bool) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) FinishAttempt(string, string, string, string, int, store.AttemptOutcome) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) RejectAttempt(string, string, string, string, int, string) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) PutDocument(string, store.DocumentRef) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) OpenDecision(string, store.Decision) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) ContestRuling(string, string, string, string, string, string) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) ConsumeFullReview(string, string, string, int, string) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) SetTaskState(string, string, string, store.TaskState) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) VoidDecision(string, string, string, string, string) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}

func (readOnlyRecords) BindTaskGrant(string, string, string, string, string, int) (store.Receipt, error) {
	return store.Receipt{}, ErrReadOnly
}
