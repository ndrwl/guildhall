package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Replay asks for a task's whole change, Head, replayed as one commit onto Base, a base tip the repository holds.
type Replay struct {
	// Repository is the absolute path of the repository holding both commits.
	Repository string
	Base       string
	Head       string
}

// Conflict is one path a replay could not merge.
type Conflict struct {
	Path string `json:"path"`
	// Lines counts the conflicting lines of both sides, between the <<<<<<< and >>>>>>> markers and without the
	// marker lines; zero when Markers is false.
	Lines int `json:"lines"`
	// Markers reports whether the merged file holds conflict markers; a binary, delete/modify or rename conflict
	// has none.
	Markers bool `json:"markers"`
}

// Rebased is a replay of Head onto Base. With no conflicts, Commit is the replayed commit; with conflicts, Marked is
// a commit of the tree with conflict markers written in, and Commit is empty. Neither moves any branch.
type Rebased struct {
	Base      string     `json:"base"`
	Head      string     `json:"head"`
	MergeBase string     `json:"merge_base"`
	Tree      string     `json:"tree"`
	Commit    string     `json:"commit,omitempty"`
	Marked    string     `json:"marked,omitempty"`
	Conflicts []Conflict `json:"conflicts"`
	// HeadPatchID is git patch-id --stable of MergeBase..Head, and PatchID that of Base..Commit, empty with
	// conflicts. Equal ids mean the replay changes exactly what Head changed, context lines included.
	HeadPatchID string `json:"head_patch_id"`
	PatchID     string `json:"patch_id,omitempty"`
}

// ConflictLines is the total of the conflicts' lines.
func (r Rebased) ConflictLines() int {
	n := 0
	for _, c := range r.Conflicts {
		n += c.Lines
	}
	return n
}

// Rebase replays r.Head onto r.Base as one commit with git merge-tree --write-tree, so there is one conflict round
// and it runs against exactly the base tip given (docs/contracts/git.md#rebase). It never runs git rebase, never
// moves a branch and never touches a worktree: it writes only objects. The commit it writes takes Head's tree
// changes, Head's message, author and author date, and as its committer date the later of Head's and Base's, so
// replaying the same commits again writes the same commit. Both commits must be full object ids the repository
// holds; a git failure or ctx ending is an error.
func (p Program) Rebase(ctx context.Context, r Replay) (Rebased, error) {
	if !digest.ValidHead(r.Base) || !digest.ValidHead(r.Head) {
		return Rebased{}, fmt.Errorf("git: rebase of %q onto %q: not full object ids", r.Head, r.Base)
	}
	out := Rebased{Base: r.Base, Head: r.Head, Conflicts: []Conflict{}}
	mb, err := p.read(ctx, r.Repository, "merge-base", r.Base, r.Head)
	if err != nil {
		return Rebased{}, err
	}
	out.MergeBase = strings.TrimSpace(string(mb))
	if out.HeadPatchID, err = p.patchID(ctx, r.Repository, out.MergeBase, r.Head); err != nil {
		return Rebased{}, err
	}
	stdout, code, err := p.status(ctx, r.Repository, nil, nil, "merge-tree", "--write-tree", "-z", "--no-messages", r.Base, r.Head)
	if code != 0 && code != 1 {
		return Rebased{}, fmt.Errorf("git merge-tree %s %s: exit %d: %v", r.Base, r.Head, code, err)
	}
	fields := strings.Split(strings.TrimSuffix(string(stdout), "\x00"), "\x00")
	out.Tree = strings.TrimSpace(fields[0])
	if !digest.ValidHead(out.Tree) {
		return Rebased{}, fmt.Errorf("git merge-tree: no tree in %q", stdout)
	}
	var paths []string
	for _, f := range fields[1:] {
		// Each conflicted file info is "<mode> <object> <stage>\t<path>"; the list ends at an empty field.
		if f == "" {
			break
		}
		_, path, ok := strings.Cut(f, "\t")
		if ok && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	if code == 1 && len(paths) == 0 {
		return Rebased{}, fmt.Errorf("git merge-tree %s %s: conflicts without conflicted files", r.Base, r.Head)
	}
	slices.Sort(paths)
	for _, path := range paths {
		c := Conflict{Path: path}
		blob, err := p.read(ctx, r.Repository, "cat-file", "blob", out.Tree+":"+path)
		if err == nil {
			c.Lines, c.Markers = conflictLines(blob)
		}
		out.Conflicts = append(out.Conflicts, c)
	}
	commit, err := p.commitTree(ctx, r.Repository, out.Tree, r.Base, r.Head)
	if err != nil {
		return Rebased{}, err
	}
	if len(out.Conflicts) > 0 {
		out.Marked = commit
		return out, nil
	}
	out.Commit = commit
	if out.PatchID, err = p.patchID(ctx, r.Repository, r.Base, commit); err != nil {
		return Rebased{}, err
	}
	return out, nil
}

// Squash writes one commit with head's tree on parent base, with head's message, author and author date and the
// later of their committer dates, as Rebase writes its commit, and returns it: a resolution committed on a marked
// head becomes one commit on the base. It moves no branch.
func (p Program) Squash(ctx context.Context, repository, base, head string) (string, error) {
	if !digest.ValidHead(base) || !digest.ValidHead(head) {
		return "", fmt.Errorf("git: squash of %q onto %q: not full object ids", head, base)
	}
	tree, err := p.read(ctx, repository, "rev-parse", "--verify", "--end-of-options", head+"^{tree}")
	if err != nil {
		return "", err
	}
	return p.commitTree(ctx, repository, strings.TrimSpace(string(tree)), base, head)
}

// PatchID returns git patch-id --stable of the change from a to b, empty for no change.
func (p Program) PatchID(ctx context.Context, repository, a, b string) (string, error) {
	return p.patchID(ctx, repository, a, b)
}

// Diff returns the full diff from a to b, with no renames, colour or external diff.
func (p Program) Diff(ctx context.Context, repository, a, b string) ([]byte, error) {
	return p.read(ctx, repository, "diff", "--no-color", "--no-ext-diff", "--no-renames", "--full-index", a, b, "--")
}

// Adopt moves the worktree w.Dir, on w.Branch at from, to commit to with git reset --keep, which keeps local and
// untracked files and refuses rather than overwrite one, so no stage output is lost. A worktree already at to is
// left as it is; one at neither commit, or on another branch, is refused unchanged.
func (p Program) Adopt(ctx context.Context, w Workspace, from, to string) error {
	if err := p.validate(ctx, w); err != nil {
		return err
	}
	branch, err := p.git(ctx, w.Dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(string(branch)) != "refs/heads/"+w.Branch {
		return fmt.Errorf("git: %s is not on branch %s", w.Dir, w.Branch)
	}
	at, err := p.git(ctx, w.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(at)) {
	case to:
		return nil
	case from:
	default:
		return fmt.Errorf("git: branch %s is at %s, not %s", w.Branch, strings.TrimSpace(string(at)), from)
	}
	_, err = p.git(ctx, w.Dir, "reset", "--keep", "--quiet", to)
	return err
}

// commitTree writes a commit of tree on parent with the message, author and author date of like and the later of
// like's and parent's committer dates.
func (p Program) commitTree(ctx context.Context, repository, tree, parent, like string) (string, error) {
	meta, err := p.read(ctx, repository, "show", "-s", "--format=%an%x00%ae%x00%aD%x00%cn%x00%ce%x00%ct%x00%B", like)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(string(meta), "\x00", 7)
	if len(f) != 7 {
		return "", fmt.Errorf("git show %s: unexpected format", like)
	}
	pt, err := p.read(ctx, repository, "show", "-s", "--format=%ct", parent)
	if err != nil {
		return "", err
	}
	likeDate, err1 := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64)
	parentDate, err2 := strconv.ParseInt(strings.TrimSpace(string(pt)), 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return "", fmt.Errorf("git: committer dates of %s and %s: %w", like, parent, err)
	}
	env := []string{"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2],
		"GIT_COMMITTER_NAME=" + f[3], "GIT_COMMITTER_EMAIL=" + f[4], fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", max(likeDate, parentDate))}
	out, code, err := p.status(ctx, repository, []byte(strings.TrimRight(f[6], "\n")+"\n"), env, "commit-tree", "--no-gpg-sign", tree, "-p", parent)
	if err != nil || code != 0 {
		return "", fmt.Errorf("git commit-tree %s -p %s: exit %d: %v", tree, parent, code, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// patchID is git patch-id --stable of the diff from a to b.
func (p Program) patchID(ctx context.Context, repository, a, b string) (string, error) {
	diff, err := p.read(ctx, repository, "diff", "--no-color", "--no-ext-diff", "--no-renames", "--full-index", a, b, "--")
	if err != nil {
		return "", err
	}
	out, code, err := p.status(ctx, repository, diff, nil, "patch-id", "--stable")
	if err != nil || code != 0 {
		return "", fmt.Errorf("git patch-id: exit %d: %v", code, err)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return id, nil
}

// conflictLines counts the lines between conflict markers, without the marker lines, and reports whether there were
// any markers. A region runs from a "<<<<<<< " line to a ">>>>>>> " line; the "=======" and diff3 "|||||||" lines
// inside it are markers too.
func conflictLines(blob []byte) (int, bool) {
	n, in, markers := 0, false, false
	for line := range strings.Lines(string(blob)) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "<<<<<<< ") || line == "<<<<<<<":
			in, markers = true, true
		case in && (strings.HasPrefix(line, ">>>>>>> ") || line == ">>>>>>>"):
			in = false
		case in && (line == "=======" || strings.HasPrefix(line, "||||||| ") || line == "|||||||"):
		case in:
			n++
		}
	}
	return n, markers
}

// status runs git in dir, as git does, with stdin and extra environment, and returns its standard output and exit
// code; a run that could not start or was ended is an error.
func (p Program) status(ctx context.Context, dir string, stdin []byte, env []string, args ...string) ([]byte, int, error) {
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
	cmd := exec.CommandContext(runCtx, path, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out, 0, nil
	case ctx.Err() != nil:
		return nil, -1, ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, -1, fmt.Errorf("git %s: no answer within %v: %w", strings.Join(args, " "), timeout, context.DeadlineExceeded)
	case errors.As(err, &exit):
		return out, exit.ExitCode(), errors.New(strings.TrimSpace(stderr.String()))
	}
	return nil, -1, err
}
