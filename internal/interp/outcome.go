package interp

import (
	"fmt"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// checkResultOutcome refuses a result's structured outcome (docs/adapters.md#result-file) that an attempt of step s
// may not report: any outcome on a step other than research, plan or build, and an already-delivered outcome naming
// by_slice unless s is inside the slice range of a multi-PR task and the slice is an earlier one. Before the slice
// range starts the task counts as one pull request, so only by_pr is accepted there. A result with no outcome passes.
// It checks only the outcome's place, not its evidence: whether the pull request merged is the Director's, or the
// close decision's, to check. A refusal is a *resultRejection; it writes nothing.
func (r *run) checkResultOutcome(s workflow.Step, res adapter.Result) error {
	o := res.Outcome
	if o == nil {
		return nil
	}
	if !workflow.ReportsOutcome(s.Type) {
		return rejectOutput(fmt.Errorf("step %s is a %s step, which reports no outcome; only research, plan and build report %q or %q",
			s.ID, s.Type, adapter.OutcomeAlreadyDelivered, adapter.OutcomeNothingToDo))
	}
	if o.BySlice == 0 {
		return nil
	}
	base, _ := workflow.ParseSliceStep(s.ID)
	switch {
	case !r.base.InSlices(base):
		return rejectOutput(fmt.Errorf("step %s runs before the slice range, where task %s delivers one pull request: name the pull request that shipped it with by_pr, not by_slice", s.ID, r.task.ID))
	case !r.slice.multi():
		return rejectOutput(fmt.Errorf("task %s delivers one pull request: name the pull request that shipped it with by_pr, not by_slice", r.task.ID))
	case o.BySlice >= r.slice.current:
		return rejectOutput(fmt.Errorf("by_slice %d is not a slice earlier than this attempt's slice %d", o.BySlice, r.slice.current))
	}
	return nil
}

// outcomeText is how the attempt-failed message names attempt n of step's structured outcome, " It reports: <label>."
// with the Director's next move, or "" when its result has none or cannot be read. Until the close decision lands,
// the Director acts on it by hand.
func (r *run) outcomeText(step string, n int) string {
	res, err := adapter.ReadResult(r.resultFile(step, n), r.identity(step, n))
	if err != nil || res.Outcome == nil {
		return ""
	}
	text := " It reports: " + res.Outcome.Label() + "."
	if res.Outcome.Kind == adapter.OutcomeAlreadyDelivered {
		text += " Check it and close it with guildhall slice delivered, or retry it."
	}
	return text
}
