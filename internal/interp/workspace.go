package interp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"

	"github.com/ndrwl/guildhall/internal/git"
)

// Workspaces makes a task's working directory a git worktree of its repository, reads the task branch's tip,
// base and ancestry for the code-head check, and replays the task's change onto a base tip for the tiered rebase;
// git.Program implements it (its contract is git.Program.Ensure, FastForward, Retire, Tip, IsAncestor, Behind, Base,
// Rebase, Squash, Adopt, Diff and Log), and gittest.Workspaces is its double.
type Workspaces interface {
	Ensure(ctx context.Context, w git.Workspace) error
	FastForward(ctx context.Context, w git.Workspace) (kept string, err error)
	Retire(ctx context.Context, r git.Retirement) ([]string, error)
	Tip(ctx context.Context, repository, branch string) (string, error)
	IsAncestor(ctx context.Context, repository, commit, head string) (bool, error)
	Behind(ctx context.Context, repository, rev, ref string) (int, []string, error)
	Base(ctx context.Context, repository, branch string) (string, error)
	Rebase(ctx context.Context, r git.Replay) (git.Rebased, error)
	Squash(ctx context.Context, repository, base, head string) (string, error)
	Adopt(ctx context.Context, w git.Workspace, from, to string) error
	Diff(ctx context.Context, repository, a, b string) ([]byte, error)
	Log(ctx context.Context, repository, a, b string, paths ...string) ([]byte, error)
	// Changes reports whether the change from base to head adds, removes or changes the file at path.
	Changes(ctx context.Context, repository, base, head, path string) (bool, error)
}

var _ Workspaces = git.Program{}

// workPath is the task's working directory, <state>/<repo>/<task>/work.
func (r *run) workPath() string {
	return filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, "work")
}

// workDir makes the task's working directory ready for an attempt and returns it: with Workspaces, a worktree of
// the task's repository on its task branch, created by the first attempt and reused by every later one;
// without, a plain directory.
func (r *run) workDir() (string, error) {
	work := r.workPath()
	if r.e.Workspaces == nil {
		return work, os.MkdirAll(work, 0o755)
	}
	repo, err := r.repository()
	if err != nil {
		return "", err
	}
	return work, r.e.Workspaces.Ensure(r.ctx, git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: branchPrefix + r.task.ID, Dir: work})
}

// behindMain is where a code-head attempt's task branch stands against the default branch after its fast-forward:
// how many default-branch commits it lacks, and why an untouched branch was kept, or "".
type behindMain struct {
	Behind int
	Kept   string
}

// fastForward moves the task's untouched branch to the current main before Artisan attempt n of the workflow
// step s, after its working directory is made ready and before its base is recorded
// (docs/interpreter.md#the-working-directory). A branch with commits of its own is kept silently; an untouched
// branch that is not moved is kept too, the attempt proceeds on it and the Director is sent one branch-kept
// message for the attempt. For a code-head step it returns where the branch then stands, for the behind-main
// note; otherwise nil. Without Workspaces, for a Director stage and for a synthetic step it does nothing.
func (r *run) fastForward(s workflow.Step, n int) (*behindMain, error) {
	if r.e.Workspaces == nil || s.Actor() != workflow.ArtisanActor ||
		!slices.ContainsFunc(r.def.Steps, func(d workflow.Step) bool { return d.ID == s.ID }) {
		return nil, nil
	}
	repo, err := r.repository()
	if err != nil {
		return nil, err
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: TaskBranch(r.task.ID), Dir: r.workPath()}
	kept, err := r.e.Workspaces.FastForward(r.ctx, w)
	if err != nil {
		return nil, fmt.Errorf("fast-forward the task branch: %w", err)
	}
	var tip string
	if kept != "" || producesHead(s) {
		if tip, err = r.e.Workspaces.Tip(r.ctx, repo.Path, w.Branch); err != nil {
			return nil, fmt.Errorf("tip of %s: %w", w.Branch, err)
		}
	}
	if kept != "" {
		// The kept reason is free-form, so whether the branch has commits of its own is asked of git, not read
		// from it: only an untouched branch that was not moved is a branch kept.
		untouched, err := r.e.Workspaces.IsAncestor(r.ctx, repo.Path, tip, git.DefaultRef(w.Branch))
		if err != nil {
			return nil, fmt.Errorf("ancestry of %s: %w", w.Branch, err)
		}
		if !untouched {
			kept = ""
		} else if err := r.branchKept(s, n, w.Branch, kept); err != nil {
			return nil, err
		}
	}
	if !producesHead(s) {
		return nil, nil
	}
	behind, _, err := r.e.Workspaces.Behind(r.ctx, repo.Path, tip, git.DefaultRef(w.Branch))
	if err != nil {
		return nil, fmt.Errorf("commits %s is behind main: %w", w.Branch, err)
	}
	return &behindMain{Behind: behind, Kept: kept}, nil
}

// branchKept sends the Director the branch-kept message of attempt n of step s, once: a replay with a different
// reason sends nothing new.
func (r *run) branchKept(s workflow.Step, n int, branch, reason string) error {
	text := fmt.Sprintf("Step %s attempt %d of %s/%s runs on its branch %s, which was not moved to the current main, because %s.",
		s.ID, n, r.task.Repository, r.task.ID, branch, reason)
	return r.notify(fmt.Sprintf("%s.%s-%d", store.MessageBranchKept, s.ID, n), store.MessageBranchKept, text)
}

// retire removes the merged task's workspace when nothing in it would be lost, before the task closes, so a replay
// after a crash retires it again. A kept or unremovable workspace never fails the merge: the Director is told, by a
// workspace-kept message, what it holds or why it could not be removed. Only a failed message or the advance's
// context ending is an error, so an interrupted retirement leaves the task open and a replay retires it again.
func (r *run) retire(id string, repo config.Repository, head string) error {
	if r.e.Workspaces == nil {
		return nil
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: branchPrefix + r.task.ID, Dir: r.workPath()}
	kept, err := r.e.Workspaces.Retire(r.ctx, git.Retirement{Workspace: w, Head: head, Outputs: r.outputs()})
	if err != nil {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		kept = []string{"it could not be removed: " + err.Error()}
	}
	if len(kept) == 0 {
		return nil
	}
	text := fmt.Sprintf("Kept the working directory %s and branch %s after merging head %s (%s), because %s.\nNothing was removed; the Director decides what happens to them.",
		w.Dir, w.Branch, head, r.mergedBy(id), strings.Join(kept, "; "))
	return r.notify(fmt.Sprintf("workspace-kept.%s.%s", id, digest.Of([]byte(text)).Hex()[:12]), store.MessageWorkspaceKept, text)
}

// outputs are the task's recorded documents by their path below their record directory, the path an output had
// in the working directory, with their sha256.
func (r *run) outputs() map[string]string {
	return RetirementOutputs(r.task.Repository, r.task.ID, r.documents)
}

// RetirementOutputs are the documents of a task by their path below their record directory, the path an output had
// in the working directory, with their sha256: the outputs git.Program.Retire takes, so `guildhall prune` retires a
// done task's workspace as its merge did.
func RetirementOutputs(repository, taskID string, documents []store.DocumentRef) map[string]string {
	prefix := path.Join("tasks", repository, taskID) + "/"
	out := map[string]string{}
	for _, d := range documents {
		if _, rel, ok := strings.Cut(strings.TrimPrefix(d.Path, prefix), "/"); ok && strings.HasPrefix(d.Path, prefix) {
			out[rel] = d.SHA256
		}
	}
	return out
}
