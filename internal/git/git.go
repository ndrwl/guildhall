// Package git is Guildhall's boundary to the git program: the questions the Director asks of a repository,
// each answered by running git with an argument list (docs/contracts/git.md).
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Range is a task's change as a commit range: the commits from Base (exclusive) to Head.
type Range struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// Scope is how much of a new head a review round covers.
type Scope string

const (
	// ScopeFull reviews the whole head.
	ScopeFull Scope = "full"
	// ScopeRebase reviews only the resolution of a rebase of an already reviewed range.
	ScopeRebase Scope = "rebase"
	// ScopeResolution is the narrowed review of a resolution Guildhall's rebaser made (docs/interpreter.md#tiered-rebase):
	// only the hunks between the conflict-marked replay and its resolution.
	ScopeResolution Scope = "resolution"
)

// Classification is how a new range relates to the last reviewed one.
type Classification struct {
	Scope Scope
	// Reason says why the scope is full; empty for a rebase.
	Reason string
	// Diff is the resolution diff, the new head against its base, for a rebase; nil for a full scope.
	Diff []byte
}

// Program answers by running the git program.
type Program struct {
	// Path is the git executable; empty means "git" found on PATH.
	Path string
	// Timeout bounds each git run; zero means DefaultTimeout. A run past it is ended and is an error naming git
	// and the timeout.
	Timeout time.Duration
}

// DefaultTimeout is how long one git run may take when Program.Timeout is zero.
const DefaultTimeout = 5 * time.Minute

// Classify classifies a new range of a task against the range last reviewed, in the repository at repo.
//
// Classify returns ScopeRebase only when the new range is provably a rebase of the reviewed one:
//   - every commit id is a full object id, the bases differ, each base is an ancestor of its head and the
//     reviewed base is an ancestor of the new base;
//   - `git range-diff` pairs every reviewed commit with a new one and shows every pair's patch identical;
//   - no file the task changes in the reviewed range was changed by the base moving;
//   - the new range changes exactly the task's files, with each byte-identical to the reviewed head, and every
//     file the base changed is byte-identical at the new head to the new base.
//
// Anything else, including a commit missing from the repository or a git failure, is ScopeFull with a reason:
// uncertainty never narrows a review. Classify returns an error only when ctx ends; it writes nothing to the
// repository, and the same arguments over the same objects give the same classification.
func (p Program) Classify(ctx context.Context, repo string, last, next Range) (Classification, error) {
	reason, diff, err := p.classify(ctx, repo, last, next)
	if ctx.Err() != nil {
		return Classification{}, ctx.Err()
	}
	if err != nil {
		return Classification{Scope: ScopeFull, Reason: err.Error()}, nil
	}
	if reason != "" {
		return Classification{Scope: ScopeFull, Reason: reason}, nil
	}
	return Classification{Scope: ScopeRebase, Diff: diff}, nil
}

// classify returns why next is not a rebase of last, or the resolution diff when it is; an error is a git failure.
func (p Program) classify(ctx context.Context, repo string, last, next Range) (string, []byte, error) {
	for _, id := range []string{last.Base, last.Head, next.Base, next.Head} {
		if !digest.ValidHead(id) {
			return fmt.Sprintf("%q is not a full commit id", id), nil, nil
		}
	}
	if last.Base == next.Base {
		return "the base did not move", nil, nil
	}
	for _, pair := range [][2]string{{last.Base, last.Head}, {next.Base, next.Head}, {last.Base, next.Base}} {
		var exit *exec.ExitError
		if _, err := p.read(ctx, repo, "merge-base", "--is-ancestor", pair[0], pair[1]); errors.As(err, &exit) && exit.ExitCode() == 1 {
			return fmt.Sprintf("%s is not an ancestor of %s", pair[0], pair[1]), nil, nil
		} else if err != nil {
			return "", nil, err
		}
	}
	out, err := p.read(ctx, repo, "range-diff", "--ignore-submodules=none", "--no-color", "--no-patch", last.Base+".."+last.Head, next.Base+".."+next.Head)
	if err != nil {
		return "", nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if f := strings.Fields(line); len(f) < 4 || f[0] == "-:" || f[2] != "=" || f[3] == "-:" {
			return fmt.Sprintf("range-diff shows a changed, added or dropped commit: %s", strings.TrimSpace(line)), nil, nil
		}
	}
	task, err := p.files(ctx, repo, last.Base, last.Head)
	if err != nil {
		return "", nil, err
	}
	moved, err := p.files(ctx, repo, last.Base, next.Base)
	if err != nil {
		return "", nil, err
	}
	for _, f := range task {
		if slices.Contains(moved, f) {
			return fmt.Sprintf("the base changed %s, which the task changes", f), nil, nil
		}
	}
	changed, err := p.files(ctx, repo, next.Base, next.Head)
	if err != nil {
		return "", nil, err
	}
	if !slices.Equal(changed, task) {
		return fmt.Sprintf("the new range changes %q, the reviewed range %q", changed, task), nil, nil
	}
	same := func(a, b string, paths []string) (bool, error) {
		if len(paths) == 0 {
			return true, nil
		}
		out, err := p.files(ctx, repo, a, b, paths...)
		return len(out) == 0, err
	}
	if ok, err := same(last.Head, next.Head, task); err != nil || !ok {
		return "a task file differs from the reviewed head", nil, err
	}
	if ok, err := same(next.Base, next.Head, moved); err != nil || !ok {
		return "a file the base changed differs from the new base", nil, err
	}
	diff, err := p.read(ctx, repo, "diff", "--ignore-submodules=none", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", next.Base, next.Head)
	if err != nil {
		return "", nil, err
	}
	if len(diff) == 0 {
		return "the new range changes nothing", nil, nil
	}
	return "", diff, nil
}

// files lists the paths that differ between commits a and b, limited to paths when any are given, sorted.
func (p Program) files(ctx context.Context, repo, a, b string, paths ...string) ([]string, error) {
	args := []string{"diff", "--ignore-submodules=none", "--name-only", "-z", "--no-renames", "--no-ext-diff", a, b, "--"}
	out, err := p.read(ctx, repo, append(args, paths...)...)
	if err != nil {
		return nil, err
	}
	names := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(out) == 0 {
		names = nil
	}
	slices.Sort(names)
	return names, nil
}

// read runs git in repo with literal pathspecs, through the one runner, and returns its standard output.
func (p Program) read(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return p.git(ctx, repo, append([]string{"--literal-pathspecs"}, args...)...)
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
