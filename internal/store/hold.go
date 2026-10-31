package store

import (
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// A hold is a Director record kept until named tasks merge (docs/decisions/hold-until-merged.md): a park with
// UntilMerged, a decision deferral or a follow-up. The engine never reads one; status judges each from the task
// states it already reads, with JudgeHold.

// HoldState is a hold's state, judged from the states of the tasks it names.
type HoldState string

const (
	// HoldWaiting is a hold some of whose named tasks are still open.
	HoldWaiting HoldState = "waiting"
	// HoldReleased is a hold every one of whose named tasks is done.
	HoldReleased HoldState = "released"
	// HoldDependencyCancelled is a hold one of whose named tasks is cancelled, so it can never be released by merge.
	HoldDependencyCancelled HoldState = "dependency-cancelled"
)

// HoldJudgement is what JudgeHold finds: the state, the named tasks not yet merged and, for
// HoldDependencyCancelled, the named tasks that are cancelled.
type HoldJudgement struct {
	State     HoldState
	Waiting   []TaskRef
	Cancelled []TaskRef
}

// JudgeHold judges a hold naming untilMerged from tasks: released once every named task is done, released as
// dependency-cancelled at once when any is cancelled, and otherwise waiting for the named tasks not done. A named
// task missing from tasks is waited for.
func JudgeHold(untilMerged []TaskRef, tasks []Task) HoldJudgement {
	var j HoldJudgement
	for _, ref := range untilMerged {
		i := slices.IndexFunc(tasks, func(t Task) bool { return t.Repository == ref.Repository && t.ID == ref.ID })
		switch {
		case i >= 0 && tasks[i].State == TaskDone:
		case i >= 0 && tasks[i].State == TaskCancelled:
			j.Cancelled = append(j.Cancelled, ref)
		default:
			j.Waiting = append(j.Waiting, ref)
		}
	}
	switch {
	case len(j.Cancelled) > 0:
		j.State = HoldDependencyCancelled
	case len(j.Waiting) > 0:
		j.State = HoldWaiting
	default:
		j.State = HoldReleased
	}
	return j
}

// String is "<repository>/<id>".
func (r TaskRef) String() string { return r.Repository + "/" + r.ID }

// TaskRefList is refs as "<repository>/<id>, …".
func TaskRefList(refs []TaskRef) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

// validateUntilMerged checks a hold's named tasks without the store: at least one, each a valid id, none named
// twice.
func validateUntilMerged(untilMerged []TaskRef) error {
	if len(untilMerged) == 0 {
		return invalid("a hold must name at least one task it waits to merge")
	}
	for i, ref := range untilMerged {
		if err := validateIDs(ref.Repository, ref.ID); err != nil {
			return err
		}
		if slices.Contains(untilMerged[:i], ref) {
			return invalid("hold names %s twice", ref)
		}
	}
	return nil
}

// checkUntilMerged refuses, inside a write transaction, a hold whose named tasks are not all open tasks of this
// store, or that names self, the held item's own task (zero for a follow-up). A named task already done or cancelled
// is refused, so the Director acts now instead.
func checkUntilMerged(tx *bolt.Tx, untilMerged []TaskRef, self TaskRef) error {
	if err := validateUntilMerged(untilMerged); err != nil {
		return err
	}
	for _, ref := range untilMerged {
		if ref == self {
			return invalid("a hold on %s cannot wait for %s itself to merge", self, ref)
		}
		t, err := getTask(tx, ref.Repository, ref.ID)
		if err != nil {
			return fmt.Errorf("held until: %w", err)
		}
		switch t.State {
		case TaskDone:
			return invalid("%s already merged; act now instead", ref)
		case TaskCancelled:
			return invalid("%s is already cancelled; act now instead", ref)
		}
	}
	return nil
}
