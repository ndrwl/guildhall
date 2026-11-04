package interp

import (
	"fmt"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskGrill is a task's grill mode as an advance applies it (docs/tasks.md#grill-mode): the record in effect, live with
// number 0 for a task that has none, the id of the bound definition's Director grill the mode applies to, "" when it
// has none, and whether the mode changed the definition.
type taskGrill struct {
	store.GrillMode
	step    string
	applied bool
}

// grillMode reads task t's grill mode in effect and finds the bound definition's Director grill, which begin applies
// it to.
func (e *Engine) grillMode(t store.Task) (taskGrill, error) {
	records, err := e.Records.GrillModes(t.Repository, t.ID)
	if err != nil {
		return taskGrill{}, fmt.Errorf("interp: grill mode: %w", err)
	}
	return taskGrill{GrillMode: store.CurrentGrillMode(records)}, nil
}

// boundGrillMode is the task's grill mode a grill attempt was bound under: its mode and record number.
type boundGrillMode struct {
	Mode   workflow.GrillMode `json:"mode"`
	Number int                `json:"number"`
}

// bindGrillMode records in b the grill mode in effect when s is the task's grill and the task has a grill-mode record;
// any other step, and a task with none, leave b as it was, so its binding reads as it did before grill modes existed.
func (r *run) bindGrillMode(s workflow.Step, b *binding) {
	if s.ID != r.grill.step || r.grill.Number == 0 {
		return
	}
	b.GrillMode = &boundGrillMode{Mode: r.grill.Mode, Number: r.grill.Number}
}

// grillReopened reports whether attempt n of s, the task's grill, which succeeded, ran in another grill mode than the
// one now in effect, so the grill runs again in the new mode and its notes supersede the old ones. guildhall task
// grill changes the mode only until research's first attempt, so a grill is never reopened once a later step ran.
func (r *run) grillReopened(s workflow.Step, n int) (bool, error) {
	if s.ID != r.grill.step {
		return false, nil
	}
	b, err := r.binding(s.ID, n)
	if err != nil {
		return false, err
	}
	ran := workflow.GrillLive
	if b.GrillMode != nil {
		ran = b.GrillMode.Mode
	}
	return ran != r.grill.Mode, nil
}
