package interp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"

	"github.com/ndrwl/guildhall/internal/git"
)

// Workspaces makes a task's working directory a git worktree of its repository; git.Program implements it (its
// contract is git.Program.Ensure, FastForward and Retire), and gittest.Workspaces is its double.
type Workspaces interface {
	Ensure(ctx context.Context, w git.Workspace) error
	FastForward(ctx context.Context, w git.Workspace) (kept string, err error)
	Retire(ctx context.Context, r git.Retirement) ([]string, error)
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
	prefix := path.Join("tasks", r.task.Repository, r.task.ID) + "/"
	out := map[string]string{}
	for _, d := range r.documents {
		if _, rel, ok := strings.Cut(strings.TrimPrefix(d.Path, prefix), "/"); ok && strings.HasPrefix(d.Path, prefix) {
			out[rel] = d.SHA256
		}
	}
	return out
}
