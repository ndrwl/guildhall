package git_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
)

// lines is a file of n numbered lines, with line i replaced by with[i].
func lines(n int, with map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if v, ok := with[i]; ok {
			b.WriteString(v + "\n")
			continue
		}
		b.WriteString("line " + string(rune('a'+i-1)) + "\n")
	}
	return b.String()
}

// rebaseRepo is a repository whose base commit holds a.txt (ten lines) and b.txt, a task branch that changes line 2
// of a.txt, and main, which change applies to; it returns the repository, the base main reached and the task head.
func rebaseRepo(t *testing.T, change func(r repo)) (r repo, main, task string) {
	t.Helper()
	global := isolate(t)
	r = repo{t, t.TempDir(), global}
	r.run("init", "-q", "-b", "trunk")
	r.commit("base", map[string]string{"a.txt": lines(10, nil), "b.txt": "b\n"})
	r.run("checkout", "-q", "-b", "guildhall/t-1")
	task = r.commit("task", map[string]string{"a.txt": lines(10, map[int]string{2: "task two"})})
	r.run("checkout", "-q", "trunk")
	change(r)
	main = r.run("rev-parse", "HEAD")
	r.run("checkout", "-q", "guildhall/t-1")
	return r, main, task
}

func TestRebaseProgram(t *testing.T) {
	ctx := context.Background()
	p := git.Program{}
	for name, tc := range map[string]struct {
		change    func(r repo)
		conflicts []git.Conflict
		equal     bool
	}{
		"clean replay": {func(r repo) { r.commit("main", map[string]string{"c.txt": "c\n"}) }, nil, true},
		"main touches a task file elsewhere": {func(r repo) {
			r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{9: "main nine"})})
		}, nil, true},
		"main changes a context line": {func(r repo) {
			r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{4: "main four"})})
		}, nil, false},
		"one-line conflict": {func(r repo) {
			r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{2: "main two"})})
		}, []git.Conflict{{Path: "a.txt", Lines: 2, Markers: true}}, false},
		"wider conflict": {func(r repo) {
			r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{1: "main one", 2: "main two", 3: "main three"})})
		}, []git.Conflict{{Path: "a.txt", Lines: 6, Markers: true}}, false},
		"delete/modify conflict has no markers": {func(r repo) {
			r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{2: "main two"})})
			r.run("rm", "-q", "b.txt")
			r.run("commit", "-q", "-m", "drop b")
		}, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, main, task := rebaseRepo(t, tc.change)
			if name == "delete/modify conflict has no markers" {
				r.commit("task b", map[string]string{"b.txt": "task b\n"})
				task = r.run("rev-parse", "HEAD")
				tc.conflicts = []git.Conflict{{Path: "a.txt", Lines: 2, Markers: true}, {Path: "b.txt"}}
			}
			got, err := p.Rebase(ctx, git.Replay{Repository: r.dir, Base: main, Head: task})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Conflicts, append([]git.Conflict{}, tc.conflicts...)) {
				t.Fatalf("conflicts = %+v, want %+v", got.Conflicts, tc.conflicts)
			}
			if got.Base != main || got.Head != task || got.HeadPatchID == "" {
				t.Fatalf("rebased = %+v", got)
			}
			if len(tc.conflicts) > 0 {
				if got.Commit != "" || got.Marked == "" || got.PatchID != "" {
					t.Fatalf("conflicting replay = %+v, want only a marked commit", got)
				}
				if parent := r.run("rev-parse", got.Marked+"^"); parent != main {
					t.Fatalf("marked parent = %s, want %s", parent, main)
				}
				return
			}
			if got.Marked != "" || got.Commit == "" {
				t.Fatalf("clean replay = %+v", got)
			}
			if parent := r.run("rev-parse", got.Commit+"^"); parent != main {
				t.Fatalf("replayed parent = %s, want %s", parent, main)
			}
			if (got.PatchID == got.HeadPatchID) != tc.equal {
				t.Fatalf("patch ids %s and %s: equal = %v, want %v", got.HeadPatchID, got.PatchID, !tc.equal, tc.equal)
			}
			if r.run("rev-parse", "HEAD") != task || r.run("rev-parse", "guildhall/t-1") != task {
				t.Fatal("Rebase moved the branch or worktree")
			}
			again, err := p.Rebase(ctx, git.Replay{Repository: r.dir, Base: main, Head: task})
			if err != nil || again.Commit != got.Commit {
				t.Fatalf("second replay = %s, %v; want the same commit %s", again.Commit, err, got.Commit)
			}
			if msg := r.run("log", "-1", "--format=%s%n%an", got.Commit); msg != "task\nt" {
				t.Fatalf("replayed commit message and author = %q", msg)
			}
		})
	}
}

func TestRebaseRefusesNonObjectIDs(t *testing.T) {
	if _, err := (git.Program{}).Rebase(context.Background(), git.Replay{Repository: t.TempDir(), Base: "main", Head: strings.Repeat("a", 40)}); err == nil {
		t.Fatal("Rebase of a branch name succeeded")
	}
}

// TestSquashAndAdoptProgram resolves a conflict on the marked head as a rebaser does, squashes it onto the base, and
// moves the worktree there with untracked stage outputs kept; Adopt refuses a worktree at another commit.
func TestSquashAndAdoptProgram(t *testing.T) {
	ctx := context.Background()
	p := git.Program{}
	r, main, task := rebaseRepo(t, func(r repo) {
		r.commit("main", map[string]string{"a.txt": lines(10, map[int]string{2: "main two"})})
	})
	work := filepath.Join(t.TempDir(), "work")
	r.run("checkout", "-q", "trunk")
	r.run("worktree", "add", "-q", work, "guildhall/t-1")
	w := git.Workspace{Repository: r.dir, Remote: r.dir, Branch: "guildhall/t-1", Dir: work}
	rb, err := p.Rebase(ctx, git.Replay{Repository: r.dir, Base: main, Head: task})
	if err != nil || rb.Marked == "" {
		t.Fatalf("Rebase = %+v, %v", rb, err)
	}
	if err := os.WriteFile(filepath.Join(work, "notes.md"), []byte("stage output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Adopt(ctx, w, main, rb.Marked); err == nil {
		t.Fatal("Adopt from a commit the worktree is not at succeeded")
	}
	if err := p.Adopt(ctx, w, task, rb.Marked); err != nil {
		t.Fatal(err)
	}
	if err := p.Adopt(ctx, w, task, rb.Marked); err != nil {
		t.Fatalf("Adopt again = %v, want no change", err)
	}
	wt := repo{t, work, r.global}
	if data, _ := os.ReadFile(filepath.Join(work, "a.txt")); !strings.Contains(string(data), "<<<<<<<") {
		t.Fatalf("marked a.txt = %q", data)
	}
	resolved := wt.commit("resolve", map[string]string{"a.txt": lines(10, map[int]string{2: "main and task two"})})
	squashed, err := p.Squash(ctx, r.dir, main, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if parent := r.run("rev-parse", squashed+"^"); parent != main {
		t.Fatalf("squashed parent = %s", parent)
	}
	if a, b := r.run("rev-parse", squashed+"^{tree}"), r.run("rev-parse", resolved+"^{tree}"); a != b {
		t.Fatal("squash changed the tree")
	}
	if again, err := p.Squash(ctx, r.dir, main, resolved); err != nil || again != squashed {
		t.Fatalf("second squash = %s, %v", again, err)
	}
	if err := p.Adopt(ctx, w, resolved, squashed); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "notes.md")); err != nil || string(data) != "stage output\n" {
		t.Fatalf("untracked output after Adopt = %q, %v", data, err)
	}
	if wt.run("rev-parse", "HEAD") != squashed {
		t.Fatal("worktree not at the squashed head")
	}
	diff, err := p.Diff(ctx, r.dir, rb.Marked, squashed)
	if err != nil || !strings.Contains(string(diff), "+line b") && !strings.Contains(string(diff), "+main and task two") {
		t.Fatalf("resolution diff = %s, %v", diff, err)
	}
}
