package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

var (
	// ErrNoRevision reports a revision the repository does not have, such as a ref not fetched yet.
	ErrNoRevision = errors.New("no such revision")
	// ErrNotRegularFile reports a path Show found that is not a regular file, such as a symbolic link or submodule.
	ErrNotRegularFile = errors.New("not a regular file")
)

// Shown is one file as Show read it.
type Shown struct {
	// Commit is the full commit id rev was at.
	Commit string
	// Data is the file's exact bytes; nil when Absent.
	Data []byte
	// Absent is set when the commit has no file at the path.
	Absent bool
}

// Show reads the file at path, slash-separated and relative to the repository root, in commit rev of the
// repository at repo, reading nothing from a work tree. A rev the repository does not have is ErrNoRevision; a
// commit with no entry at path is Absent, not an error; a path that is not a regular file, such as a directory or
// a symbolic link, is ErrNotRegularFile, and any git failure is an error.
func (p Program) Show(ctx context.Context, repo, rev, path string) (Shown, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return Shown{}, fmt.Errorf("revision %q is not a revision", rev)
	}
	out, err := p.git(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return Shown{}, fmt.Errorf("%w: %s", ErrNoRevision, rev)
	} else if err != nil {
		return Shown{}, err
	}
	commit := strings.TrimSpace(string(out))
	out, err = p.read(ctx, repo, "ls-tree", "-z", "--full-tree", commit, "--", path)
	if err != nil {
		return Shown{}, err
	}
	if len(out) == 0 {
		return Shown{Commit: commit, Absent: true}, nil
	}
	// One "<mode> <type> <object>\t<path>" entry; a path inside a missing directory lists nothing.
	meta, name, ok := strings.Cut(strings.TrimSuffix(string(out), "\x00"), "\t")
	f := strings.Fields(meta)
	if !ok || name != path || len(f) != 3 {
		return Shown{}, fmt.Errorf("git ls-tree %s -- %s: unexpected entry %q", commit, path, out)
	}
	if f[0] != "100644" && f[0] != "100755" {
		return Shown{}, fmt.Errorf("%w: %s at %s has mode %s", ErrNotRegularFile, path, commit, f[0])
	}
	data, err := p.git(ctx, repo, "cat-file", "blob", f[2])
	if err != nil {
		return Shown{}, err
	}
	return Shown{Commit: commit, Data: data}, nil
}

// Changes reports whether the change from commit base to commit head, in the repository at repo, adds, removes
// or changes the file at path. Any git failure is an error.
func (p Program) Changes(ctx context.Context, repo, base, head, path string) (bool, error) {
	for _, id := range []string{base, head} {
		if id == "" || strings.HasPrefix(id, "-") {
			return false, fmt.Errorf("commit %q is not a commit", id)
		}
	}
	// The pathspec is literal, so anything listed is the path itself or, were it a directory, inside it.
	changed, err := p.files(ctx, repo, base, head, path)
	if err != nil {
		return false, err
	}
	return len(changed) > 0, nil
}

// ChangedFiles lists, sorted, the slash-separated paths of the files the change base...head adds, removes or
// changes in the repository at repo: the change head makes since its merge-base with base, as a pull request's diff
// shows it, with renames listed as a removal and an addition. Any git failure is an error.
func (p Program) ChangedFiles(ctx context.Context, repo, base, head string) ([]string, error) {
	for _, id := range []string{base, head} {
		if id == "" || strings.HasPrefix(id, "-") {
			return nil, fmt.Errorf("commit %q is not a commit", id)
		}
	}
	out, err := p.read(ctx, repo, "diff", "--ignore-submodules=none", "--name-only", "-z", "--no-renames", "--no-ext-diff", base+"..."+head, "--")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []string{}, nil
	}
	names := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	slices.Sort(names)
	return names, nil
}
