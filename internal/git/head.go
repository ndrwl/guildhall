package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// FinalHead returns the full commit object id at HEAD in dir, without writing
// to the repository. Staged or unstaged tracked changes (including submodules)
// fail with an error naming the dirty paths; untracked files alone are ignored.
// An unborn HEAD, missing repository, git failure or cancelled context is an
// error. Callers must hold exclusive custody of the workspace while taking it.
func (p Program) FinalHead(ctx context.Context, dir string) (string, error) {
	changes, err := p.TrackedChanges(ctx, dir)
	if err != nil {
		return "", err
	}
	if len(changes) != 0 {
		return "", fmt.Errorf("cannot record code head: uncommitted tracked changes: %q; commit or discard them before intake", changes)
	}
	return p.Head(ctx, dir)
}

// TrackedChanges returns the porcelain status records of the staged or unstaged tracked changes (including
// submodules) in dir, nil when there are none; untracked files are ignored. It writes nothing to the repository.
func (p Program) TrackedChanges(ctx context.Context, dir string) ([]string, error) {
	status, err := p.read(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=no", "--ignore-submodules=untracked")
	if err != nil {
		return nil, err
	}
	if len(status) == 0 {
		return nil, nil
	}
	// NUL records preserve paths with spaces/newlines and rename source paths.
	return strings.Split(strings.TrimSuffix(string(status), "\x00"), "\x00"), nil
}

// Head returns the full commit object id at HEAD in dir. An unborn HEAD, missing repository, git failure or cancelled
// context is an error.
func (p Program) Head(ctx context.Context, dir string) (string, error) {
	out, err := p.read(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(out))
	if !digest.ValidHead(head) {
		return "", fmt.Errorf("git HEAD is not a full commit object id: %q", head)
	}
	return head, nil
}
