package gittest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git/gittest"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRepositoriesCopiesAreIndependent: each copy is a Repository of its own, a clone one commit behind its own
// remote's trunk, and a commit pushed through one copy reaches neither another copy nor its remote.
func TestRepositoriesCopiesAreIndependent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	repositories := gittest.Repositories(t)
	type copied struct{ repository, remote, base string }
	var copies []copied
	for range 2 {
		t.Run("copy", func(ct *testing.T) {
			repository, remote, base := repositories(ct)
			if repository == "" || remote == "" || base == "" {
				ct.Fatalf("Repositories gave %q, %q, %q", repository, remote, base)
			}
			if got := gitOut(ct, remote, "rev-parse", "trunk"); got != base {
				ct.Fatalf("remote trunk is %s, want base %s", got, base)
			}
			if got, want := gitOut(ct, repository, "rev-parse", "HEAD"), gitOut(ct, remote, "rev-parse", "trunk~1"); got != want {
				ct.Fatalf("repository is at %s; want %s, one commit behind trunk", got, want)
			}
			url := gitOut(ct, repository, "remote", "get-url", "origin")
			same, err := sameFile(url, remote)
			if err != nil || !same {
				ct.Fatalf("origin is %s, want this copy's remote %s (%v)", url, remote, err)
			}
			for _, c := range copies {
				if same, _ := sameFile(c.repository, repository); same || c.remote == remote {
					ct.Fatalf("copy shares %s or %s with an earlier copy", repository, remote)
				}
			}
			if exec.Command("git", "-C", remote, "show-ref", "--verify", "--quiet", "refs/heads/pushed").Run() == nil {
				ct.Fatal("an earlier copy's push reached this copy's remote")
			}
			if err := os.WriteFile(filepath.Join(repository, "c.txt"), []byte("c\n"), 0o644); err != nil {
				ct.Fatal(err)
			}
			gitOut(ct, repository, "add", "c.txt")
			gitOut(ct, repository, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "c")
			gitOut(ct, repository, "push", "-q", "origin", "HEAD:refs/heads/pushed")
			copies = append(copies, copied{repository, remote, base})
		})
	}
	if len(copies) != 2 {
		t.Fatalf("made %d copies, want 2", len(copies))
	}
	if copies[0].base != copies[1].base {
		t.Errorf("copies have bases %s and %s; want the same history", copies[0].base, copies[1].base)
	}
}

func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}
