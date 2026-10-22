package git_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
)

// TestBaseProgram: a task's base is where its branch left the remote's default branch as worktree custody last
// fetched it, read without the remote; it moves when the branch is rebased onto a newer default branch, and
// cannot be read for a task with no workspace or one retired.
func TestBaseProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	ensure := func(t *testing.T, w git.Workspace) {
		t.Helper()
		if err := (git.Program{}).Ensure(ctx, w); err != nil {
			t.Fatalf("Ensure = %v", err)
		}
	}
	base := func(t *testing.T, w git.Workspace) string {
		t.Helper()
		got, err := (git.Program{}).Base(ctx, w.Repository, w.Branch)
		if err != nil {
			t.Fatalf("Base = %v", err)
		}
		return got
	}
	// moveRemote commits to the remote's default branch from a clone of its own and returns the new commit.
	moveRemote := func(t *testing.T, w git.Workspace) string {
		t.Helper()
		clone := repo{t, t.TempDir(), global}
		clone.run("clone", "-q", w.Remote, ".")
		commit := clone.commit("upstream", map[string]string{"upstream.txt": "upstream\n"})
		clone.run("push", "-q", "origin", "HEAD:trunk")
		return commit
	}
	t.Run("a new workspace is based on the default branch it started at", func(t *testing.T) {
		w, start := arrangeWorkspace(t)
		ensure(t, w)
		repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		if got := base(t, w); got != start {
			t.Errorf("Base = %s, want %s", got, start)
		}
	})
	t.Run("the base moves when the branch is rebased onto the fetched default branch", func(t *testing.T) {
		w, start := arrangeWorkspace(t)
		ensure(t, w)
		repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		moved := moveRemote(t, w)
		// The remote moved but the branch did not: the base is where the branch left the default branch.
		ensure(t, w)
		if got := base(t, w); got != start {
			t.Errorf("Base before the rebase = %s, want %s", got, start)
		}
		// An Artisan rebases onto the default branch the reused workspace fetched.
		repo{t, w.Dir, global}.run("rebase", "-q", "refs/guildhall/default/"+w.Branch)
		if got := base(t, w); got != moved {
			t.Errorf("Base after the rebase = %s, want %s", got, moved)
		}
		// Reading the base needs no remote.
		w.Remote = filepath.Join(t.TempDir(), "missing.git")
		if got := base(t, w); got != moved {
			t.Errorf("Base without the remote = %s, want %s", got, moved)
		}
	})
	t.Run("a reused workspace whose remote is unreachable is still reused", func(t *testing.T) {
		w, start := arrangeWorkspace(t)
		ensure(t, w)
		w.Remote = filepath.Join(t.TempDir(), "missing.git")
		ensure(t, w)
		if got := base(t, w); got != start {
			t.Errorf("Base = %s, want %s", got, start)
		}
	})
	t.Run("no base without a workspace or after retirement", func(t *testing.T) {
		w, _ := arrangeWorkspace(t)
		if got, err := (git.Program{}).Base(ctx, w.Repository, w.Branch); err == nil {
			t.Errorf("Base before Ensure = %s, want an error", got)
		}
		ensure(t, w)
		head := repo{t, w.Dir, global}.run("rev-parse", "HEAD")
		if kept, err := (git.Program{}).Retire(ctx, git.Retirement{Workspace: w, Head: head}); err != nil || len(kept) > 0 {
			t.Fatalf("Retire = %v, %v", kept, err)
		}
		if got, err := (git.Program{}).Base(ctx, w.Repository, w.Branch); err == nil {
			t.Errorf("Base after Retire = %s, want an error", got)
		}
		if refs := (repo{t, w.Repository, global}).run("for-each-ref", "refs/guildhall/"); refs != "" {
			t.Errorf("refs after Retire:\n%s", refs)
		}
	})
	t.Run("refuses an invalid branch", func(t *testing.T) {
		w, _ := arrangeWorkspace(t)
		ensure(t, w)
		for _, branch := range []string{"", "-x", "a..b"} {
			if got, err := (git.Program{}).Base(ctx, w.Repository, branch); err == nil {
				t.Errorf("Base(%q) = %s, want an error", branch, got)
			}
		}
	})
}
