package git_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

// TestVersionReads pins Subject, FetchDefault and Behind over a repository and a local bare remote whose default
// branch trunk is ahead of what the repository fetched (gittest.Repository), with no network.
func TestVersionReads(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repository, remote, _ := gittest.Repository(t)
	r := repo{t, repository, global}
	start := r.run("rev-parse", "HEAD")
	const ref = "refs/guildhall/version/main"
	p := git.Program{}

	if got, err := p.Subject(ctx, repository, start); err != nil || got != "a.txt" {
		t.Errorf("Subject(start) = %q, %v; want a.txt", got, err)
	}
	if _, err := p.Subject(ctx, repository, strings.Repeat("e", 40)); err == nil {
		t.Error("Subject of a commit the repository lacks succeeded")
	}
	if _, err := p.Subject(ctx, repository, "HEAD"); err == nil {
		t.Error("Subject of a name, not a commit id, succeeded")
	}
	// A pseudo-version's 12-digit abbreviation resolves; a shorter one is refused.
	if got, err := p.Subject(ctx, repository, start[:12]); err != nil || got != "a.txt" {
		t.Errorf("Subject(start[:12]) = %q, %v; want a.txt", got, err)
	}
	if _, err := p.Subject(ctx, repository, start[:7]); err == nil {
		t.Error("Subject of a 7-digit abbreviation succeeded")
	}
	if _, _, err := p.Behind(ctx, repository, start, ref); err == nil {
		t.Error("Behind with no fetched ref succeeded")
	}

	// Two more merges reach the remote: the fetched ref names them, newest first.
	clone := repo{t, t.TempDir(), global}
	clone.run("clone", "-q", remote, ".")
	clone.commit("Fix the login redirect (#159)", map[string]string{"c.txt": "c\n"})
	clone.commit("Direct push without a pull request", map[string]string{"d.txt": "d\n"})
	clone.run("push", "-q", "origin", "HEAD:trunk")
	if err := p.FetchDefault(ctx, repository, remote, ref); err != nil {
		t.Fatalf("FetchDefault = %v", err)
	}
	count, subjects, err := p.Behind(ctx, repository, start, ref)
	if want := []string{"Direct push without a pull request", "Fix the login redirect (#159)", "b.txt"}; err != nil || count != 3 || !slices.Equal(subjects, want) {
		t.Errorf("Behind = %d, %q, %v; want 3, %q", count, subjects, err, want)
	}
	if count, _, err := p.Behind(ctx, repository, start[:12], ref); err != nil || count != 3 {
		t.Errorf("Behind(start[:12]) = %d, %v; want 3", count, err)
	}
	tip := r.run("rev-parse", ref)
	if count, subjects, err := p.Behind(ctx, repository, tip, ref); err != nil || count != 0 || len(subjects) != 0 {
		t.Errorf("Behind(tip) = %d, %q, %v; want none", count, subjects, err)
	}

	// An unreachable remote is an error and leaves the ref where the last fetch put it.
	if err := p.FetchDefault(ctx, repository, filepath.Join(t.TempDir(), "gone.git"), ref); err == nil {
		t.Error("FetchDefault from a missing remote succeeded")
	}
	if got := r.run("rev-parse", ref); got != tip {
		t.Errorf("failed fetch moved the ref to %s, want %s", got, tip)
	}
	for _, bad := range []struct{ remote, ref string }{{remote, "refs/heads/main"}, {"--upload-pack=x", ref}, {"", ref}} {
		if err := p.FetchDefault(ctx, repository, bad.remote, bad.ref); err == nil {
			t.Errorf("FetchDefault(%q, %q) succeeded", bad.remote, bad.ref)
		}
	}
}

// TestFirstParents pins FirstParents: the head and its first-parent ancestors, newest first, bounded by n, never a
// merged side branch's commits; a name, a short id and a commit the repository lacks are refused.
func TestFirstParents(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repository, _, _ := gittest.Repository(t)
	r := repo{t, repository, global}
	p := git.Program{}
	start := r.run("rev-parse", "HEAD")
	r.run("switch", "-q", "-c", "side")
	side := r.commit("side", map[string]string{"side.txt": "s\n"})
	r.run("switch", "-q", "-")
	main := r.commit("main", map[string]string{"main.txt": "m\n"})
	r.run("merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := r.run("rev-parse", "HEAD")

	got, err := p.FirstParents(ctx, repository, merge, 2)
	if want := []string{merge, main, start}; err != nil || !slices.Equal(got, want) {
		t.Errorf("FirstParents(merge, 2) = %q, %v; want %q", got, err, want)
	}
	if slices.Contains(got, side) {
		t.Error("FirstParents lists the merged side branch")
	}
	if got, err := p.FirstParents(ctx, repository, merge, 0); err != nil || !slices.Equal(got, []string{merge}) {
		t.Errorf("FirstParents(merge, 0) = %q, %v; want the head alone", got, err)
	}
	for _, bad := range []string{"HEAD", merge[:12], strings.Repeat("e", 40)} {
		if _, err := p.FirstParents(ctx, repository, bad, 3); err == nil {
			t.Errorf("FirstParents(%q) succeeded", bad)
		}
	}
}
