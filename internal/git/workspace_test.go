package git_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

func TestWorkspaceContract(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		repositories := gittest.Repositories(t)
		gittest.RunWorkspaceContract(t, func(t *testing.T) gittest.WorkspaceHarness {
			return programHarness(t, repositories)
		})
	})
	t.Run("double", func(t *testing.T) {
		gittest.RunWorkspaceContract(t, func(t *testing.T) gittest.WorkspaceHarness {
			f := gittest.NewWorkspaces()
			return gittest.WorkspaceHarness{
				Ensure:         f.Ensure,
				Retire:         f.Retire,
				FastForward:    f.FastForward,
				AdvanceDefault: func(t *testing.T, w git.Workspace, name string) string { return f.AdvanceDefault(w.Remote, name) },
				Tip:            f.Tip, IsAncestor: f.IsAncestor, Base: f.Base, Behind: f.Behind,
				Arrange: func(t *testing.T) (git.Workspace, string) {
					base := strings.Repeat("b", 40)
					f.SetDefault("remote", base)
					dir := t.TempDir()
					if err := os.Mkdir(filepath.Join(dir, "t-1"), 0o755); err != nil {
						t.Fatal(err)
					}
					return git.Workspace{Repository: filepath.Join(dir, "repo"), Remote: "remote", Branch: "guildhall/t-1", Dir: filepath.Join(dir, "t-1", "work")}, base
				},
				Head: func(t *testing.T, dir string) string { return f.Head(dir) },
				Commit: func(t *testing.T, dir, name string) string {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
						t.Fatal(err)
					}
					return f.Commit(dir)
				},
				Contains:   func(t *testing.T, repository, head string) bool { return f.Contains(repository, head) },
				Regenerate: f.Regenerate,
				ArrangeRegeneration: func(t *testing.T, w git.Workspace) (git.Regeneration, git.Regeneration, string) {
					const golden = "digests/x.sha256"
					onto := f.Head(w.Dir)
					marked := func(head string) string {
						f.SetRebase(onto, head, git.Rebased{Marked: strings.Repeat(head[:1], 40),
							Conflicts: []git.Conflict{{Path: golden, Lines: 2, Markers: true}}})
						r, err := f.Rebase(context.Background(), git.Replay{Repository: w.Repository, Base: onto, Head: head})
						if err != nil {
							t.Fatal(err)
						}
						return r.Marked
					}
					rewrite := git.Regeneration{Repository: w.Repository, Marked: marked(strings.Repeat("c", 40)), Onto: onto,
						Take: []string{golden}, Commands: [][]string{{"regenerate"}}, Dir: w.Dir + ".regen"}
					f.SetRegenerate(rewrite.Marked, git.Regenerated{Changed: []string{golden}})
					fail := rewrite
					fail.Marked = marked(strings.Repeat("d", 40))
					return rewrite, fail, golden
				},
			}
		})
	})
}

// isolate keeps the user's and system's git configuration out of every git the test runs, Program's included.
func isolate(t *testing.T) string {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return global
}

// arrangeWorkspace makes a repository and its remote from repositories (gittest.Repositories) and a workspace of
// it whose directory's parent exists, and returns it with the base a new worktree must start at.
func arrangeWorkspace(t *testing.T, repositories func(*testing.T) (string, string, string)) (git.Workspace, string) {
	t.Helper()
	repository, remote, base := repositories(t)
	tasks := filepath.Join(filepath.Dir(repository), "state", "app", "t-1")
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		t.Fatal(err)
	}
	return git.Workspace{Repository: repository, Remote: remote, Branch: "guildhall/t-1", Dir: filepath.Join(tasks, "work")}, base
}

func programHarness(t *testing.T, repositories func(*testing.T) (string, string, string)) gittest.WorkspaceHarness {
	global := isolate(t)
	return gittest.WorkspaceHarness{
		Ensure:      git.Program{}.Ensure,
		Retire:      git.Program{}.Retire,
		FastForward: git.Program{}.FastForward,
		AdvanceDefault: func(t *testing.T, w git.Workspace, name string) string {
			return advanceDefault(t, global, w.Remote, name)
		},
		Tip: git.Program{}.Tip, IsAncestor: git.Program{}.IsAncestor, Base: git.Program{}.Base, Behind: git.Program{}.Behind,
		Arrange: func(t *testing.T) (git.Workspace, string) { return arrangeWorkspace(t, repositories) },
		Head:    func(t *testing.T, dir string) string { return repo{t, dir, global}.run("rev-parse", "HEAD") },
		Commit: func(t *testing.T, dir, name string) string {
			return repo{t, dir, global}.commit("attempt", map[string]string{name: name + "\n"})
		},
		Contains: func(t *testing.T, repository, head string) bool {
			return exec.Command("git", "-C", repository, "cat-file", "-e", head+"^{commit}").Run() == nil
		},
		Regenerate: git.Program{}.Regenerate,
		ArrangeRegeneration: func(t *testing.T, w git.Workspace) (git.Regeneration, git.Regeneration, string) {
			g := arrangeRegeneration(t, global, w.Repository, repo{t, w.Dir, global}.run("rev-parse", "HEAD"))
			fail := g
			fail.Commands = [][]string{helperCommand(t, "write", g.Take[0], "partial\n"), helperCommand(t, "fail")}
			return g, fail, g.Take[0]
		},
	}
}

// regenerateHelper, as this test binary's first argument, makes it a regenerate command (see regenerate).
const regenerateHelper = "regenerate-helper"

// regenerate is the regenerate helper: "write <path> <data>" writes data to path, and "fail" says so and exits 3.
func regenerate(args []string) int {
	switch {
	case len(args) == 3 && args[0] == "write":
		if err := os.WriteFile(filepath.FromSlash(args[1]), []byte(args[2]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", args[1])
		return 0
	case len(args) == 1 && args[0] == "fail":
		fmt.Println("the golden generator failed")
		return 3
	}
	fmt.Fprintln(os.Stderr, "regenerate helper: unknown arguments", args)
	return 2
}

// helperCommand is an argv that runs this test binary as the regenerate helper with args.
func helperCommand(t *testing.T, args ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{exe, regenerateHelper}, args...)
}

// arrangeRegeneration makes, in repository, two commits on base that both change the golden digests/x.sha256 and
// replays one onto the other, and returns a regeneration of the marked replay, in a scratch directory beside the
// repository, whose command writes the golden.
func arrangeRegeneration(t *testing.T, global, repository, base string) git.Regeneration {
	t.Helper()
	const golden = "digests/x.sha256"
	side := filepath.Join(t.TempDir(), "side")
	r := repo{t, repository, global}
	r.run("worktree", "add", "--detach", "--quiet", side, base)
	defer r.run("worktree", "remove", "--force", side)
	digests := func() {
		if err := os.MkdirAll(filepath.Join(side, "digests"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	digests()
	s := repo{t, side, global}
	onto := s.commit("main's golden", map[string]string{golden: "main\n"})
	s.run("reset", "-q", "--hard", base)
	digests()
	task := s.commit("the task's golden", map[string]string{golden: "task\n", "a.go": "package a\n"})
	rebased, err := git.Program{}.Rebase(context.Background(), git.Replay{Repository: repository, Base: onto, Head: task})
	if err != nil || rebased.Marked == "" || len(rebased.Conflicts) != 1 || !rebased.Conflicts[0].Markers {
		t.Fatalf("Rebase = %+v, %v; want one conflict with markers", rebased, err)
	}
	return git.Regeneration{Repository: repository, Marked: rebased.Marked, Onto: onto, Take: []string{golden},
		Commands: [][]string{helperCommand(t, "write", golden, "regenerated\n")}, Dir: filepath.Join(filepath.Dir(repository), "scratch", "work.regen")}
}

// TestRegenerateProgram pins what only git shows: the regenerated golden holds the command's output and no
// markers, the base side of each Take path is what the command starts from, and a failing command's output and exit
// status are reported.
func TestRegenerateProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	w, base := arrangeWorkspace(t, gittest.Repositories(t))
	if err := (git.Program{}).Ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	g := arrangeRegeneration(t, global, w.Repository, base)
	r := repo{t, w.Repository, global}
	// A command that does nothing leaves the base side: the conflict is gone, with main's line.
	g.Commands = [][]string{helperCommand(t, "write", "other.txt", "x\n")}
	got, err := git.Program{}.Regenerate(ctx, g)
	if err != nil || got.Failed != "" {
		t.Fatalf("Regenerate = %+v, %v", got, err)
	}
	if b := r.run("show", got.Commit+":digests/x.sha256"); b != "main" {
		t.Errorf("golden after taking the base side = %q, want main", b)
	}
	if !slices.Equal(got.Changed, []string{"digests/x.sha256", "other.txt"}) {
		t.Errorf("Changed = %q, want the golden and the untracked file the command wrote", got.Changed)
	}
	if b := r.run("show", got.Commit+":a.go"); b != "package a" {
		t.Errorf("a.go = %q, want the task's", b)
	}
	if r.run("log", "-1", "--format=%s", got.Commit) != "the task's golden" || r.run("rev-parse", got.Commit+"^") != g.Marked {
		t.Errorf("regenerated commit %s does not take the marked commit's message and parent", got.Commit)
	}
	// The generator's output replaces the golden, with no markers.
	g = arrangeRegeneration(t, global, w.Repository, base)
	got, err = git.Program{}.Regenerate(ctx, g)
	if err != nil || got.Failed != "" {
		t.Fatalf("Regenerate = %+v, %v", got, err)
	}
	if b := r.run("show", got.Commit+":digests/x.sha256"); b != "regenerated" || strings.Contains(b, "<<<<<<<") {
		t.Errorf("regenerated golden = %q, want the generator's output", b)
	}
	if !strings.Contains(string(got.Output), "wrote digests/x.sha256") {
		t.Errorf("Output = %q, want the command's output", got.Output)
	}
	// A failing command names itself and its exit status, with its output.
	g.Commands = [][]string{helperCommand(t, "fail")}
	got, err = git.Program{}.Regenerate(ctx, g)
	if err != nil || got.Commit != "" || !strings.Contains(got.Failed, "exit status 3") || !strings.Contains(got.Failed, regenerateHelper) ||
		!strings.Contains(string(got.Output), "the golden generator failed") {
		t.Errorf("Regenerate with a failing command = %+v, %v", got, err)
	}
	// A command that cannot start fails the same way.
	g.Commands = [][]string{{filepath.Join(t.TempDir(), "no-such-generator")}}
	if got, err := (git.Program{}).Regenerate(ctx, g); err != nil || got.Failed == "" {
		t.Errorf("Regenerate with a missing command = %+v, %v", got, err)
	}
	// A leftover scratch worktree from a crashed run is replaced.
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g.Dir, "stale.txt"), "stale\n")
	g.Commands = [][]string{helperCommand(t, "write", "digests/x.sha256", "again\n")}
	if got, err := (git.Program{}).Regenerate(ctx, g); err != nil || !slices.Equal(got.Changed, []string{"digests/x.sha256"}) {
		t.Errorf("Regenerate over a leftover directory = %+v, %v", got, err)
	}
	if _, err := os.Stat(g.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scratch worktree left behind: %v", err)
	}
	for name, bad := range map[string]func(*git.Regeneration){
		"short marked":  func(g *git.Regeneration) { g.Marked = "HEAD" },
		"relative dir":  func(g *git.Regeneration) { g.Dir = "work.regen" },
		"no commands":   func(g *git.Regeneration) { g.Commands = nil },
		"empty command": func(g *git.Regeneration) { g.Commands = [][]string{{}} },
	} {
		v := g
		bad(&v)
		if _, err := (git.Program{}).Regenerate(ctx, v); err == nil {
			t.Errorf("%s: Regenerate succeeded", name)
		}
	}
}

// advanceDefault pushes a commit adding the file name onto the remote's default branch, trunk, from a clone of its
// own, as a dependency's merge does, and returns it.
func advanceDefault(t *testing.T, global, remote, name string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "merger")
	repo{t, filepath.Dir(clone), global}.run("clone", "-q", remote, clone)
	r := repo{t, clone, global}
	commit := r.commit("merge "+name, map[string]string{name: name + "\n"})
	r.run("push", "-q", "origin", "HEAD:trunk")
	return commit
}

// TestFastForwardProgram pins what only git shows about FastForward: it records the remote's default branch as
// DefaultRef, so Base moves with it, and tracked changes that do not conflict are carried along.
func TestFastForwardProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repositories := gittest.Repositories(t)
	w, base := arrangeWorkspace(t, repositories)
	if err := (git.Program{}).Ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r := repo{t, w.Repository, global}
	if got := r.run("rev-parse", git.DefaultRef(w.Branch)); got != base {
		t.Fatalf("DefaultRef after Ensure = %s, want %s", got, base)
	}
	writeFile(t, filepath.Join(w.Dir, "a.txt"), "edited\n")
	main := advanceDefault(t, global, w.Remote, "merged.txt")
	if kept, err := (git.Program{}).FastForward(ctx, w); err != nil || kept != "" {
		t.Fatalf("FastForward = %q, %v", kept, err)
	}
	if got := r.run("rev-parse", git.DefaultRef(w.Branch)); got != main {
		t.Errorf("DefaultRef after FastForward = %s, want %s", got, main)
	}
	if got, err := (git.Program{}).Base(ctx, w.Repository, w.Branch); err != nil || got != main {
		t.Errorf("Base after FastForward = %s, %v; want %s", got, err, main)
	}
	if got := r.run("rev-parse", "refs/heads/"+w.Branch); got != main {
		t.Errorf("branch at %s, want %s", got, main)
	}
	if b, err := os.ReadFile(filepath.Join(w.Dir, "a.txt")); err != nil || string(b) != "edited\n" {
		t.Errorf("FastForward lost a tracked change: %q, %v", b, err)
	}
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (git.Program{}).FastForward(ended, w); err == nil {
		t.Error("FastForward with an ended context succeeded")
	}
	t.Run("a worktree on another branch is kept", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		if err := (git.Program{}).Ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		repo{t, w.Dir, global}.run("checkout", "-q", "-b", "experiment")
		advanceDefault(t, global, w.Remote, "merged.txt")
		if kept, err := (git.Program{}).FastForward(ctx, w); err != nil || kept == "" {
			t.Errorf("FastForward of a worktree on another branch = %q, %v; want it kept", kept, err)
		}
	})
}

// TestEnsureProgram pins the behaviour only real git shows: recovery from an interrupted creation, a branch kept
// across a removed worktree, other repositories' directories and pushing a worktree's commit from the repository.
func TestEnsureProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repositories := gittest.Repositories(t)
	ensure := func(t *testing.T, w git.Workspace) {
		t.Helper()
		if err := (git.Program{}).Ensure(ctx, w); err != nil {
			t.Fatalf("Ensure = %v", err)
		}
	}
	head := func(t *testing.T, dir string) string { return repo{t, dir, global}.run("rev-parse", "HEAD") }
	t.Run("an interrupted creation is finished", func(t *testing.T) {
		for name, leave := range map[string]func(t *testing.T, w git.Workspace){
			// git worktree add finished but the move into place did not happen.
			"registered": func(t *testing.T, w git.Workspace) {
				r := repo{t, w.Repository, global}
				r.run("branch", w.Branch, "origin/trunk")
				r.run("worktree", "add", "-q", w.Dir+".partial", w.Branch)
				writeFile(t, filepath.Join(w.Dir+".partial", "half"), "x")
			},
			// A directory git had begun but not registered.
			"unregistered": func(t *testing.T, w git.Workspace) {
				writeFile(t, filepath.Join(w.Dir+".partial", "half"), "x")
			},
		} {
			t.Run(name, func(t *testing.T) {
				w, base := arrangeWorkspace(t, repositories)
				leave(t, w)
				ensure(t, w)
				if _, err := os.Stat(w.Dir + ".partial"); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the partial worktree is still there: %v", err)
				}
				if _, err := os.Stat(filepath.Join(w.Dir, "half")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the worktree holds the partial one's file: %v", err)
				}
				// A branch left by the interrupted creation is used as it is.
				want := base
				if name == "registered" {
					want = repo{t, w.Repository, global}.run("rev-parse", "origin/trunk")
				}
				if got := head(t, w.Dir); got != want {
					t.Errorf("worktree at %s, want %s", got, want)
				}
				list := repo{t, w.Repository, global}.run("worktree", "list", "--porcelain")
				if strings.Contains(list, ".partial") {
					t.Errorf("the partial worktree is still registered:\n%s", list)
				}
			})
		}
	})
	t.Run("a removed worktree keeps its branch's commits", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		ensure(t, w)
		commit := repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		repo{t, w.Repository, global}.run("worktree", "remove", w.Dir)
		ensure(t, w)
		if got := head(t, w.Dir); got != commit {
			t.Errorf("worktree created again at %s, want the branch's %s", got, commit)
		}
	})
	t.Run("a worktree deleted without git is created again", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		ensure(t, w)
		commit := repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		if err := os.RemoveAll(w.Dir); err != nil {
			t.Fatal(err)
		}
		ensure(t, w)
		if got := head(t, w.Dir); got != commit {
			t.Errorf("worktree created again at %s, want the branch's %s", got, commit)
		}
	})
	t.Run("an agent may move the worktree's branch", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		ensure(t, w)
		r := repo{t, w.Dir, global}
		r.run("checkout", "-q", "-b", "experiment")
		commit := r.commit("attempt", map[string]string{"c.txt": "c\n"})
		ensure(t, w)
		if got := head(t, w.Dir); got != commit {
			t.Errorf("reused worktree at %s, want %s", got, commit)
		}
	})
	t.Run("refuses a checkout of another repository", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		other, _ := arrangeWorkspace(t, repositories)
		repo{t, other.Repository, global}.run("worktree", "add", "-q", w.Dir)
		if err := (git.Program{}).Ensure(ctx, w); !errors.Is(err, git.ErrNotWorkspace) {
			t.Errorf("Ensure over another repository's worktree = %v, want ErrNotWorkspace", err)
		}
		w.Dir = filepath.Join(other.Repository, "sub")
		writeFile(t, filepath.Join(w.Dir, "f"), "f")
		if err := (git.Program{}).Ensure(ctx, w); !errors.Is(err, git.ErrNotWorkspace) {
			t.Errorf("Ensure over a directory inside another repository = %v, want ErrNotWorkspace", err)
		}
	})
	t.Run("refuses a subdirectory of the repository's own checkout", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		w.Dir = filepath.Join(w.Repository, "sub")
		writeFile(t, filepath.Join(w.Dir, "f"), "f")
		if err := (git.Program{}).Ensure(ctx, w); !errors.Is(err, git.ErrNotWorkspace) {
			t.Errorf("Ensure over a subdirectory of the repository = %v, want ErrNotWorkspace", err)
		}
	})
	t.Run("an unreachable remote creates nothing", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		w.Remote = filepath.Join(t.TempDir(), "missing.git")
		err := (git.Program{}).Ensure(ctx, w)
		if err == nil || !strings.Contains(err.Error(), "fetch") {
			t.Fatalf("Ensure from an unreachable remote = %v, want git's fetch failure", err)
		}
		for _, p := range []string{w.Dir, w.Dir + ".partial"} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s exists after a failed Ensure: %v", p, err)
			}
		}
	})
	// The edge publish needs (forge.GitHub.Publish): a worktree's commit is a commit of the repository, and the
	// repository pushes it to the task branch.
	t.Run("a worktree's commit is pushed from the repository", func(t *testing.T) {
		w, _ := arrangeWorkspace(t, repositories)
		ensure(t, w)
		commit := repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		r := repo{t, w.Repository, global}
		r.run("cat-file", "-e", commit+"^{commit}")
		r.run("push", "--quiet", "--porcelain", w.Remote, "+"+commit+":refs/heads/"+w.Branch)
		if got := (repo{t, w.Remote, global}).run("rev-parse", "refs/heads/"+w.Branch); got != commit {
			t.Errorf("remote task branch at %s, want %s", got, commit)
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

// TestRetireProgram pins what only git shows about retiring: tracked changes, ignored files, the branch's own tip
// and an interrupted retirement.
func TestRetireProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repositories := gittest.Repositories(t)
	merged := func(t *testing.T) (git.Retirement, repo) {
		t.Helper()
		w, _ := arrangeWorkspace(t, repositories)
		if err := (git.Program{}).Ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		r := repo{t, w.Dir, global}
		head := r.commit("change", map[string]string{".gitignore": "build/\n", "c.txt": "c\n"})
		return git.Retirement{Workspace: w, Head: head}, r
	}
	branch := func(t *testing.T, ret git.Retirement) bool {
		return exec.Command("git", "-C", ret.Repository, "show-ref", "--verify", "--quiet", "refs/heads/"+ret.Branch).Run() == nil
	}
	retire := func(t *testing.T, ret git.Retirement) []string {
		t.Helper()
		kept, err := (git.Program{}).Retire(ctx, ret)
		if err != nil {
			t.Fatalf("Retire = %v", err)
		}
		return kept
	}
	t.Run("removes the worktree and its branch, keeping other branches", func(t *testing.T) {
		ret, r := merged(t)
		r.run("branch", "experiment")
		writeFile(t, filepath.Join(ret.Dir, "build", "out.bin"), "ignored")
		if kept := retire(t, ret); len(kept) > 0 {
			t.Fatalf("kept %q", kept)
		}
		if branch(t, ret) {
			t.Error("the task branch survived its retirement")
		}
		if exec.Command("git", "-C", ret.Repository, "show-ref", "--verify", "--quiet", "refs/heads/experiment").Run() != nil {
			t.Error("retiring deleted another branch")
		}
		if list := (repo{t, ret.Repository, global}).run("worktree", "list", "--porcelain"); strings.Contains(filepath.ToSlash(list), "/work") {
			t.Errorf("the worktree is still registered:\n%s", list)
		}
	})
	t.Run("a worktree behind the merged head is retired", func(t *testing.T) {
		ret, r := merged(t)
		ret.Head = r.commit("more", map[string]string{"d.txt": "d\n"})
		r.run("reset", "-q", "--hard", "HEAD~1")
		if kept := retire(t, ret); len(kept) > 0 {
			t.Fatalf("kept %q", kept)
		}
		if _, err := os.Stat(ret.Dir); !errors.Is(err, os.ErrNotExist) || branch(t, ret) {
			t.Errorf("worktree there: %v, branch there %v; want both removed", err, branch(t, ret))
		}
	})
	t.Run("keeps a changed tracked file", func(t *testing.T) {
		ret, _ := merged(t)
		writeFile(t, filepath.Join(ret.Dir, "c.txt"), "edited\n")
		if kept := retire(t, ret); len(kept) == 0 {
			t.Fatal("retired a worktree with a changed file")
		}
		if b, err := os.ReadFile(filepath.Join(ret.Dir, "c.txt")); err != nil || string(b) != "edited\n" {
			t.Errorf("kept worktree lost its change: %q, %v", b, err)
		}
	})
	t.Run("keeps a branch whose tip was not merged", func(t *testing.T) {
		ret, r := merged(t)
		r.commit("unmerged", map[string]string{"e.txt": "e\n"})
		r.run("checkout", "-q", "--detach", ret.Head)
		kept := retire(t, ret)
		if len(kept) == 0 || !branch(t, ret) {
			t.Fatalf("kept %q, branch there %v; want both kept", kept, branch(t, ret))
		}
		if _, err := os.Stat(ret.Dir); err != nil {
			t.Errorf("kept worktree removed: %v", err)
		}
	})
	t.Run("an interrupted retirement deletes the branch", func(t *testing.T) {
		ret, _ := merged(t)
		(repo{t, ret.Repository, global}).run("worktree", "remove", ret.Dir)
		if kept := retire(t, ret); len(kept) > 0 || branch(t, ret) {
			t.Fatalf("kept %q, branch there %v; want the branch deleted", kept, branch(t, ret))
		}
	})
	// forgeHead commits on the remote's default branch from a clone of its own and pushes it only to a pull request
	// ref, as a forge's "Update branch" before a merge outside Guildhall does, so the repository lacks it.
	forgeHead := func(t *testing.T, ret git.Retirement) string {
		t.Helper()
		clone := filepath.Join(t.TempDir(), "forge")
		repo{t, filepath.Dir(clone), global}.run("clone", "-q", ret.Remote, clone)
		r := repo{t, clone, global}
		head := r.commit("update branch", map[string]string{"f.txt": "f\n"})
		r.run("push", "-q", "origin", "HEAD:refs/pull/1/head")
		if exec.Command("git", "-C", ret.Repository, "cat-file", "-e", head+"^{commit}").Run() == nil {
			t.Fatal("the repository already has the forge's head")
		}
		return head
	}
	unmergedWorkspace := func(t *testing.T) git.Retirement {
		t.Helper()
		w, _ := arrangeWorkspace(t, repositories)
		if err := (git.Program{}).Ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		return git.Retirement{Workspace: w}
	}
	t.Run("a merged head the repository lacks is fetched from the remote", func(t *testing.T) {
		ret := unmergedWorkspace(t)
		ret.Head = forgeHead(t, ret)
		if kept := retire(t, ret); len(kept) > 0 {
			t.Fatalf("kept %q", kept)
		}
		if _, err := os.Stat(ret.Dir); !errors.Is(err, os.ErrNotExist) || branch(t, ret) {
			t.Errorf("worktree there: %v, branch there %v; want both removed", err, branch(t, ret))
		}
	})
	t.Run("a merged head that cannot be fetched is an error", func(t *testing.T) {
		ret := unmergedWorkspace(t)
		ret.Head = forgeHead(t, ret)
		ret.Remote = filepath.Join(t.TempDir(), "gone.git")
		if _, err := (git.Program{}).Retire(ctx, ret); err == nil || !strings.Contains(err.Error(), " fetch ") {
			t.Fatalf("Retire with an unreachable remote = %v, want the fetch's error", err)
		}
		if _, err := os.Stat(ret.Dir); err != nil || !branch(t, ret) {
			t.Errorf("worktree there: %v, branch there %v; want both kept", err, branch(t, ret))
		}
	})
	t.Run("a head the repository lacks is an error", func(t *testing.T) {
		ret, _ := merged(t)
		ret.Head = strings.Repeat("0", 40)
		if _, err := (git.Program{}).Retire(ctx, ret); err == nil {
			t.Fatal("Retire at an unknown head succeeded")
		}
		if _, err := os.Stat(ret.Dir); err != nil {
			t.Errorf("worktree removed: %v", err)
		}
	})
}
