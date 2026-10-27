package interp

import (
	"fmt"
	"path"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// recordWorkHead derives a missing code-head output from committed work. Once
// persisted, that document wins over subsequent workspace changes during replay.
// The synthetic head file belongs to the document directory, never the worktree.
func (r *run) recordWorkHead(s workflow.Step, n int, port string) error {
	name := documentName(place{step: s.ID, round: n, part: port})
	if r.document(name) != nil {
		return nil
	}
	head, err := (git.Program{}).FinalHead(r.ctx, r.workPath())
	if err != nil {
		return err
	}
	if err := r.checkHead(s, n, head); err != nil {
		return err
	}
	data := []byte(head + "\n")
	return r.put(store.DocumentRef{
		Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path:   path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), "head"),
		SHA256: digest.Of(data).Hex(),
	}, data)
}

// producesHead reports whether step s's single output port is a code head, as build's is.
func producesHead(s workflow.Step) bool {
	kind, _ := workflow.LookupKind(s.Type, s.Version)
	return len(kind.Outputs) == 1 && kind.Outputs[0].Type == workflow.CodeHead
}

// baseName is the document holding the base attempt n of step started from, <step>-<n>-base.
func baseName(step string, n int) string {
	return documentName(place{step: step, round: n, part: "base"})
}

// recordBase records, once, the base attempt n of a code-head step starts from: the merge base of the task
// branch and the default branch its workspace last fetched, as of launch. A relaunch keeps the first record, so
// the base a head is checked against never moves with the default branch.
func (r *run) recordBase(s workflow.Step, n int) error {
	if r.e.Workspaces == nil || !producesHead(s) {
		return nil
	}
	name := baseName(s.ID, n)
	if r.document(name) != nil {
		return nil
	}
	repo, err := r.repository()
	if err != nil {
		return err
	}
	base, err := r.e.Workspaces.Base(r.ctx, repo.Path, TaskBranch(r.task.ID))
	if err != nil {
		return fmt.Errorf("base of %s: %w", TaskBranch(r.task.ID), err)
	}
	if !digest.ValidHead(base) {
		return fmt.Errorf("base of %s is not a full commit id: %q", TaskBranch(r.task.ID), base)
	}
	data := []byte(base + "\n")
	return r.put(store.DocumentRef{
		Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path:   path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), "base"),
		SHA256: digest.Of(data).Hex(),
	}, data)
}

// checkHead refuses, as the result's fault, a head attempt n of step s produced that is not the tip of the task
// branch or does not descend from the base the attempt recorded at launch. Without Workspaces or a recorded base,
// as for an attempt launched before bases were recorded, nothing is checked. A git failure, a missing branch and
// an ended context are operational errors, never a rejection.
func (r *run) checkHead(s workflow.Step, n int, head string) error {
	if r.e.Workspaces == nil {
		return nil
	}
	d := r.document(baseName(s.ID, n))
	if d == nil {
		return nil
	}
	data, err := r.read(*d)
	if err != nil {
		return err
	}
	base := strings.TrimSpace(string(data))
	repo, err := r.repository()
	if err != nil {
		return err
	}
	branch := TaskBranch(r.task.ID)
	tip, err := r.e.Workspaces.Tip(r.ctx, repo.Path, branch)
	if err != nil {
		return fmt.Errorf("tip of %s: %w", branch, err)
	}
	if tip != head {
		return rejectOutput(fmt.Errorf("head %s is not the tip of %s (tip %s)", short(head), branch, short(tip)))
	}
	ok, err := r.e.Workspaces.IsAncestor(r.ctx, repo.Path, base, head)
	if err != nil {
		return fmt.Errorf("ancestry of head %s: %w", head, err)
	}
	if !ok {
		return rejectOutput(fmt.Errorf("head %s does not descend from the attempt's base %s", short(head), short(base)))
	}
	return nil
}

// short is a commit id abbreviated to seven characters for a reason a reader scans.
func short(id string) string {
	if len(id) > 7 {
		return id[:7]
	}
	return id
}
