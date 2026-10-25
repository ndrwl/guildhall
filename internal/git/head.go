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
	status, err := p.read(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=no", "--ignore-submodules=untracked")
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		// NUL records preserve paths with spaces/newlines and rename source paths.
		fields := strings.Split(strings.TrimSuffix(string(status), "\x00"), "\x00")
		return "", fmt.Errorf("cannot record code head: uncommitted tracked changes: %q; commit or discard them before intake", fields)
	}
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
