package interp

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// dependencyGate decides whether the task may run s, its first step not run by the Director, or must wait for its
// dependencies to merge (docs/interpreter.md#the-dependency-gate). It passes a task that already has an attempt of a
// step not run by the Director, reading nothing; a task with no effective dependencies (store.EffectiveDependencies),
// after one read of its dependency records and no dependency lookups; a task the Director overrode; and a task whose
// every effective dependency is done. The gate only holds or releases: a released task starts on the main its
// dependencies merged into because its first attempt's launch fast-forwards its untouched branch.
//
// A held task records nothing: the Director is sent one dependency-held message per state of its unmerged
// dependencies, and the advance waits for them (WaitDependencies).
func (r *run) dependencyGate(s workflow.Step) (held bool, next Next, err error) {
	if r.pastGate() {
		return false, Next{}, nil
	}
	changes, err := r.e.Records.TaskDependencies(r.task.Repository, r.task.ID)
	if err != nil {
		return false, Next{}, fmt.Errorf("dependency records: %w", err)
	}
	deps := store.EffectiveDependencies(r.task, changes)
	if len(deps) == 0 {
		return false, Next{}, nil
	}
	_, err = r.e.Records.DependencyOverride(r.task.Repository, r.task.ID)
	switch {
	case err == nil:
		return false, Next{}, nil
	case !errors.Is(err, store.ErrNotFound):
		return false, Next{}, fmt.Errorf("dependency override: %w", err)
	}
	lines, err := r.unmerged(deps)
	if err != nil {
		return false, Next{}, err
	}
	if len(lines) == 0 {
		return false, Next{}, nil
	}
	return true, Next{Wait: WaitDependencies, Step: s.ID}, r.dependencyHeld(s, lines)
}

// pastGate reports whether the task has an attempt of any step not run by the Director: it passed the gate before,
// so a loop, retry or send-back to that step is never held again. A design round's designer attempt does not count,
// so it never lets research past the gate.
func (r *run) pastGate() bool { return PastDependencyGate(r.def, r.attempts) }

// PastDependencyGate reports whether a task bound to def, with attempts, is past the dependency gate: it has an
// attempt of a step not run by the Director, a design round's designer attempt aside. The gate never holds such a
// task again, so a dependency added to it holds nothing (docs/tasks.md#changing-dependencies).
func PastDependencyGate(def *workflow.Definition, attempts []store.StageAttempt) bool {
	_, designer := DesignSteps(def)
	return slices.ContainsFunc(attempts, func(a store.StageAttempt) bool {
		i := slices.IndexFunc(def.Steps, func(s workflow.Step) bool { return s.ID == a.Step })
		return a.Step != designer && (i < 0 || def.Steps[i].Actor() != workflow.DirectorActor)
	})
}

// unmerged lists each of the task's effective dependencies, effective, that is not done as "<repo>/<id>: <state>",
// in repository then id order, where state is coarse so a dependency moving through its normal steps does not change
// it.
func (r *run) unmerged(effective []store.TaskRef) ([]string, error) {
	deps := slices.Clone(effective)
	slices.SortFunc(deps, func(a, b store.TaskRef) int {
		return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.ID, b.ID))
	})
	deps = slices.Compact(deps)
	var lines []string
	for _, d := range deps {
		state, err := r.dependencyState(d)
		if err != nil {
			return nil, fmt.Errorf("dependency %s/%s: %w", d.Repository, d.ID, err)
		}
		if state != "" {
			lines = append(lines, fmt.Sprintf("%s/%s: %s", d.Repository, d.ID, state))
		}
	}
	return lines, nil
}

// dependencyState is a dependency's coarse state read from its records, or "" when it is done.
func (r *run) dependencyState(d store.TaskRef) (string, error) {
	t, err := r.e.Records.Task(d.Repository, d.ID)
	if err != nil {
		return "", err
	}
	switch t.State {
	case store.TaskDone:
		return "", nil
	case store.TaskCancelled:
		return fmt.Sprintf("cancelled; it can never merge; start anyway with %s", r.overrideCommand()), nil
	}
	decisions, err := r.e.Records.Decisions(d.Repository, d.ID)
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(decisions, func(x store.Decision) bool { return x.Open() && x.Kind != store.DecisionQuestion }); i >= 0 {
		return fmt.Sprintf("waiting for the Guildmaster: decision %s open", decisions[i].ID), nil
	}
	attempts, err := r.e.Records.Attempts(d.Repository, d.ID)
	if err != nil {
		return "", err
	}
	for i, a := range attempts {
		latest := i == len(attempts)-1 || attempts[i+1].Step != a.Step
		if latest && (a.Outcome == store.AttemptFailed || a.Outcome == store.AttemptStopped) {
			return fmt.Sprintf("waiting for the Director: %s attempt %d %s", a.Step, a.Number, a.Outcome), nil
		}
	}
	if mq, ok := r.e.Records.(MergeQueue); ok {
		q, err := mq.MergeQueue(d.Repository)
		if err != nil {
			return "", err
		}
		if i := slices.IndexFunc(q.Entries, func(e store.QueuedEntry) bool { return e.TaskID == d.ID }); i >= 0 {
			if q.Entries[i].Held {
				return "merge held (task hold-merge)", nil
			}
			return fmt.Sprintf("queued for merge, place %d of %d", i+1, len(q.Entries)), nil
		}
	}
	return "in progress", nil
}

func (r *run) overrideCommand() string {
	return fmt.Sprintf("guildhall task start --ignore-deps --reason <reason> --repo %s %s", r.task.Repository, r.task.ID)
}

// dependencyHeld sends the Director the held task's dependency states under an id derived from them, so the same
// states send nothing new and a change sends a new message.
func (r *run) dependencyHeld(s workflow.Step, lines []string) error {
	states := strings.Join(lines, "\n")
	text := fmt.Sprintf("Task %s/%s is held before step %s until its dependencies merge:\n%s\nStart it anyway with: %s",
		r.task.Repository, r.task.ID, s.ID, states, r.overrideCommand())
	return r.notify(store.MessageDependencyHeld+"."+digest.Of([]byte(states)).Hex()[:12], store.MessageDependencyHeld, text)
}
