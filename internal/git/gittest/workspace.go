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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
)

// WorkspaceHarness is one implementation of git.Program.Ensure, FastForward and Retire under the workspace contract suite.
type WorkspaceHarness struct {
	Ensure func(ctx context.Context, w git.Workspace) error
	Retire func(ctx context.Context, r git.Retirement) ([]string, error)
	// FastForward is git.Program.FastForward.
	FastForward func(ctx context.Context, w git.Workspace) (string, error)
	// AdvanceDefault adds a commit adding the file name to the default branch of w's remote, as a dependency's
	// merge does, and returns it.
	AdvanceDefault func(t *testing.T, w git.Workspace, name string) string
	// Tip, IsAncestor and Base are the implementation's git.Program.Tip, IsAncestor and Base.
	Tip        func(ctx context.Context, repository, branch string) (string, error)
	IsAncestor func(ctx context.Context, repository, commit, head string) (bool, error)
	Base       func(ctx context.Context, repository, branch string) (string, error)
	// Behind is the implementation's git.Program.Behind.
	Behind func(ctx context.Context, repository, rev, ref string) (int, []string, error)
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

// RunWorkspaceContract runs the workspace contract (git.Program.Ensure, FastForward and Retire) against harnesses from newHarness.
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
	forward := func(t *testing.T, h WorkspaceHarness, w git.Workspace) string {
		t.Helper()
		kept, err := h.FastForward(ctx, w)
		if err != nil {
			t.Fatalf("FastForward(%+v) = %v", w, err)
		}
		return kept
	}
	t.Run("fast-forwards an untouched branch and keeps untracked outputs", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		ensure(t, h, w)
		writeFile(t, filepath.Join(w.Dir, "notes.md"), "the grill's notes\n")
		main := h.AdvanceDefault(t, w, "merged.txt")
		if kept := forward(t, h, w); kept != "" {
			t.Fatalf("FastForward kept the branch: %s", kept)
		}
		if got := h.Head(t, w.Dir); got != main {
			t.Errorf("worktree at %s after FastForward, want the default branch's %s", got, main)
		}
		if b, err := os.ReadFile(filepath.Join(w.Dir, "notes.md")); err != nil || string(b) != "the grill's notes\n" {
			t.Errorf("FastForward lost an untracked output: %q, %v", b, err)
		}
		if kept := forward(t, h, w); kept != "" || h.Head(t, w.Dir) != main {
			t.Errorf("repeated FastForward = %q at %s, want nothing done at %s", kept, h.Head(t, w.Dir), main)
		}
	})
	t.Run("fast-forward of a missing worktree does nothing", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		if kept := forward(t, h, w); kept != "" {
			t.Errorf("FastForward of a missing worktree kept %q", kept)
		}
		if _, err := os.Stat(w.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("FastForward created the working directory: %v", err)
		}
	})
	t.Run("fast-forward keeps a diverged branch", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		ensure(t, h, w)
		commit := h.Commit(t, w.Dir, "task.txt")
		h.AdvanceDefault(t, w, "merged.txt")
		kept := forward(t, h, w)
		if kept == "" {
			t.Fatal("FastForward of a diverged branch kept nothing")
		}
		t.Logf("kept: %s", kept)
		if got := h.Head(t, w.Dir); got != commit {
			t.Errorf("diverged worktree moved to %s, want %s", got, commit)
		}
		if again := forward(t, h, w); again == "" || h.Head(t, w.Dir) != commit {
			t.Errorf("repeated FastForward = %q at %s", again, h.Head(t, w.Dir))
		}
	})
	t.Run("fast-forward keeps a branch an untracked file blocks", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		ensure(t, h, w)
		writeFile(t, filepath.Join(w.Dir, "merged.txt"), "the grill's own file\n")
		h.AdvanceDefault(t, w, "merged.txt")
		kept := forward(t, h, w)
		if kept == "" {
			t.Fatal("FastForward over an untracked file in the way kept nothing")
		}
		t.Logf("kept: %s", kept)
		if got := h.Head(t, w.Dir); got != base {
			t.Errorf("blocked worktree moved to %s, want %s", got, base)
		}
		if b, err := os.ReadFile(filepath.Join(w.Dir, "merged.txt")); err != nil || string(b) != "the grill's own file\n" {
			t.Errorf("FastForward overwrote an untracked file: %q, %v", b, err)
		}
	})
	t.Run("fast-forward refuses an invalid workspace", func(t *testing.T) {
		h := newHarness(t)
		w, _ := h.Arrange(t)
		w.Dir = "work"
		if _, err := h.FastForward(ctx, w); !errors.Is(err, git.ErrInvalidWorkspace) {
			t.Errorf("FastForward = %v, want ErrInvalidWorkspace", err)
		}
	})
	t.Run("the tip and base of a task branch", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		if _, err := h.Tip(ctx, w.Repository, w.Branch); err == nil {
			t.Error("Tip of a branch that does not exist succeeded")
		}
		if _, err := h.Base(ctx, w.Repository, w.Branch); err == nil {
			t.Error("Base before the first Ensure succeeded")
		}
		ensure(t, h, w)
		if got, err := h.Tip(ctx, w.Repository, w.Branch); err != nil || got != base {
			t.Errorf("Tip of a new branch = %s, %v; want %s", got, err, base)
		}
		commit := h.Commit(t, w.Dir, "c.txt")
		if got, err := h.Tip(ctx, w.Repository, w.Branch); err != nil || got != commit {
			t.Errorf("Tip after a commit = %s, %v; want %s", got, err, commit)
		}
		if got, err := h.Base(ctx, w.Repository, w.Branch); err != nil || got != base {
			t.Errorf("Base after a commit = %s, %v; want %s", got, err, base)
		}
	})
	t.Run("ancestry", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		ensure(t, h, w)
		commit := h.Commit(t, w.Dir, "c.txt")
		unknown := strings.Repeat("0", 39) + "1"
		for _, c := range []struct {
			commit, head string
			want         bool
		}{
			{base, commit, true},
			{commit, commit, true},
			{commit, base, false},
			{unknown, commit, false},
			{base, unknown, false},
		} {
			if got, err := h.IsAncestor(ctx, w.Repository, c.commit, c.head); err != nil || got != c.want {
				t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", c.commit, c.head, got, err, c.want)
			}
		}
		ended, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := h.IsAncestor(ended, w.Repository, base, commit); err == nil {
			t.Error("IsAncestor with an ended context succeeded")
		}
		if _, err := h.Tip(ended, w.Repository, w.Branch); err == nil {
			t.Error("Tip with an ended context succeeded")
		}
	})
	t.Run("behind the recorded default branch", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		ensure(t, h, w)
		def := git.DefaultRef(w.Branch)
		behind := func(rev string) int {
			t.Helper()
			n, subjects, err := h.Behind(ctx, w.Repository, rev, def)
			if err != nil || len(subjects) != n {
				t.Fatalf("Behind(%s, %s) = %d, %q, %v", rev, def, n, subjects, err)
			}
			return n
		}
		if got := behind(base); got != 0 {
			t.Errorf("a new branch is %d behind, want 0", got)
		}
		commit := h.Commit(t, w.Dir, "c.txt")
		h.AdvanceDefault(t, w, "one.txt")
		h.AdvanceDefault(t, w, "two.txt")
		if kept := forward(t, h, w); kept == "" {
			t.Fatal("FastForward of a diverged branch kept nothing")
		}
		if got := behind(commit); got != 2 {
			t.Errorf("a branch with its own commit is %d behind after main moved twice, want 2", got)
		}
		for _, c := range []struct {
			commit string
			want   bool
		}{{base, true}, {commit, false}} {
			if got, err := h.IsAncestor(ctx, w.Repository, c.commit, def); err != nil || got != c.want {
				t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", c.commit, def, got, err, c.want)
			}
		}
		if _, _, err := h.Behind(ctx, w.Repository, commit, git.DefaultRef("guildhall/none")); err == nil {
			t.Error("Behind a ref that does not exist succeeded")
		}
	})
	retired := func(t *testing.T, h WorkspaceHarness, r git.Retirement) {
		t.Helper()
		if kept, err := h.Retire(ctx, r); err != nil || len(kept) > 0 {
			t.Fatalf("Retire = %q, %v; want the workspace removed", kept, err)
		}
		if _, err := os.Stat(r.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired working directory is still there: %v", err)
		}
	}
	// arrangeMerged ensures a workspace, commits in it, writes an output and returns a retirement at that commit.
	arrangeMerged := func(t *testing.T, h WorkspaceHarness) git.Retirement {
		t.Helper()
		w, _ := h.Arrange(t)
		ensure(t, h, w)
		head := h.Commit(t, w.Dir, "change.txt")
		writeFile(t, filepath.Join(w.Dir, "out", "spec.md"), "the spec\n")
		return git.Retirement{Workspace: w, Head: head, Outputs: map[string][]string{"out/spec.md": {digest.Of([]byte("the spec\n")).Hex()}}}
	}
	kept := func(t *testing.T, h WorkspaceHarness, r git.Retirement) {
		t.Helper()
		k, err := h.Retire(ctx, r)
		if err != nil || len(k) == 0 {
			t.Fatalf("Retire = %q, %v; want the workspace kept with a reason", k, err)
		}
		t.Logf("kept: %q", k)
		if _, err := os.Stat(filepath.Join(r.Dir, "change.txt")); err != nil {
			t.Errorf("a kept workspace lost a file: %v", err)
		}
	}
	t.Run("retires a merged workspace once", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		retired(t, h, r)
		retired(t, h, r)
	})
	t.Run("a workspace never created is already retired", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		retired(t, h, git.Retirement{Workspace: w, Head: base})
	})
	t.Run("keeps an untracked file that is not an output", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		writeFile(t, filepath.Join(r.Dir, "notes.txt"), "mine\n")
		kept(t, h, r)
	})
	t.Run("keeps an output with other bytes", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		writeFile(t, filepath.Join(r.Dir, "out", "spec.md"), "edited after it was recorded\n")
		kept(t, h, r)
	})
	t.Run("retires an output matching any of the hashes recorded at its path", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		r.Outputs["out/spec.md"] = append(r.Outputs["out/spec.md"], digest.Of([]byte("a later spec\n")).Hex())
		retired(t, h, r)
	})
	t.Run("keeps an output matching none of the hashes recorded at its path", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		r.Outputs["out/spec.md"] = append(r.Outputs["out/spec.md"], digest.Of([]byte("a later spec\n")).Hex())
		writeFile(t, filepath.Join(r.Dir, "out", "spec.md"), "edited after it was recorded\n")
		kept(t, h, r)
	})
	t.Run("keeps commits after the merged head", func(t *testing.T) {
		h := newHarness(t)
		r := arrangeMerged(t, h)
		h.Commit(t, r.Dir, "later.txt")
		kept(t, h, r)
	})
	t.Run("refuses an invalid retirement or a plain directory", func(t *testing.T) {
		h := newHarness(t)
		w, base := h.Arrange(t)
		for name, r := range map[string]git.Retirement{
			"invalid head": {Workspace: w, Head: "main"},
			"relative dir": {Workspace: git.Workspace{Repository: w.Repository, Remote: w.Remote, Branch: w.Branch, Dir: "work"}, Head: base},
		} {
			if _, err := h.Retire(ctx, r); !errors.Is(err, git.ErrInvalidWorkspace) {
				t.Errorf("%s: Retire = %v, want ErrInvalidWorkspace", name, err)
			}
		}
		writeFile(t, filepath.Join(w.Dir, "plan.md"), "plan\n")
		if _, err := h.Retire(ctx, git.Retirement{Workspace: w, Head: base}); !errors.Is(err, git.ErrNotWorkspace) {
			t.Errorf("Retire over a plain directory = %v, want ErrNotWorkspace", err)
		}
		if _, err := os.Stat(filepath.Join(w.Dir, "plan.md")); err != nil {
			t.Errorf("Retire changed a plain directory: %v", err)
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

// Workspaces is an in-memory double of git.Program.Ensure, Retire, Tip, IsAncestor and Base for consumers' tests: each working directory it creates
// is a plain directory whose commits it keeps in memory, and it passes RunWorkspaceContract. It never runs git.
// It is safe for concurrent use.
type Workspaces struct {
	mu       sync.Mutex
	defaults map[string]string
	branches map[[2]string]string
	trees    map[string]tree
	commits  map[string]map[string]bool
	parents  map[string]string
	// adds names the file each AdvanceDefault commit adds.
	adds map[string]string
	// recorded is each workspace's default branch as Ensure or FastForward last recorded it, by repository and
	// branch.
	recorded map[[2]string]string
	calls    []git.Workspace
	forwards []git.Workspace
	n        int
	// replay holds the scripted replays and diffs of Rebase and Diff.
	replay rebases
}

// tree is one working directory the double created: its repository, branch and the commit it is at.
type tree struct {
	repository, branch, head string
	// committed holds each file's sha256 as of the tree's last commit.
	committed map[string]string
}

// NewWorkspaces returns a double with no remotes.
func NewWorkspaces() *Workspaces {
	return &Workspaces{defaults: map[string]string{}, branches: map[[2]string]string{}, trees: map[string]tree{}, commits: map[string]map[string]bool{},
		parents: map[string]string{}, recorded: map[[2]string]string{}}
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
		f.record(w)
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
	f.trees[dir] = tree{repository: w.Repository, branch: w.Branch, head: head}
	f.record(w)
	return nil
}

// record records the remote's default branch for the workspace, as git.Program records
// refs/guildhall/default/<branch> on every Ensure; a remote with none leaves the record as it was.
func (f *Workspaces) record(w git.Workspace) {
	if head, ok := f.defaults[w.Remote]; ok {
		f.recorded[[2]string{w.Repository, w.Branch}] = head
		f.add(w.Repository, head)
	}
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
	next := hex.EncodeToString(sum[:])
	f.parents[next] = tr.head
	tr.head = next
	tr.committed = map[string]string{}
	_ = walkFiles(dir, func(rel string, data []byte) { tr.committed[rel] = digest.Of(data).Hex() })
	f.trees[dir] = tr
	f.branches[[2]string{tr.repository, tr.branch}] = next
	f.add(tr.repository, tr.head)
	return tr.head
}

// AdvanceDefault adds a commit adding the file name to remote's default branch, as a merge into main does, and
// returns it.
func (f *Workspaces) AdvanceDefault(remote, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	parent := f.defaults[remote]
	sum := sha1.Sum([]byte(parent + strconv.Itoa(f.n)))
	next := hex.EncodeToString(sum[:])
	f.parents[next] = parent
	if f.adds == nil {
		f.adds = map[string]string{}
	}
	f.adds[next] = name
	f.defaults[remote] = next
	return next
}

// FastForward implements the contract over the double's directories: a tree it created on w.Branch whose head is
// behind the remote's default branch moves to it, unless a file one of the commits between adds already exists in
// the directory, as git refuses to overwrite an untracked file.
func (f *Workspaces) FastForward(ctx context.Context, w git.Workspace) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(w.Repository) || !filepath.IsAbs(w.Dir) || w.Remote == "" || w.Branch == "" || strings.ContainsAny(w.Branch, " ~^:?*[\\") {
		return "", fmt.Errorf("%w: %+v", git.ErrInvalidWorkspace, w)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwards = append(f.forwards, w)
	dir := filepath.Clean(w.Dir)
	tr, ok := f.trees[dir]
	if _, err := os.Stat(dir); !ok || err != nil {
		return "", nil
	}
	main, ok := f.defaults[w.Remote]
	if ok {
		f.recorded[[2]string{w.Repository, w.Branch}] = main
		f.add(w.Repository, main)
	}
	switch {
	case !ok:
		return "the remote " + w.Remote + " has no default branch", nil
	case tr.branch != w.Branch:
		return "the worktree is not on branch " + w.Branch, nil
	case f.merged(main, tr.head):
		return "", nil
	case !f.merged(tr.head, main):
		return "branch " + w.Branch + " is at " + tr.head + ", which has commits that are not on the default branch", nil
	}
	for c := main; c != tr.head; c = f.parents[c] {
		if name := f.adds[c]; name != "" {
			if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
				return "untracked " + name + " would be overwritten", nil
			}
		}
	}
	tr.head = main
	f.trees[dir] = tr
	f.branches[[2]string{tr.repository, tr.branch}] = main
	f.add(tr.repository, main)
	return "", nil
}

// Tip implements git.Program.Tip: the commit the branch is at, an error for a branch that does not exist.
func (f *Workspaces) Tip(ctx context.Context, repository, branch string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tip, ok := f.branches[[2]string{repository, branch}]
	if !ok {
		return "", fmt.Errorf("branch %s of %s does not exist", branch, repository)
	}
	return tip, nil
}

// IsAncestor implements git.Program.IsAncestor over the parents the double's commits record: false for a commit
// the repository does not have.
func (f *Workspaces) IsAncestor(ctx context.Context, repository, commit, head string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	commit, head = f.resolve(repository, commit), f.resolve(repository, head)
	if !f.commits[repository][commit] || !f.commits[repository][head] {
		return false, nil
	}
	return f.merged(commit, head), nil
}

// Behind implements git.Program.Behind over the parents the double's commits record: the commits of ref, a
// branch's DefaultRef or refs/heads/<branch>, that rev does not contain, newest first, with the file each
// AdvanceDefault commit adds, or its id, as its subject.
func (f *Workspaces) Behind(ctx context.Context, repository, rev, ref string) (int, []string, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	head := f.resolve(repository, ref)
	if !strings.HasPrefix(ref, "refs/") || head == ref {
		return 0, nil, fmt.Errorf("ref %q of %s does not exist", ref, repository)
	}
	if !f.commits[repository][rev] {
		return 0, nil, fmt.Errorf("%s has no commit %q", repository, rev)
	}
	var subjects []string
	for c := head; c != "" && !f.merged(c, rev); c = f.parents[c] {
		subject := f.adds[c]
		if subject == "" {
			subject = c
		}
		subjects = append(subjects, subject)
	}
	return len(subjects), subjects, nil
}

// resolve is the commit a branch's DefaultRef or refs/heads/<branch> of repository names, as Ensure and
// FastForward last recorded it; any other name is returned as it is.
func (f *Workspaces) resolve(repository, name string) string {
	if branch, ok := strings.CutPrefix(name, git.DefaultRef("")); ok {
		if c, ok := f.recorded[[2]string{repository, branch}]; ok {
			return c
		}
	}
	if branch, ok := strings.CutPrefix(name, "refs/heads/"); ok {
		if c, ok := f.branches[[2]string{repository, branch}]; ok {
			return c
		}
	}
	return name
}

// Base implements git.Program.Base: the nearest commit of the branch's history that is also in the history of
// the default branch Ensure last recorded for it; a branch with no workspace, before the first Ensure or after
// Retire, is an error.
func (f *Workspaces) Base(ctx context.Context, repository, branch string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]string{repository, branch}
	tip, ok := f.branches[key]
	def, recorded := f.recorded[key]
	if !ok || !recorded {
		return "", fmt.Errorf("branch %s of %s has no workspace", branch, repository)
	}
	for c := tip; c != ""; c = f.parents[c] {
		if f.merged(c, def) {
			return c, nil
		}
	}
	return "", fmt.Errorf("branch %s of %s has no merge base with its default branch", branch, repository)
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

// FastForwards returns every workspace FastForward was asked for after validation, in order.
func (f *Workspaces) FastForwards() []git.Workspace {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]git.Workspace(nil), f.forwards...)
}

// Repository builds, with real git and without the user's or system's git configuration, a bare remote whose
// default branch trunk is at base and a clone of it at repository fetched one commit before base, so a worktree
// that starts at base proves it fetched the remote's default branch. Both are under one new temporary directory.
func Repository(t *testing.T) (repository, remote, base string) {
	t.Helper()
	return buildRepository(t, t.TempDir())
}

// buildRepository builds Repository's fixture in root.
func buildRepository(t *testing.T, root string) (repository, remote, base string) {
	t.Helper()
	global := filepath.Join(root, "gitconfig")
	writeFile(t, global, "")
	git := repositoryGit(t, global)
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

// repositoryGit returns a runner of git in a directory, with the system configuration and every global setting
// but global's excluded, that fails t.
func repositoryGit(t *testing.T, global string) func(dir string, args ...string) string {
	return func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+global)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
}

// Repositories returns a Repository for each test it is called with, built with real git only once per t: each
// call copies that first fixture's files into its own test's temporary directory and points the copy's origin at
// the copy's own remote, so each repository and remote are independent of every other copy and of the first, and
// a case costs one git run instead of building the history. t must outlive every test the result is called with.
func Repositories(t *testing.T) func(*testing.T) (repository, remote, base string) {
	template := t.TempDir()
	var (
		mu    sync.Mutex
		built bool
		head  string
	)
	return func(ct *testing.T) (string, string, string) {
		ct.Helper()
		mu.Lock()
		defer mu.Unlock()
		if !built {
			_, _, head = buildRepository(ct, template)
			built = true
		}
		root := ct.TempDir()
		if err := os.CopyFS(root, os.DirFS(template)); err != nil {
			ct.Fatalf("copy the repository fixture: %v", err)
		}
		repository, remote := filepath.Join(root, "repository"), filepath.Join(root, "remote.git")
		repositoryGit(ct, filepath.Join(root, "gitconfig"))(repository, "remote", "set-url", "origin", remote)
		return repository, remote, head
	}
}

// Retire implements the contract over the double's directories, where every file is untracked.
func (f *Workspaces) Retire(ctx context.Context, r git.Retirement) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(r.Repository) || !filepath.IsAbs(r.Dir) || r.Branch == "" || !digest.ValidHead(r.Head) {
		return nil, fmt.Errorf("%w: %+v", git.ErrInvalidWorkspace, r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Clean(r.Dir)
	branch := [2]string{r.Repository, r.Branch}
	var kept []string
	if tip, ok := f.branches[branch]; ok && !f.merged(tip, r.Head) {
		kept = append(kept, "branch "+r.Branch+" is at "+tip+", which was not merged")
	}
	tr, ours := f.trees[dir]
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		ours = false
	} else if err != nil {
		return nil, err
	} else if !ours {
		return nil, fmt.Errorf("%w: %s", git.ErrNotWorkspace, dir)
	}
	if ours {
		if !f.merged(tr.head, r.Head) {
			kept = append(kept, "HEAD is at "+tr.head+", which was not merged")
		}
		err := walkFiles(dir, func(rel string, data []byte) {
			sum := digest.Of(data).Hex()
			if tr.committed[rel] != sum && !slices.Contains(r.Outputs[rel], sum) {
				kept = append(kept, "changed or untracked "+rel)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	if len(kept) > 0 {
		return kept, nil
	}
	if ours {
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
		delete(f.trees, dir)
	}
	delete(f.branches, branch)
	delete(f.recorded, branch)
	return nil, nil
}

// merged reports whether commit is head or one of its ancestors.
func (f *Workspaces) merged(commit, head string) bool {
	for c := head; c != ""; c = f.parents[c] {
		if c == commit {
			return true
		}
	}
	return false
}

// walkFiles calls fn with every file under dir, by slash path relative to it, and its bytes.
func walkFiles(dir string, fn func(rel string, data []byte)) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err == nil {
			fn(filepath.ToSlash(rel), data)
		}
		return err
	})
}
