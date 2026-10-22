package interp

import (
	"context"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/git"
)

// Workspaces makes a task's working directory a git worktree of its repository; git.Program implements it (its
// contract is git.Program.Ensure), and gittest.Workspaces is its double.
type Workspaces interface {
	Ensure(ctx context.Context, w git.Workspace) error
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
