package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
