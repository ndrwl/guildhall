package gittest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ndrwl/guildhall/internal/git"
)

// rebases holds the double's scripted replays, by base and head.
type rebases struct {
	mu      sync.Mutex
	scripts map[[2]string]git.Rebased
	diffs   map[[2]string][]byte
	replays []git.Replay
}

func (f *Workspaces) rebases() *rebases {
	rb := &f.replay
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.scripts == nil {
		rb.scripts, rb.diffs = map[[2]string]git.Rebased{}, map[[2]string][]byte{}
	}
	return rb
}

// SetRebase scripts the replay of head onto base; its Base and Head are set from the request. Without a script a
// replay is clean: a new commit with equal patch ids.
func (f *Workspaces) SetRebase(base, head string, r git.Rebased) {
	rb := f.rebases()
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.scripts[[2]string{base, head}] = r
}

// SetDiff scripts Diff from a to b; without a script it is one line naming both.
func (f *Workspaces) SetDiff(a, b string, diff []byte) {
	rb := f.rebases()
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.diffs[[2]string{a, b}] = slices.Clone(diff)
}

// Replays returns every replay Rebase was asked for, in order.
func (f *Workspaces) Replays() []git.Replay {
	rb := f.rebases()
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return slices.Clone(rb.replays)
}

// Rebase implements git.Program.Rebase over the double's commits: the scripted replay, or a clean one. The commits
// it names become commits of the repository, with Base as their parent, and the same replay gives the same commits.
func (f *Workspaces) Rebase(ctx context.Context, r git.Replay) (git.Rebased, error) {
	if err := ctx.Err(); err != nil {
		return git.Rebased{}, err
	}
	rb := f.rebases()
	rb.mu.Lock()
	rb.replays = append(rb.replays, r)
	out, ok := rb.scripts[[2]string{r.Base, r.Head}]
	rb.mu.Unlock()
	if !ok {
		out = git.Rebased{Commit: derived(r.Base, r.Head, "rebase"), HeadPatchID: "patch-" + r.Head[:12]}
		out.PatchID = out.HeadPatchID
	}
	out.Base, out.Head = r.Base, r.Head
	if out.Conflicts == nil {
		out.Conflicts = []git.Conflict{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range []string{out.Commit, out.Marked} {
		if c != "" {
			f.parents[c] = r.Base
			f.add(r.Repository, c)
		}
	}
	return out, nil
}

// Squash implements git.Program.Squash: a commit derived from both, on base.
func (f *Workspaces) Squash(ctx context.Context, repository, base, head string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c := derived(base, head, "squash")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parents[c] = base
	f.add(repository, c)
	return c, nil
}

// Diff implements git.Program.Diff with the scripted diff.
func (f *Workspaces) Diff(ctx context.Context, repository, a, b string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rb := f.rebases()
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if d, ok := rb.diffs[[2]string{a, b}]; ok {
		return slices.Clone(d), nil
	}
	return []byte(fmt.Sprintf("diff %s..%s\n", a, b)), nil
}

// Adopt implements git.Program.Adopt over the double's directories: a tree on w.Branch at from moves to to, one
// already at to is unchanged, and any other is refused. Files in the directory are never touched.
func (f *Workspaces) Adopt(ctx context.Context, w git.Workspace, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Clean(w.Dir)
	tr, ok := f.trees[dir]
	switch {
	case !ok:
		return fmt.Errorf("%w: %s", git.ErrNotWorkspace, dir)
	case tr.branch != w.Branch:
		return fmt.Errorf("gittest: %s is not on branch %s", dir, w.Branch)
	case tr.head == to:
		return nil
	case tr.head != from:
		return fmt.Errorf("gittest: branch %s is at %s, not %s", w.Branch, tr.head, from)
	}
	tr.head = to
	f.trees[dir] = tr
	f.branches[[2]string{tr.repository, tr.branch}] = to
	f.add(tr.repository, to)
	return nil
}

// derived is a commit id derived from a, b and what made it.
func derived(a, b, what string) string {
	sum := sha1.Sum([]byte(a + b + what))
	return hex.EncodeToString(sum[:])
}
