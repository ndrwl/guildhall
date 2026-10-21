package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

func TestClassifierContract(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		gittest.RunClassifierContract(t, func(t *testing.T) gittest.Harness {
			return gittest.Harness{Classify: git.Program{}.Classify, Arrange: arrange}
		})
	})
	t.Run("script", func(t *testing.T) {
		gittest.RunClassifierContract(t, func(t *testing.T) gittest.Harness {
			s := &gittest.Script{}
			return gittest.Harness{Classify: s.Classify, Arrange: func(t *testing.T, c gittest.Case) (string, git.Range, git.Range) {
				last := git.Range{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
				next := git.Range{Base: strings.Repeat("c", 40), Head: strings.Repeat(string(c[0]), 40)}
				if c == gittest.RebaseOnly {
					s.Add("repo", last, next, git.Classification{Scope: git.ScopeRebase, Diff: []byte("diff\n")})
				}
				return "repo", last, next
			}}
		})
	})
}

// arrange builds history c in a new repository with real git: the task changes the first line of t.txt and
// adds n.txt on base B0 in two commits, and is reviewed at H0.
func arrange(t *testing.T, c gittest.Case) (string, git.Range, git.Range) {
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
	b0 := commit("base", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "t.txt": "t\n1\n2\n3\n4\n5\n6\n7\n8\n"})
	commit("task edits t", map[string]string{"t.txt": "task\n1\n2\n3\n4\n5\n6\n7\n8\n"})
	h0 := commit("task adds n", map[string]string{"n.txt": "n\n"})
	last := git.Range{Base: b0, Head: h0}
	run("checkout", "-q", "--detach", b0)
	switch c {
	case gittest.SameBase:
		run("checkout", "-q", "--detach", h0)
		return dir, last, git.Range{Base: b0, Head: commit("more", map[string]string{"a.txt": "a2\n"})}
	case gittest.BaseTouchesTaskFile:
		// Far from the task's hunk, so the task's patch is unchanged.
		commit("base moves", map[string]string{"t.txt": "t\n1\n2\n3\n4\n5\n6\n7\neight\n"})
	default:
		commit("base moves", map[string]string{"b.txt": "b2\n"})
	}
	b1 := run("rev-parse", "HEAD")
	run("cherry-pick", b0+".."+h0)
	next := git.Range{Base: b1, Head: run("rev-parse", "HEAD")}
	switch c {
	case gittest.RebaseAndEdit:
		if err := os.WriteFile(filepath.Join(dir, "t.txt"), []byte("task\n1\n2\n3\n4\n5\n6\n7\n8\nmore\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("commit", "-q", "-a", "--amend", "--no-edit")
		next.Head = run("rev-parse", "HEAD")
	case gittest.UnknownCommit:
		next.Head = strings.Repeat("0", 40)
	}
	return dir, last, next
}
