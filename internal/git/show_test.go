package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
)

// TestShowAndChanges pins Show on a present, a missing and an unreadable path, at a fetched ref and at a ref not
// fetched yet, and Changes over a commit that changes the path and one that does not.
func TestShowAndChanges(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repository, remote, _ := gittest.Repository(t)
	r := repo{t, repository, global}
	p := git.Program{}
	const ref = "refs/guildhall/policies"
	const path = ".guildhall/policies.md"

	if _, err := p.Show(ctx, repository, ref, path); !errors.Is(err, git.ErrNoRevision) {
		t.Errorf("Show of a ref not fetched yet = %v, want ErrNoRevision", err)
	}
	start := r.run("rev-parse", "HEAD")
	if got, err := p.Show(ctx, repository, start, path); err != nil || !got.Absent || got.Commit != start || got.Data != nil {
		t.Errorf("Show of a missing path = %+v, %v; want absent at %s", got, err, start)
	}

	if err := os.MkdirAll(filepath.Join(repository, ".guildhall"), 0o755); err != nil {
		t.Fatal(err)
	}
	with := r.commit("Add policies", map[string]string{path: "- Keep it.\n"})
	r.run("push", "-q", "-f", "origin", "HEAD:trunk")
	if err := p.FetchDefault(ctx, repository, remote, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Show(ctx, repository, ref, path); err != nil || got.Absent || got.Commit != with || string(got.Data) != "- Keep it.\n" {
		t.Errorf("Show at the fetched ref = %+v, %v; want the file at %s", got, err, with)
	}
	// The work tree is never read: an uncommitted edit does not change what Show reads.
	if err := os.WriteFile(filepath.Join(repository, path), []byte("- Edited.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Show(ctx, repository, "HEAD", path); err != nil || string(got.Data) != "- Keep it.\n" {
		t.Errorf("Show read the work tree: %+v, %v", got, err)
	}
	if _, err := p.Show(ctx, repository, with, ".guildhall"); err == nil {
		t.Error("Show of a directory succeeded")
	}
	if _, err := p.Show(ctx, repository, "--output=x", path); err == nil {
		t.Error("Show of an option as a revision succeeded")
	}
	if _, err := p.Show(ctx, filepath.Join(t.TempDir(), "none"), ref, path); err == nil || errors.Is(err, git.ErrNoRevision) {
		t.Errorf("Show in a missing repository = %v, want a git failure", err)
	}

	if changed, err := p.Changes(ctx, repository, start, with, path); err != nil || !changed {
		t.Errorf("Changes over the commit adding the file = %v, %v; want true", changed, err)
	}
	other := r.commit("Other", map[string]string{"other.txt": "x\n"})
	if changed, err := p.Changes(ctx, repository, with, other, path); err != nil || changed {
		t.Errorf("Changes over a commit not touching the file = %v, %v; want false", changed, err)
	}
	if _, err := p.Changes(ctx, repository, "-x", other, path); err == nil {
		t.Error("Changes from an option succeeded")
	}
}

// TestChangedFiles: the paths a change adds, changes and deletes since its merge-base with the base, sorted, with
// nothing the base side changed after the merge-base; a symbolic link is listed, and Show refuses it as not a
// regular file.
func TestChangedFiles(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	repository, _, _ := gittest.Repository(t)
	r := repo{t, repository, global}
	p := git.Program{}
	fork := r.commit("Fork", map[string]string{"changed.txt": "before\n", "deleted.txt": "gone\n", "same.txt": "same\n"})
	r.run("switch", "-q", "-c", "change")
	if err := os.MkdirAll(filepath.Join(repository, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.run("rm", "-q", "deleted.txt")
	if err := os.Symlink("same.txt", filepath.Join(repository, "link")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	r.run("add", "link")
	head := r.commit("Change", map[string]string{"changed.txt": "after\n", "sub/added.txt": "new\n"})
	r.run("switch", "-q", "trunk")
	base := r.commit("Base moves on", map[string]string{"same.txt": "moved\n", "base-only.txt": "x\n"})

	got, err := p.ChangedFiles(ctx, repository, base, head)
	if want := []string{"changed.txt", "deleted.txt", "link", "sub/added.txt"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("ChangedFiles = %q, %v; want %q", got, err, want)
	}
	if got, err := p.ChangedFiles(ctx, repository, fork, fork); err != nil || got == nil || len(got) != 0 {
		t.Errorf("ChangedFiles of no change = %#v, %v; want empty", got, err)
	}
	if _, err := p.Show(ctx, repository, head, "link"); !errors.Is(err, git.ErrNotRegularFile) {
		t.Errorf("Show of a symbolic link = %v, want ErrNotRegularFile", err)
	}
	if _, err := p.ChangedFiles(ctx, repository, "--output=x", head); err == nil {
		t.Error("ChangedFiles from an option succeeded")
	}
	if _, err := p.ChangedFiles(ctx, repository, strings.Repeat("0", 40), head); err == nil {
		t.Error("ChangedFiles from a commit the repository lacks succeeded")
	}
}
