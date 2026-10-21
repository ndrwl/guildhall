package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
)

func TestClassifierContract(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		RunClassifierContract(t, func(t *testing.T) Harness {
			return Harness{Classify: git.Program{}.Classify, Arrange: arrange}
		})
	})
	t.Run("script", func(t *testing.T) {
		RunClassifierContract(t, func(t *testing.T) Harness {
			s := &Script{}
			return Harness{Classify: s.Classify, Arrange: func(t *testing.T, c Case) (string, git.Range, git.Range) {
				last := git.Range{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
				next := git.Range{Base: strings.Repeat("c", 40), Head: strings.Repeat(string(c[0]), 40)}
				if c == RebaseOnly || c == SubmoduleRebase {
					s.Add("repo", last, next, git.Classification{Scope: git.ScopeRebase, Diff: []byte("Subproject commit scripted\n")})
				}
				return "repo", last, next
			}}
		})
	})
}

// arrange builds history c in a new repository with real git: the task changes the first line of t.txt and
// adds n.txt on base B0 in two commits, and is reviewed at H0.
func arrange(t *testing.T, c Case) (string, git.Range, git.Range) {
	dir, global := t.TempDir(), filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+global)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string, files map[string]string) string {
		t.Helper()
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			run("add", name)
		}
		run("commit", "-q", "-m", msg)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q")
	if c == SubmoduleRebase || c == BaseTouchesSubmodule {
		first := commit("submodule first", map[string]string{"seed": "first\n"})
		second := commit("submodule second", map[string]string{"seed": "second\n"})
		third := commit("submodule third", map[string]string{"seed": "third\n"})
		run("update-index", "--add", "--cacheinfo", "160000,"+first+",module")
		b0 := commit("base", map[string]string{".gitmodules": "[submodule \"module\"]\n\tpath = module\n\turl = ./module\n\tignore = all\n", "ordinary": "base\n"})
		run("update-index", "--cacheinfo", "160000,"+second+",module")
		h0 := commit("task", map[string]string{"ordinary": "task\n"})
		run("checkout", "-q", "--detach", b0)
		if c == BaseTouchesSubmodule {
			run("update-index", "--cacheinfo", "160000,"+third+",module")
		}
		b1 := commit("base moves", map[string]string{"unrelated": "moved\n"})
		run("update-index", "--cacheinfo", "160000,"+second+",module")
		h1 := commit("task", map[string]string{"ordinary": "task\n"})
		run("config", "diff.ignoreSubmodules", "all")
		run("config", "submodule.module.ignore", "all")
		return dir, git.Range{Base: b0, Head: h0}, git.Range{Base: b1, Head: h1}
	}
	b0 := commit("base", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "t.txt": "t\n1\n2\n3\n4\n5\n6\n7\n8\n"})
	commit("task edits t", map[string]string{"t.txt": "task\n1\n2\n3\n4\n5\n6\n7\n8\n"})
	h0 := commit("task adds n", map[string]string{"n.txt": "n\n"})
	last := git.Range{Base: b0, Head: h0}
	run("checkout", "-q", "--detach", b0)
	switch c {
	case SameBase:
		run("checkout", "-q", "--detach", h0)
		return dir, last, git.Range{Base: b0, Head: commit("more", map[string]string{"a.txt": "a2\n"})}
	case BaseTouchesTaskFile:
		// Far from the task's hunk, so the task's patch is unchanged.
		commit("base moves", map[string]string{"t.txt": "t\n1\n2\n3\n4\n5\n6\n7\neight\n"})
	default:
		commit("base moves", map[string]string{"b.txt": "b2\n"})
	}
	b1 := run("rev-parse", "HEAD")
	run("cherry-pick", b0+".."+h0)
	next := git.Range{Base: b1, Head: run("rev-parse", "HEAD")}
	switch c {
	case RebaseAndEdit:
		if err := os.WriteFile(filepath.Join(dir, "t.txt"), []byte("task\n1\n2\n3\n4\n5\n6\n7\n8\nmore\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("commit", "-q", "-a", "--amend", "--no-edit")
		next.Head = run("rev-parse", "HEAD")
	case UnknownCommit:
		next.Head = strings.Repeat("0", 40)
	}
	return dir, last, next
}
