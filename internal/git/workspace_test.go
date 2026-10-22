package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

func TestWorkspaceContract(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		gittest.RunWorkspaceContract(t, func(t *testing.T) gittest.WorkspaceHarness {
			return programHarness(t)
		})
	})
	t.Run("double", func(t *testing.T) {
		gittest.RunWorkspaceContract(t, func(t *testing.T) gittest.WorkspaceHarness {
			f := gittest.NewWorkspaces()
			return gittest.WorkspaceHarness{
				Ensure: f.Ensure,
				Retire: f.Retire,
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
				Contains: func(t *testing.T, repository, head string) bool { return f.Contains(repository, head) },
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

// arrangeWorkspace makes a repository and its remote (gittest.Repository) and a workspace of it whose directory's
// parent exists, and returns it with the base a new worktree must start at.
func arrangeWorkspace(t *testing.T) (git.Workspace, string) {
	t.Helper()
	repository, remote, base := gittest.Repository(t)
	tasks := filepath.Join(filepath.Dir(repository), "state", "app", "t-1")
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		t.Fatal(err)
	}
	return git.Workspace{Repository: repository, Remote: remote, Branch: "guildhall/t-1", Dir: filepath.Join(tasks, "work")}, base
}

func programHarness(t *testing.T) gittest.WorkspaceHarness {
	global := isolate(t)
	return gittest.WorkspaceHarness{
		Ensure:  git.Program{}.Ensure,
		Retire:  git.Program{}.Retire,
		Arrange: func(t *testing.T) (git.Workspace, string) { return arrangeWorkspace(t) },
		Head:    func(t *testing.T, dir string) string { return repo{t, dir, global}.run("rev-parse", "HEAD") },
		Commit: func(t *testing.T, dir, name string) string {
			return repo{t, dir, global}.commit("attempt", map[string]string{name: name + "\n"})
		},
		Contains: func(t *testing.T, repository, head string) bool {
			return exec.Command("git", "-C", repository, "cat-file", "-e", head+"^{commit}").Run() == nil
		},
	}
}

// TestEnsureProgram pins the behaviour only real git shows: recovery from an interrupted creation, a branch kept
// across a removed worktree, other repositories' directories and pushing a worktree's commit from the repository.
func TestEnsureProgram(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
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
				w, base := arrangeWorkspace(t)
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
		w, _ := arrangeWorkspace(t)
		ensure(t, w)
		commit := repo{t, w.Dir, global}.commit("attempt", map[string]string{"c.txt": "c\n"})
		repo{t, w.Repository, global}.run("worktree", "remove", w.Dir)
		ensure(t, w)
		if got := head(t, w.Dir); got != commit {
			t.Errorf("worktree created again at %s, want the branch's %s", got, commit)
		}
	})
	t.Run("an agent may move the worktree's branch", func(t *testing.T) {
		w, _ := arrangeWorkspace(t)
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
		w, _ := arrangeWorkspace(t)
		other, _ := arrangeWorkspace(t)
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
		w, _ := arrangeWorkspace(t)
		w.Dir = filepath.Join(w.Repository, "sub")
		writeFile(t, filepath.Join(w.Dir, "f"), "f")
		if err := (git.Program{}).Ensure(ctx, w); !errors.Is(err, git.ErrNotWorkspace) {
			t.Errorf("Ensure over a subdirectory of the repository = %v, want ErrNotWorkspace", err)
		}
	})
	t.Run("an unreachable remote creates nothing", func(t *testing.T) {
		w, _ := arrangeWorkspace(t)
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
		w, _ := arrangeWorkspace(t)
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
	merged := func(t *testing.T) (git.Retirement, repo) {
		t.Helper()
		w, _ := arrangeWorkspace(t)
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
