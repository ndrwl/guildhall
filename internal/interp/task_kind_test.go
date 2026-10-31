package interp_test

import (
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskKindResolver resolves each name in defs to its definition, and any other name to an error.
func taskKindResolver(defs map[string]*workflow.Definition) func(workflow.Revision) (*workflow.Definition, error) {
	return func(rev workflow.Revision) (*workflow.Definition, error) {
		if def, ok := defs[rev.Name]; ok {
			return def, nil
		}
		return nil, errors.New("unknown workflow")
	}
}

func taskBoundTo(name string) store.Task {
	return store.Task{Repository: "app", ID: "t1", Workflow: workflow.Revision{Name: name}}
}

var (
	taskKindHallmarkDef = &workflow.Definition{Steps: []workflow.Step{{ID: "hallmark", Type: "hallmark"}}}
	taskKindPlainDef    = &workflow.Definition{Steps: []workflow.Step{{ID: "build", Type: "build"}}}
)

// TestTaskKindOfRenamedHallmark: a hallmark task whose repository later renames kinds.hallmark still reads hallmark,
// since its bound workflow resolves and has a hallmark step (hallmark d90 H13, red first).
func TestTaskKindOfRenamedHallmark(t *testing.T) {
	r := config.Repository{ID: "app", Workflow: "default", Kinds: map[string]string{config.TaskKindHallmark: "audit-v2"}}
	resolve := taskKindResolver(map[string]*workflow.Definition{"audit": taskKindHallmarkDef})
	if got := interp.TaskKindOf(taskBoundTo("audit"), r, resolve); got != config.TaskKindHallmark {
		t.Fatalf("TaskKindOf = %q, want hallmark", got)
	}
}

// TestTaskKindOfBugResolves: a task bound to kinds.bug's workflow, which resolves with no hallmark step, reads bug
// (red first).
func TestTaskKindOfBugResolves(t *testing.T) {
	r := config.Repository{ID: "app", Workflow: "default", Kinds: map[string]string{config.TaskKindBug: "fix-flow"}}
	resolve := taskKindResolver(map[string]*workflow.Definition{"fix-flow": taskKindPlainDef, "default": taskKindPlainDef})
	if got := interp.TaskKindOf(taskBoundTo("fix-flow"), r, resolve); got != config.TaskKindBug {
		t.Fatalf("TaskKindOf = %q, want bug", got)
	}
}

// TestTaskKindOfBugIsWorkflow: when kinds.bug names the repository's workflow, a task bound to it reads feature, as
// docs/decisions/task-kinds.md says.
func TestTaskKindOfBugIsWorkflow(t *testing.T) {
	r := config.Repository{ID: "app", Workflow: "shared", Kinds: map[string]string{config.TaskKindBug: "shared"}}
	resolve := taskKindResolver(map[string]*workflow.Definition{"shared": taskKindPlainDef})
	if got := interp.TaskKindOf(taskBoundTo("shared"), r, resolve); got != config.TaskKindFeature {
		t.Fatalf("TaskKindOf = %q, want feature", got)
	}
}

// TestTaskKindOfNeverFails: a workflow that does not resolve falls through to the name rule, so kinds.bug's name
// reads bug and a hallmark name reads feature; a nil resolver or a nil definition does not panic.
func TestTaskKindOfNeverFails(t *testing.T) {
	r := config.Repository{ID: "app", Workflow: "default", Kinds: map[string]string{config.TaskKindBug: "fix-flow"}}
	fail := taskKindResolver(nil)
	if got := interp.TaskKindOf(taskBoundTo("fix-flow"), r, fail); got != config.TaskKindBug {
		t.Fatalf("unresolved kinds.bug: TaskKindOf = %q, want bug", got)
	}
	if got := interp.TaskKindOf(taskBoundTo("hallmark"), r, fail); got != config.TaskKindFeature {
		t.Fatalf("unresolved hallmark: TaskKindOf = %q, want feature", got)
	}
	if got := interp.TaskKindOf(taskBoundTo("fix-flow"), r, nil); got != config.TaskKindBug {
		t.Fatalf("nil resolver: TaskKindOf = %q, want bug", got)
	}
	nilDef := func(workflow.Revision) (*workflow.Definition, error) { return nil, nil }
	if got := interp.TaskKindOf(taskBoundTo("default"), r, nilDef); got != config.TaskKindFeature {
		t.Fatalf("nil definition: TaskKindOf = %q, want feature", got)
	}
}

// TestTaskKindOfZeroRepository: with a zero repository, a task bound to bug reads bug and one bound to default reads
// feature.
func TestTaskKindOfZeroRepository(t *testing.T) {
	fail := taskKindResolver(nil)
	if got := interp.TaskKindOf(taskBoundTo("bug"), config.Repository{}, fail); got != config.TaskKindBug {
		t.Fatalf("bug: TaskKindOf = %q, want bug", got)
	}
	if got := interp.TaskKindOf(taskBoundTo("default"), config.Repository{}, fail); got != config.TaskKindFeature {
		t.Fatalf("default: TaskKindOf = %q, want feature", got)
	}
}
