package interp

import (
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TaskKindOf is the one reading of the kind task t was created as (docs/decisions/task-kinds.md), in repository r:
// hallmark when its bound workflow resolves by resolve and has a hallmark step, the rule LastHallmark and
// OpenHallmark use; bug when it is bound to the name r runs for a bug and that name is not r's Workflow; feature
// otherwise. It never fails: a workflow that does not resolve, or a nil resolve, falls through to the name rule, and
// a zero r reads the shipped names.
func TaskKindOf(t store.Task, r config.Repository, resolve func(workflow.Revision) (*workflow.Definition, error)) string {
	if resolve != nil {
		if def, err := resolve(t.Workflow); err == nil && def != nil && len(definitionHallmarkSteps(def)) > 0 {
			return config.TaskKindHallmark
		}
	}
	if bug, ok := r.TaskKindWorkflow(config.TaskKindBug); ok && t.Workflow.Name == bug && bug != r.Workflow {
		return config.TaskKindBug
	}
	return config.TaskKindFeature
}
