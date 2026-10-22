package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Workspace is one task's working directory: a git worktree of the task's repository, so that a commit an
// Artisan makes there is a commit of the repository, which publish pushes from (docs/contracts/git.md).
type Workspace struct {
	// Repository is the absolute path of the repository on the owner machine, its configured path.
	Repository string
	// Remote is the repository's configured remote, a URL or remote name; a new worktree starts at its
	// default branch.
	Remote string
	// Branch is the task branch, such as guildhall/<task>, that a new worktree is created on.
	Branch string
	// Dir is the absolute path of the working directory.
	Dir string
}

var (
	// ErrInvalidWorkspace reports a workspace missing a field, with a relative path or naming an invalid branch.
	ErrInvalidWorkspace = errors.New("invalid workspace")
	// ErrNotWorkspace reports a working directory that exists, is not empty and is not a worktree of the
	// repository, such as a plain directory an earlier Guildhall made or a checkout of another repository.
	// Nothing in it was changed.
	ErrNotWorkspace = errors.New("working directory is not a worktree of the repository")
)

// DefaultTimeout is how long one git run of a workspace operation may take when Program.Timeout is zero.
const DefaultTimeout = 5 * time.Minute

// Ensure makes w.Dir a worktree of w.Repository, creating it when it does not exist and reusing it when it does.
//
// A new worktree is on branch w.Branch: an existing local branch of that name is checked out as it is, so a
// worktree removed and created again keeps its commits; otherwise the branch is created at the commit the
// remote's default branch (its HEAD) is at now, fetched from w.Remote. A missing or empty w.Dir is created; a
// worktree of w.Repository is reused unchanged, whatever branch or commit it is at and whatever it holds, so
// every attempt of a task works in the same tree. Ensure never resets, cleans or checks out over a reused tree.
//
// Ensure is idempotent and replay safe: the worktree is built beside w.Dir and moved into place only once it is
// complete, so an Ensure interrupted at any point leaves either no w.Dir or a complete one, and repeating it
// finishes the work. It refuses a workspace that fails validation (ErrInvalidWorkspace) and a w.Dir that exists,
// is not empty and is not a worktree of w.Repository (ErrNotWorkspace), changing nothing; a git failure, such as
// an unreachable remote, is an error naming git's reason, and ctx ending is an error. Concurrent calls for one
// workspace are not safe; calls for different workspaces of one repository are.
func (p Program) Ensure(ctx context.Context, w Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.validate(ctx, w); err != nil {
		return err
	}
	switch ok, err := p.isWorktree(ctx, w); {
	case err != nil:
		return err
	case ok:
		// A move interrupted between renaming the directory and updating git's records is mended here.
		_, err := p.git(ctx, w.Repository, "worktree", "repair", w.Dir)
		return err
	}
	switch entries, err := os.ReadDir(w.Dir); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%w: %s is not a worktree of %s", ErrNotWorkspace, w.Dir, w.Repository)
	default:
		if err := os.Remove(w.Dir); err != nil {
			return err
		}
	}
	return p.create(ctx, w)
}

// validate refuses a workspace missing a field, with a relative path or naming a branch git would not accept.
func (p Program) validate(ctx context.Context, w Workspace) error {
	switch {
	case !filepath.IsAbs(w.Repository) || !filepath.IsAbs(w.Dir):
		return fmt.Errorf("%w: repository %q and directory %q must be absolute", ErrInvalidWorkspace, w.Repository, w.Dir)
	case w.Remote == "":
		return fmt.Errorf("%w: no remote", ErrInvalidWorkspace)
	case w.Branch == "" || strings.HasPrefix(w.Branch, "-"):
		return fmt.Errorf("%w: branch %q", ErrInvalidWorkspace, w.Branch)
	}
	if _, err := p.git(ctx, "", "check-ref-format", "refs/heads/"+w.Branch); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: branch %q is not a valid branch name", ErrInvalidWorkspace, w.Branch)
	}
	return nil
}

// isWorktree reports whether w.Dir is the top of a worktree of w.Repository, comparing directories by identity
// so that symbolic links, case and short names on Windows do not matter.
func (p Program) isWorktree(ctx context.Context, w Workspace) (bool, error) {
	if fi, err := os.Lstat(filepath.Join(w.Dir, ".git")); err != nil || !fi.Mode().IsRegular() {
		return false, nil
	}
	want, err := p.git(ctx, w.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, err
	}
	got, err := p.git(ctx, w.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir", "--show-toplevel")
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, nil
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	return len(lines) == 2 && same(lines[0], strings.TrimSpace(string(want))) && same(lines[1], w.Dir), nil
}

// same reports whether two paths name the same existing directory.
func same(a, b string) bool {
	fa, err := os.Stat(filepath.FromSlash(a))
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// create builds the worktree at w.Dir+".partial", clearing any an interrupted Ensure left there, and moves it
// into place. The branch is created from the remote's HEAD only when the repository has no branch of its name.
func (p Program) create(ctx context.Context, w Workspace) error {
	partial := w.Dir + ".partial"
	// Nothing ever ran in a partial worktree, so it is removed whatever it holds, registered or not.
	_, _ = p.git(ctx, w.Repository, "worktree", "remove", "--force", "--force", partial)
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
		return err
	}
	ref := "refs/heads/" + w.Branch
	if _, err := p.git(ctx, w.Repository, "show-ref", "--verify", "--quiet", ref); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := p.git(ctx, w.Repository, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", w.Remote, "HEAD:"+ref); err != nil {
			return err
		}
	}
	if _, err := p.git(ctx, w.Repository, "worktree", "add", "--quiet", partial, w.Branch); err != nil {
		return err
	}
	_, err := p.git(ctx, w.Repository, "worktree", "move", partial, w.Dir)
	return err
}

// git runs git in dir, or in the current directory when dir is empty, bounded by the timeout and never prompting,
// and returns its standard output.
func (p Program) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path := p.Path
	if path == "" {
		path = "git"
	}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("git %s: no answer within %v: %w", strings.Join(args, " "), timeout, context.DeadlineExceeded)
	}
	return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
}

// Retirement is a task workspace whose change was merged, to be removed.
type Retirement struct {
	Workspace
	// Head is the full commit id the task's change was merged at: work at it or behind it was delivered.
	Head string
	// Outputs are the files Guildhall recorded from the working directory, by clean slash-separated path relative
	// to it, with their sha256 in lowercase hex: untracked files that are copies of them are not work.
	Outputs map[string]string
}

// Retire removes a merged task's worktree, w.Dir, and its local branch, w.Branch, when nothing in them would be
// lost, and otherwise removes nothing and returns why it kept them.
//
// Something would be lost when a tracked file is changed or staged; when an untracked file is not one of
// r.Outputs with the same bytes; when the worktree's HEAD is not r.Head or one of its ancestors; or when the
// branch exists and its tip is not. Files git ignores are not work and are removed with the worktree; other local
// branches and stashes are the repository's and are kept. The branch is deleted only while still at the tip
// that was checked.
//
// Retire is idempotent and replay safe: a missing w.Dir with no branch is already retired, whatever r.Head is, and a branch left by an interrupted
// Retire is deleted when its tip is merged. It refuses an invalid workspace or head (ErrInvalidWorkspace) and a
// w.Dir that exists and is not a worktree of w.Repository (ErrNotWorkspace), removing nothing; a git failure,
// including a head the repository does not have, is an error, and ctx ending is an error.
func (p Program) Retire(ctx context.Context, r Retirement) (kept []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digest.ValidHead(r.Head) {
		return nil, fmt.Errorf("%w: head %q is not a full commit id", ErrInvalidWorkspace, r.Head)
	}
	if err := p.validate(ctx, r.Workspace); err != nil {
		return nil, err
	}
	ok, err := p.isWorktree(ctx, r.Workspace)
	if err != nil {
		return nil, err
	}
	if !ok {
		if _, err := os.Lstat(r.Dir); err == nil {
			return nil, fmt.Errorf("%w: %s is not a worktree of %s", ErrNotWorkspace, r.Dir, r.Repository)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	ref := "refs/heads/" + r.Branch
	tip := ""
	if out, err := p.git(ctx, r.Repository, "show-ref", "--verify", "--hash", ref); err == nil {
		tip = strings.TrimSpace(string(out))
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !ok && tip == "" {
		return nil, nil
	}
	if _, err := p.git(ctx, r.Repository, "cat-file", "-e", r.Head+"^{commit}"); err != nil {
		return nil, err
	}
	if tip != "" {
		if merged, err := p.merged(ctx, r.Repository, tip, r.Head); err != nil {
			return nil, err
		} else if !merged {
			kept = append(kept, fmt.Sprintf("branch %s is at %s, which is not the merged head %s or behind it", r.Branch, tip, r.Head))
		}
	}
	if ok {
		more, err := p.unmerged(ctx, r)
		if err != nil {
			return nil, err
		}
		kept = append(kept, more...)
	}
	if len(kept) > 0 {
		return kept, nil
	}
	if ok {
		// Untracked outputs and ignored files remain, which only --force removes; unmerged work was ruled out above.
		if _, err := p.git(ctx, r.Repository, "worktree", "remove", "--force", r.Dir); err != nil {
			return nil, err
		}
	}
	if tip != "" {
		// Deleted only while still at the tip that was checked.
		if _, err := p.git(ctx, r.Repository, "update-ref", "-d", ref, tip); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// unmerged lists what in the worktree r.Dir would be lost: a HEAD that is not r.Head or behind it, a changed or
// staged tracked file, and an untracked file that is not one of r.Outputs with the same bytes.
func (p Program) unmerged(ctx context.Context, r Retirement) ([]string, error) {
	var kept []string
	out, err := p.git(ctx, r.Dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	head := strings.TrimSpace(string(out))
	if merged, err := p.merged(ctx, r.Repository, head, r.Head); err != nil {
		return nil, err
	} else if !merged {
		kept = append(kept, fmt.Sprintf("HEAD is at %s, which is not the merged head %s or behind it", head, r.Head))
	}
	status, err := p.git(ctx, r.Dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(status), "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if len(e) < 4 {
			continue
		}
		code, name := e[:2], e[3:]
		if code[0] == 'R' || code[0] == 'C' {
			i++ // the entry's original path follows it
		}
		if code != "??" {
			kept = append(kept, "changed "+name)
			continue
		}
		want, ok := r.Outputs[name]
		data, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(name)))
		if !ok || err != nil || digest.Of(data).Hex() != want {
			kept = append(kept, "untracked "+name)
		}
	}
	return kept, nil
}

// merged reports whether commit is head or one of its ancestors in the repository.
func (p Program) merged(ctx context.Context, repository, commit, head string) (bool, error) {
	_, err := p.git(ctx, repository, "merge-base", "--is-ancestor", commit, head)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}
