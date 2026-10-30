package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

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

// Ensure makes w.Dir a worktree of w.Repository, creating it when it does not exist and reusing it when it does.
//
// A new worktree is on branch w.Branch: an existing local branch of that name is checked out as it is, so a
// worktree removed and created again keeps its commits; otherwise the branch is created at the commit the
// remote's default branch (its HEAD) is at now, fetched from w.Remote. A missing or empty w.Dir is created; a
// worktree of w.Repository is reused unchanged, whatever branch or commit it is at and whatever it holds, so
// every attempt of a task works in the same tree. Ensure never resets, cleans or checks out over a reused tree.
//
// Every Ensure also records the commit the remote's default branch is at now as DefaultRef(w.Branch), which Base
// reads: a new branch's fetch records it, and otherwise it is fetched on its own, where a failure is not an error
// and leaves the ref as it was, so a reused worktree needs no remote.
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
		if _, err := p.git(ctx, w.Repository, "worktree", "repair", w.Dir); err != nil {
			return err
		}
		return p.fetchDefault(ctx, w)
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
	}
	return p.validBranch(ctx, w.Branch)
}

// validBranch refuses a branch name that is empty, starts with "-" or that git would not accept.
func (p Program) validBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%w: branch %q", ErrInvalidWorkspace, branch)
	}
	if _, err := p.git(ctx, "", "check-ref-format", "refs/heads/"+branch); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: branch %q is not a valid branch name", ErrInvalidWorkspace, branch)
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

// create builds the worktree at w.Dir+".partial", clearing any an interrupted Ensure left there and any stale
// registration of the missing w.Dir, and moves it into place. The branch is created from the remote's HEAD only when the repository has no branch of its name.
func (p Program) create(ctx context.Context, w Workspace) error {
	partial := w.Dir + ".partial"
	// Nothing ever ran in a partial worktree, so it is removed whatever it holds, registered or not.
	_, _ = p.git(ctx, w.Repository, "worktree", "remove", "--force", "--force", partial)
	// w.Dir is missing here, so a registration git still holds for it is stale and only blocks the branch.
	_, _ = p.git(ctx, w.Repository, "worktree", "remove", "--force", "--force", w.Dir)
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
		if _, err := p.git(ctx, w.Repository, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", w.Remote, "HEAD:"+ref, "+HEAD:"+DefaultRef(w.Branch)); err != nil {
			return err
		}
	} else if err := p.fetchDefault(ctx, w); err != nil {
		return err
	}
	if _, err := p.git(ctx, w.Repository, "worktree", "add", "--quiet", partial, w.Branch); err != nil {
		return err
	}
	_, err := p.git(ctx, w.Repository, "worktree", "move", partial, w.Dir)
	return err
}

// DefaultRef is the ref a workspace on branch records the remote's default branch at, refs/guildhall/default/<branch>.
func DefaultRef(branch string) string { return "refs/guildhall/default/" + branch }

// fetchDefault records the commit the remote's default branch is at as DefaultRef(w.Branch). Only ctx ending is
// an error: an unreachable remote leaves the ref as it was.
func (p Program) fetchDefault(ctx context.Context, w Workspace) error {
	_, _ = p.git(ctx, w.Repository, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", w.Remote, "+HEAD:"+DefaultRef(w.Branch))
	return ctx.Err()
}

// FastForward moves an untouched task branch to the commit the remote's default branch is at now, so a task that
// waited for its dependencies starts from the main they merged into (docs/contracts/git.md#fastforward).
//
// A w.Dir that is not a worktree of w.Repository is left alone: a later Ensure creates the branch at the current
// main anyway. Otherwise it fetches the remote's default branch and records it as DefaultRef(w.Branch), as Ensure
// does, and when the worktree is on w.Branch and its HEAD is an ancestor of that commit it runs git merge --ff-only
// in the worktree. That only fast-forwards, never rebases, and keeps untracked files, such as a Director stage's
// outputs: git refuses rather than overwrite one.
//
// When the branch cannot be moved, nothing changes and kept says why: the remote could not be fetched, the worktree
// is on another branch, the branch has commits that are not on the default branch, or git refused the fast-forward,
// for example because an untracked file would be overwritten. That is not an error.
//
// FastForward is idempotent and replay safe: a branch already at or ahead of the default branch is left as it is.
// It refuses a workspace that fails validation (ErrInvalidWorkspace); another git failure is an error naming git's
// reason, and ctx ending is an error.
func (p Program) FastForward(ctx context.Context, w Workspace) (kept string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := p.validate(ctx, w); err != nil {
		return "", err
	}
	if ok, err := p.isWorktree(ctx, w); err != nil || !ok {
		return "", err
	}
	if _, err := p.git(ctx, w.Repository, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", w.Remote, "+HEAD:"+DefaultRef(w.Branch)); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "the remote's default branch could not be fetched: " + err.Error(), nil
	}
	out, err := p.git(ctx, w.Repository, "rev-parse", "--verify", DefaultRef(w.Branch)+"^{commit}")
	if err != nil {
		return "", err
	}
	main := strings.TrimSpace(string(out))
	if out, err := p.git(ctx, w.Dir, "symbolic-ref", "--quiet", "HEAD"); err != nil || strings.TrimSpace(string(out)) != "refs/heads/"+w.Branch {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return fmt.Sprintf("the worktree is not on branch %s", w.Branch), nil
	}
	out, err = p.git(ctx, w.Dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(out))
	if ahead, err := p.merged(ctx, w.Repository, main, head); err != nil || ahead {
		return "", err
	}
	if behind, err := p.merged(ctx, w.Repository, head, main); err != nil {
		return "", err
	} else if !behind {
		return fmt.Sprintf("branch %s is at %s, which has commits that are not on the default branch's %s", w.Branch, head, main), nil
	}
	if _, err := p.git(ctx, w.Dir, "merge", "--ff-only", "--quiet", main); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return fmt.Sprintf("git refused to fast-forward branch %s from %s to %s: %v", w.Branch, head, main, err), nil
	}
	return "", nil
}

// Base returns the base of the task on branch in the repository at repository: the merge base of the branch and
// the remote's default branch as Ensure last recorded it, read without the remote. A branch that does not exist,
// a workspace never ensured or retired, and an invalid branch name are errors.
func (p Program) Base(ctx context.Context, repository, branch string) (string, error) {
	if err := p.validBranch(ctx, branch); err != nil {
		return "", err
	}
	out, err := p.git(ctx, repository, "merge-base", "refs/heads/"+branch, DefaultRef(branch))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Tip returns the commit refs/heads/<branch> is at in the repository at repository. A branch that does not exist,
// an invalid branch name and any git failure are errors.
func (p Program) Tip(ctx context.Context, repository, branch string) (string, error) {
	if err := p.validBranch(ctx, branch); err != nil {
		return "", err
	}
	out, err := p.git(ctx, repository, "show-ref", "--verify", "--hash", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether commit is head or one of its ancestors in the repository at repository. A commit or
// head the repository does not have is false, not an error; any other git failure is an error.
func (p Program) IsAncestor(ctx context.Context, repository, commit, head string) (bool, error) {
	for _, id := range []string{commit, head} {
		if strings.HasPrefix(id, "-") {
			return false, nil
		}
		_, err := p.git(ctx, repository, "rev-parse", "--verify", "--quiet", id+"^{commit}")
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return p.merged(ctx, repository, commit, head)
}

// Retirement is a task workspace whose change was merged, to be removed.
type Retirement struct {
	Workspace
	// Head is the full commit id the task's change was merged at: work at it or behind it was delivered.
	Head string
	// Outputs are the files Guildhall recorded from the working directory, by clean slash-separated path relative
	// to it, with the sha256 in lowercase hex of every copy recorded at that path: an untracked file that is a copy
	// of any of them is not work.
	Outputs map[string][]string
}

// Retire removes a merged task's worktree, w.Dir, and its local branch, w.Branch, when nothing in them would be
// lost, and otherwise removes nothing and returns why it kept them.
//
// Something would be lost when a tracked file is changed or staged; when an untracked file has none of the
// bytes r.Outputs records at its path; when the worktree's HEAD is not r.Head or one of its ancestors; or when the
// branch exists and its tip is not. Files git ignores are not work and are removed with the worktree; other local
// branches and stashes are the repository's and are kept. The branch is deleted only while still at the tip
// that was checked.
//
// Retire is idempotent and replay safe: a missing w.Dir with no branch is already retired, whatever r.Head is, and a branch left by an interrupted
// Retire is deleted when its tip is merged. It refuses an invalid workspace or head (ErrInvalidWorkspace) and a
// w.Dir that exists and is not a worktree of w.Repository (ErrNotWorkspace), removing nothing. A head the
// repository does not have, as when the forge made it, is fetched from w.Remote by its commit id once, writing no
// ref; a git failure, including a head that fetch cannot get, is an error, and ctx ending is an error.
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
		_, err := p.git(ctx, r.Repository, "update-ref", "-d", DefaultRef(r.Branch))
		return nil, err
	}
	if err := p.fetchHead(ctx, r); err != nil {
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
	_, err = p.git(ctx, r.Repository, "update-ref", "-d", DefaultRef(r.Branch))
	return nil, err
}

// fetchHead ensures the repository has the commit r.Head, fetching it from r.Remote by its id when it is missing,
// as after a merge outside Guildhall of a head the forge made. A head already there makes no network call.
func (p Program) fetchHead(ctx context.Context, r Retirement) error {
	_, err := p.git(ctx, r.Repository, "cat-file", "-e", r.Head+"^{commit}")
	if err == nil || ctx.Err() != nil {
		return err
	}
	if _, ferr := p.git(ctx, r.Repository, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", r.Remote, r.Head); ferr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the merged head %s is not in the repository and fetching it failed: %w", r.Head, ferr)
	}
	_, err = p.git(ctx, r.Repository, "cat-file", "-e", r.Head+"^{commit}")
	return err
}

// unmerged lists what in the worktree r.Dir would be lost: a HEAD that is not r.Head or behind it, a changed or
// staged tracked file, and an untracked file with none of the bytes r.Outputs records at its path.
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
		want := r.Outputs[name]
		data, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(name)))
		if len(want) == 0 || err != nil || !slices.Contains(want, digest.Of(data).Hex()) {
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
