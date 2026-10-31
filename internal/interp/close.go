package interp

import "github.com/ndrwl/guildhall/internal/store"

// closeTask closes the task done: it voids every decision of the task still open, with reason, then sets the task
// done. It is the one path every engine close takes, the last Guildhall merge, a merge outside Guildhall and a
// report-only finish, so a closed task never has an open decision; the store refuses the close otherwise
// (docs/decisions/closed-tasks-have-no-open-decisions.md). Each void is keyed on the decision alone and a close
// passes one fixed reason, so a close that failed part-way replays to the same writes.
func (r *run) closeTask(reason string) error {
	decisions, err := r.e.Records.Decisions(r.task.Repository, r.task.ID)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if !d.Open() {
			continue
		}
		if _, err := r.e.Records.VoidDecision(r.mutation("void", "close", d.ID), r.task.Repository, r.task.ID, d.ID, reason); err != nil {
			return err
		}
		if v := r.decision(d.ID); v != nil {
			v.Void, v.VoidReason = true, reason
		}
	}
	_, err = r.e.Records.SetTaskState(r.mutation("task-state", string(store.TaskDone)), r.task.Repository, r.task.ID, store.TaskDone)
	return err
}
