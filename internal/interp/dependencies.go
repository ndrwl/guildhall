package interp

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// dependencyGate decides whether the task may run s, its first step not run by the Director, or must wait for its
// dependencies to merge (docs/interpreter.md#the-dependency-gate). It passes a task with no depends_on, reading
// nothing more; a task that already has an attempt of a step not run by the Director; a task the Director overrode;
// and a task whose every dependency is done. A task that passes by the override or its dependencies, before its
// first such attempt is recorded, has its untouched branch fast-forwarded to the current main first.
//
// A held task records nothing: the Director is sent one dependency-held message per state of its unmerged
// dependencies, and the advance waits for them (WaitDependencies).
func (r *run) dependencyGate(s workflow.Step) (held bool, next Next, err error) {
	if len(r.task.DependsOn) == 0 || r.pastGate() {
		return false, Next{}, nil
	}
	_, err = r.e.Records.DependencyOverride(r.task.Repository, r.task.ID)
	switch {
	case err == nil:
		return false, Next{}, r.fastForward()
	case !errors.Is(err, store.ErrNotFound):
		return false, Next{}, fmt.Errorf("dependency override: %w", err)
	}
	lines, err := r.unmerged()
	if err != nil {
		return false, Next{}, err
	}
	if len(lines) == 0 {
		return false, Next{}, r.fastForward()
	}
	return true, Next{Wait: WaitDependencies, Step: s.ID}, r.dependencyHeld(s, lines)
}

// pastGate reports whether the task has an attempt of any step not run by the Director: it passed the gate before,
// so a loop, retry or send-back to that step is never held again.
func (r *run) pastGate() bool {
	return slices.ContainsFunc(r.attempts, func(a store.StageAttempt) bool {
		i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == a.Step })
		return i < 0 || r.def.Steps[i].Actor() != workflow.DirectorActor
	})
}

// unmerged lists each dependency that is not done as "<repo>/<id>: <state>", in repository then id order, where
// state is coarse so a dependency moving through its normal steps does not change it.
func (r *run) unmerged() ([]string, error) {
	deps := slices.Clone(r.task.DependsOn)
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
	if i := slices.IndexFunc(decisions, func(x store.Decision) bool { return !x.Answered }); i >= 0 {
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
	err := r.notify(store.MessageDependencyHeld+"."+digest.Of([]byte(states)).Hex()[:12], store.MessageDependencyHeld, text)
	if errors.Is(err, store.ErrMutationConflict) {
		return nil
	}
	return err
}

// fastForward moves the task's untouched branch to the current main as the gate opens; a branch it cannot move is
// kept, research proceeds on it and the Director is told why once.
func (r *run) fastForward() error {
	if r.e.Workspaces == nil {
		return nil
	}
	repo, err := r.repository()
	if err != nil {
		return err
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: TaskBranch(r.task.ID), Dir: r.workPath()}
	kept, err := r.e.Workspaces.FastForward(r.ctx, w)
	if err != nil {
		return fmt.Errorf("fast-forward the task branch: %w", err)
	}
	if kept == "" {
		return nil
	}
	text := fmt.Sprintf("The dependencies of %s/%s no longer hold it, but its branch %s was not moved to the current main, because %s.\nIt proceeds on the existing branch.",
		r.task.Repository, r.task.ID, w.Branch, kept)
	err = r.notify(store.MessageDependencyBranchKept, store.MessageDependencyBranchKept, text)
	if errors.Is(err, store.ErrMutationConflict) {
		return nil
	}
	return err
}
