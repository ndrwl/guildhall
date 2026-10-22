package gittest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
)

// WorkspaceHarness is one implementation of git.Program.Ensure under the workspace contract suite.
type WorkspaceHarness struct {
	Ensure func(ctx context.Context, w git.Workspace) error
	// Arrange returns a workspace whose remote's default branch is at base, whose Dir and branch do not exist yet
	// and whose Dir's parent does.
	Arrange func(t *testing.T) (w git.Workspace, base string)
	// Head returns the commit the worktree at dir is at.
	Head func(t *testing.T, dir string) string
	// Commit adds the file name to the worktree at dir, commits it and returns the commit.
	Commit func(t *testing.T, dir, name string) string
	// Contains reports whether head is a commit of the repository at repository.
	Contains func(t *testing.T, repository, head string) bool
}

// RunWorkspaceContract runs the workspace contract (git.Program.Ensure) against harnesses from newHarness.
func RunWorkspaceContract(t *testing.T, newHarness func(t *testing.T) WorkspaceHarness) {
	ctx := context.Background()
	ensure := func(t *testing.T, h WorkspaceHarness, w git.Workspace) {
		t.Helper()
		if err := h.Ensure(ctx, w); err != nil {
			t.Fatalf("Ensure(%+v) = %v", w, err)
		}
	}
	t.Run("creates the worktree at the remote's default branch", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		ensure(t, h, w)
		if got := h.Head(t, w.Dir); got != base {
			t.Errorf("new worktree at %s, want the default branch's %s", got, base)
		}
	})
	t.Run("reuses the worktree across attempts", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		ensure(t, h, w)
		commit := h.Commit(t, w.Dir, "first.txt")
		writeFile(t, filepath.Join(w.Dir, "notes.txt"), "uncommitted\n")
		ensure(t, h, w)
		if got := h.Head(t, w.Dir); got != commit {
			t.Errorf("reused worktree at %s, want the attempt's commit %s", got, commit)
		}
		if b, err := os.ReadFile(filepath.Join(w.Dir, "notes.txt")); err != nil || string(b) != "uncommitted\n" {
			t.Errorf("reuse lost an uncommitted file: %q, %v", b, err)
		}
		if !h.Contains(t, w.Repository, commit) {
			t.Errorf("commit %s made in the worktree is not a commit of the repository", commit)
		}
	})
	t.Run("an empty directory becomes the worktree", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		if err := os.Mkdir(w.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ensure(t, h, w)
		if got := h.Head(t, w.Dir); got != base {
			t.Errorf("worktree at %s, want %s", got, base)
		}
	})
	t.Run("refuses a plain directory with files", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		writeFile(t, filepath.Join(w.Dir, "plan.md"), "an earlier attempt's output\n")
		if err := h.Ensure(ctx, w); !errors.Is(err, git.ErrNotWorkspace) {
			t.Fatalf("Ensure over a plain directory = %v, want ErrNotWorkspace", err)
		}
		entries, err := os.ReadDir(w.Dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "plan.md" {
			t.Errorf("refused directory holds %v, %v; want only plan.md", entries, err)
		}
	})
	t.Run("a directory with spaces", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		w.Dir = filepath.Join(filepath.Dir(w.Dir), "a task dir", "work")
		ensure(t, h, w)
		ensure(t, h, w)
		if got := h.Head(t, w.Dir); got != base {
			t.Errorf("worktree at %s, want %s", got, base)
		}
	})
	t.Run("workspaces of one repository are independent", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		other := w
		other.Branch, other.Dir = w.Branch+"-other", w.Dir+"-other"
		ensure(t, h, w)
		ensure(t, h, other)
		commit := h.Commit(t, w.Dir, "mine.txt")
		if got := h.Head(t, other.Dir); got != base {
			t.Errorf("other worktree at %s after a commit in the first, want %s", got, base)
		}
		if got := h.Head(t, w.Dir); got != commit {
			t.Errorf("worktree at %s, want %s", got, commit)
		}
	})
	t.Run("refuses an invalid workspace", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		for name, bad := range map[string]func(*git.Workspace){
			"relative dir":        func(w *git.Workspace) { w.Dir = "work" },
			"relative repository": func(w *git.Workspace) { w.Repository = "repo" },
			"no remote":           func(w *git.Workspace) { w.Remote = "" },
			"no branch":           func(w *git.Workspace) { w.Branch = "" },
			"invalid branch":      func(w *git.Workspace) { w.Branch = "guildhall/a b" },
		} {
			v := w
			bad(&v)
			if err := h.Ensure(ctx, v); !errors.Is(err, git.ErrInvalidWorkspace) {
				t.Errorf("%s: Ensure = %v, want ErrInvalidWorkspace", name, err)
			}
		}
		if _, err := os.Stat(w.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused Ensure created the working directory: %v", err)
		}
	})
	t.Run("ended context", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		ended, cancel := context.WithCancel(ctx)
		cancel()
		if err := h.Ensure(ended, w); err == nil {
			t.Fatal("Ensure with an ended context succeeded")
		}
		if _, err := os.Stat(w.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Ensure with an ended context created the working directory: %v", err)
		}
	})
}

func writeFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Workspaces is an in-memory double of git.Program.Ensure for consumers' tests: each working directory it creates
// is a plain directory whose commits it keeps in memory, and it passes RunWorkspaceContract. It never runs git.
// It is safe for concurrent use.
type Workspaces struct {
	mu       sync.Mutex
	defaults map[string]string
	branches map[[2]string]string
	trees    map[string]tree
	commits  map[string]map[string]bool
	calls    []git.Workspace
	n        int
}

// tree is one working directory the double created: its repository and the commit it is at.
type tree struct{ repository, head string }

// NewWorkspaces returns a double with no remotes.
func NewWorkspaces() *Workspaces {
	return &Workspaces{defaults: map[string]string{}, branches: map[[2]string]string{}, trees: map[string]tree{}, commits: map[string]map[string]bool{}}
}

// SetDefault points remote's default branch at head; Ensure fails for a remote with none.
func (f *Workspaces) SetDefault(remote, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaults[remote] = head
}

// Ensure implements the contract over plain directories.
func (f *Workspaces) Ensure(ctx context.Context, w git.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(w.Repository) || !filepath.IsAbs(w.Dir) || w.Remote == "" || w.Branch == "" || strings.ContainsAny(w.Branch, " ~^:?*[\\") {
		return fmt.Errorf("%w: %+v", git.ErrInvalidWorkspace, w)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, w)
	dir := filepath.Clean(w.Dir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if tr, ok := f.trees[dir]; ok && err == nil {
		if tr.repository != w.Repository {
			return fmt.Errorf("%w: %s is a worktree of %s", git.ErrNotWorkspace, dir, tr.repository)
		}
		return nil
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", git.ErrNotWorkspace, dir)
	}
	head, ok := f.branches[[2]string{w.Repository, w.Branch}]
	if !ok {
		if head, ok = f.defaults[w.Remote]; !ok {
			return fmt.Errorf("remote %s has no default branch", w.Remote)
		}
		f.branches[[2]string{w.Repository, w.Branch}] = head
		f.add(w.Repository, head)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.trees[dir] = tree{repository: w.Repository, head: head}
	return nil
}

func (f *Workspaces) add(repository, head string) {
	if f.commits[repository] == nil {
		f.commits[repository] = map[string]bool{}
	}
	f.commits[repository][head] = true
}

// Head returns the commit the working directory dir is at, or "" for one the double did not create.
func (f *Workspaces) Head(dir string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trees[filepath.Clean(dir)].head
}

// Commit records a new commit in the working directory dir, as an Artisan's commit there, and returns it.
func (f *Workspaces) Commit(dir string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir = filepath.Clean(dir)
	tr, ok := f.trees[dir]
	if !ok {
		return ""
	}
	f.n++
	sum := sha1.Sum([]byte(tr.head + strconv.Itoa(f.n)))
	tr.head = hex.EncodeToString(sum[:])
	f.trees[dir] = tr
	f.add(tr.repository, tr.head)
	return tr.head
}

// Contains reports whether head is a commit of repository.
func (f *Workspaces) Contains(repository, head string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[repository][head]
}

// Calls returns every workspace Ensure was asked for after validation, in order.
func (f *Workspaces) Calls() []git.Workspace {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]git.Workspace(nil), f.calls...)
}

// Repository builds, with real git and without the user's or system's git configuration, a bare remote whose
// default branch trunk is at base and a clone of it at repository fetched one commit before base, so a worktree
// that starts at base proves it fetched the remote's default branch. Both are under one new temporary directory.
func Repository(t *testing.T) (repository, remote, base string) {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "gitconfig")
	writeFile(t, global, "")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+global)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote, repository = filepath.Join(root, "remote.git"), filepath.Join(root, "repository")
	seed := filepath.Join(root, "seed")
	git(root, "init", "-q", "--bare", "--initial-branch", "trunk", remote)
	git(root, "init", "-q", "--initial-branch", "trunk", seed)
	commit := func(name string) string {
		writeFile(t, filepath.Join(seed, name), name+"\n")
		git(seed, "add", name)
		git(seed, "commit", "-q", "-m", name)
		git(seed, "push", "-q", remote, "trunk")
		return git(seed, "rev-parse", "HEAD")
	}
	commit("a.txt")
	git(root, "clone", "-q", remote, repository)
	return repository, remote, commit("b.txt")
}
