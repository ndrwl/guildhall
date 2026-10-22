package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

// hangingGitEnv makes this test binary a git that never answers in time.
const hangingGitEnv = "GUILDHALL_TEST_HANGING_GIT"

func TestMain(m *testing.M) {
	if os.Getenv(hangingGitEnv) != "" {
		time.Sleep(time.Minute)
		fmt.Fprintln(os.Stderr, "hanging git: not ended at its timeout")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestClassifyTimeout: a git run of the classifier that outlasts Program.Timeout is ended, and the review is full
// with a reason naming git and the timeout; Classify itself does not fail, since its context did not end.
func TestClassifyTimeout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(hangingGitEnv, "1")
	p := git.Program{Path: exe, Timeout: 200 * time.Millisecond}
	last := git.Range{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	next := git.Range{Base: strings.Repeat("c", 40), Head: strings.Repeat("d", 40)}
	got, err := p.Classify(context.Background(), t.TempDir(), last, next)
	if err != nil {
		t.Fatalf("Classify = %v, want a full classification", err)
	}
	if got.Scope != git.ScopeFull || got.Diff != nil || !strings.Contains(got.Reason, "merge-base") || !strings.Contains(got.Reason, "200ms") {
		t.Errorf("Classify past its timeout = %q (%s), want full with a reason naming git merge-base and 200ms", got.Scope, got.Reason)
	}
}

func TestClassifierContract(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		arrange := newArrange(t)
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
				if c == gittest.RebaseOnly || c == gittest.SubmoduleRebase {
					s.Add("repo", last, next, git.Classification{Scope: git.ScopeRebase, Diff: []byte("Subproject commit scripted\n")})
				}
				return "repo", last, next
			}}
		})
	})
}

// repo runs real git in one directory with an isolated, empty global config.
type repo struct {
	t           *testing.T
	dir, global string
}

func (r repo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+r.global)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r repo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(data), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.run("add", name)
	}
	r.run("commit", "-q", "-m", msg)
	return r.run("rev-parse", "HEAD")
}

// base is a shared history that cases clone instead of rebuilding.
type base struct {
	dir  string
	revs []string
}

// newArrange returns an arrange that builds each base history once per test t and gives every case its own
// shared clone of it.
func newArrange(t *testing.T) func(*testing.T, gittest.Case) (string, git.Range, git.Range) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bases := map[bool]base{}
	clone := func(ct *testing.T, submodule bool) (repo, []string) {
		b, ok := bases[submodule]
		if !ok {
			r := repo{t, t.TempDir(), global}
			r.run("init", "-q")
			if submodule {
				// The task changes the gitlink module on base B0 and an ordinary file, and is reviewed at H0.
				first := r.commit("submodule first", map[string]string{"seed": "first\n"})
				second := r.commit("submodule second", map[string]string{"seed": "second\n"})
				third := r.commit("submodule third", map[string]string{"seed": "third\n"})
				r.run("update-index", "--add", "--cacheinfo", "160000,"+first+",module")
				b0 := r.commit("base", map[string]string{".gitmodules": "[submodule \"module\"]\n\tpath = module\n\turl = ./module\n\tignore = all\n", "ordinary": "base\n"})
				r.run("update-index", "--cacheinfo", "160000,"+second+",module")
				h0 := r.commit("task", map[string]string{"ordinary": "task\n"})
				b = base{r.dir, []string{second, third, b0, h0}}
			} else {
				// The task changes the first line of t.txt and adds n.txt on base B0 in two commits, and is reviewed at H0.
				b0 := r.commit("base", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "t.txt": "t\n1\n2\n3\n4\n5\n6\n7\n8\n"})
				r.commit("task edits t", map[string]string{"t.txt": "task\n1\n2\n3\n4\n5\n6\n7\n8\n"})
				h0 := r.commit("task adds n", map[string]string{"n.txt": "n\n"})
				b = base{r.dir, []string{b0, h0}}
			}
			bases[submodule] = b
		}
		r := repo{ct, ct.TempDir(), global}
		r.run("clone", "-q", "--shared", b.dir, ".")
		return r, b.revs
	}
	return func(t *testing.T, c gittest.Case) (string, git.Range, git.Range) {
		return arrange(t, c, clone)
	}
}

// arrange derives history c from a clone of its base with real git.
func arrange(t *testing.T, c gittest.Case, clone func(*testing.T, bool) (repo, []string)) (string, git.Range, git.Range) {
	if c == gittest.SubmoduleRebase || c == gittest.BaseTouchesSubmodule {
		r, revs := clone(t, true)
		second, third, b0, h0 := revs[0], revs[1], revs[2], revs[3]
		r.run("checkout", "-q", "--detach", b0)
		if c == gittest.BaseTouchesSubmodule {
			r.run("update-index", "--cacheinfo", "160000,"+third+",module")
		}
		b1 := r.commit("base moves", map[string]string{"unrelated": "moved\n"})
		r.run("update-index", "--cacheinfo", "160000,"+second+",module")
		h1 := r.commit("task", map[string]string{"ordinary": "task\n"})
		r.run("config", "diff.ignoreSubmodules", "all")
		r.run("config", "submodule.module.ignore", "all")
		return r.dir, git.Range{Base: b0, Head: h0}, git.Range{Base: b1, Head: h1}
	}
	r, revs := clone(t, false)
	b0, h0 := revs[0], revs[1]
	last := git.Range{Base: b0, Head: h0}
	r.run("checkout", "-q", "--detach", b0)
	switch c {
	case gittest.SameBase:
		r.run("checkout", "-q", "--detach", h0)
		return r.dir, last, git.Range{Base: b0, Head: r.commit("more", map[string]string{"a.txt": "a2\n"})}
	case gittest.BaseTouchesTaskFile:
		// Far from the task's hunk, so the task's patch is unchanged.
		r.commit("base moves", map[string]string{"t.txt": "t\n1\n2\n3\n4\n5\n6\n7\neight\n"})
	default:
		r.commit("base moves", map[string]string{"b.txt": "b2\n"})
	}
	b1 := r.run("rev-parse", "HEAD")
	r.run("cherry-pick", b0+".."+h0)
	next := git.Range{Base: b1, Head: r.run("rev-parse", "HEAD")}
	switch c {
	case gittest.RebaseAndEdit:
		if err := os.WriteFile(filepath.Join(r.dir, "t.txt"), []byte("task\n1\n2\n3\n4\n5\n6\n7\n8\nmore\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.run("commit", "-q", "-a", "--amend", "--no-edit")
		next.Head = r.run("rev-parse", "HEAD")
	case gittest.UnknownCommit:
		next.Head = strings.Repeat("0", 40)
	}
	return r.dir, last, next
}
