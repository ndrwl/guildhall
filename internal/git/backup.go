package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// The helpers below build and publish commits in a bare repository from files outside it, as a Director copy's
// backup does (docs/running.md#backups): files are staged into a private index with update-index, whose stat cache
// skips files that did not change, trees and commits are written with plumbing, and nothing is checked out.

// Bare is a bare repository at Dir.
type Bare struct {
	Program
	Dir string
}

// InitBare creates the bare repository at dir when it does not exist yet, and sets it to store bytes as they are on
// every platform. Running it on an existing repository only sets those settings again.
func (p Program) InitBare(ctx context.Context, dir string) (Bare, error) {
	if _, err := p.git(ctx, "", "init", "--quiet", "--bare", dir); err != nil {
		return Bare{}, err
	}
	b := Bare{Program: p, Dir: dir}
	for _, kv := range [][2]string{{"core.autocrlf", "false"}, {"core.safecrlf", "false"}, {"core.filemode", "false"},
		{"core.symlinks", "false"}, {"core.fsmonitor", "false"}, {"gc.autoDetach", "false"}} {
		if _, err := b.run(ctx, "", nil, nil, "config", kv[0], kv[1]); err != nil {
			return Bare{}, err
		}
	}
	return b, nil
}

// run runs git on the bare repository, from workTree with it as the work tree when one is given, with extra
// environment and stdin, and returns its standard output.
func (b Bare) run(ctx context.Context, workTree string, env []string, stdin []byte, args ...string) ([]byte, error) {
	dir := b.Dir
	env = append([]string{"GIT_DIR=" + b.Dir}, env...)
	if workTree != "" {
		dir = workTree
		env = append(env, "GIT_WORK_TREE="+workTree)
	}
	out, code, err := b.status(ctx, dir, stdin, env, args...)
	if err != nil || code != 0 {
		return out, fmt.Errorf("git %s: exit %d: %w", strings.Join(args, " "), code, err)
	}
	return out, nil
}

// Stage brings index, a private index file, to exactly paths, the slash-separated paths of regular files relative
// to workTree: each is added or refreshed, and every other path in the index is removed. A path whose stat data
// matches its index entry is not read again.
func (b Bare) Stage(ctx context.Context, index, workTree string, paths []string) error {
	env := []string{"GIT_INDEX_FILE=" + index}
	out, err := b.run(ctx, workTree, env, nil, "ls-files", "-z", "--cached")
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		keep[p] = true
	}
	var gone bytes.Buffer
	for name := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if name != "" && !keep[name] {
			gone.WriteString(name + "\x00")
		}
	}
	if gone.Len() > 0 {
		if _, err := b.run(ctx, workTree, env, gone.Bytes(), "update-index", "-z", "--force-remove", "--stdin"); err != nil {
			return err
		}
	}
	if len(paths) == 0 {
		return nil
	}
	var add bytes.Buffer
	for _, p := range paths {
		add.WriteString(p + "\x00")
	}
	// --remove drops a file that vanished since the caller listed it.
	_, err = b.run(ctx, workTree, env, add.Bytes(), "update-index", "-z", "--add", "--remove", "--stdin")
	return err
}

// WriteTree writes index's tree and returns its id.
func (b Bare) WriteTree(ctx context.Context, index string) (string, error) {
	out, err := b.run(ctx, "", []string{"GIT_INDEX_FILE=" + index}, nil, "write-tree")
	return strings.TrimSpace(string(out)), err
}

// HashBlob writes data as a blob and returns its id.
func (b Bare) HashBlob(ctx context.Context, data []byte) (string, error) {
	out, err := b.run(ctx, "", nil, data, "hash-object", "-w", "--stdin")
	return strings.TrimSpace(string(out)), err
}

// TreeEntry is one entry of a tree MakeTree writes: a blob or a tree, named Name.
type TreeEntry struct {
	Tree bool
	ID   string
	Name string
}

// MakeTree writes a tree of entries and returns its id.
func (b Bare) MakeTree(ctx context.Context, entries []TreeEntry) (string, error) {
	var in bytes.Buffer
	for _, e := range entries {
		if e.Tree {
			fmt.Fprintf(&in, "040000 tree %s\t%s\x00", e.ID, e.Name)
		} else {
			fmt.Fprintf(&in, "100644 blob %s\t%s\x00", e.ID, e.Name)
		}
	}
	out, err := b.run(ctx, "", nil, in.Bytes(), "mktree", "-z")
	return strings.TrimSpace(string(out)), err
}

// Signature is a commit's author and committer.
type Signature struct {
	Name, Email string
	When        time.Time
}

// CommitTree writes a commit of tree with message, on parent unless it is empty, and returns its id.
func (b Bare) CommitTree(ctx context.Context, tree, parent, message string, sig Signature) (string, error) {
	date := fmt.Sprintf("@%d +0000", sig.When.Unix())
	env := []string{"GIT_AUTHOR_NAME=" + sig.Name, "GIT_AUTHOR_EMAIL=" + sig.Email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + sig.Name, "GIT_COMMITTER_EMAIL=" + sig.Email, "GIT_COMMITTER_DATE=" + date}
	args := []string{"commit-tree", "--no-gpg-sign", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := b.run(ctx, "", env, []byte(strings.TrimRight(message, "\n")+"\n"), args...)
	return strings.TrimSpace(string(out)), err
}

// Resolve returns the commit rev names, and false when there is none.
func (b Bare) Resolve(ctx context.Context, rev string) (string, bool, error) {
	out, code, err := b.status(ctx, b.Dir, nil, []string{"GIT_DIR=" + b.Dir}, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	switch {
	case code == 1:
		return "", false, nil
	case err != nil || code != 0:
		return "", false, fmt.Errorf("git rev-parse %s: exit %d: %w", rev, code, err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// TreeOf returns commit's tree.
func (b Bare) TreeOf(ctx context.Context, commit string) (string, error) {
	out, err := b.run(ctx, "", nil, nil, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	return strings.TrimSpace(string(out)), err
}

// SetRef points ref at commit.
func (b Bare) SetRef(ctx context.Context, ref, commit string) error {
	_, err := b.run(ctx, "", nil, nil, "update-ref", ref, commit)
	return err
}

// Push pushes commit to branch of remote, fast-forward only: the refspec has no '+', so a branch that does not
// contain the remote's tip is refused by git, never forced. It never deletes a branch.
func (b Bare) Push(ctx context.Context, remote, commit, branch string) error {
	if !digest.ValidHead(commit) || strings.HasPrefix(remote, "-") || branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("git push: refusing remote %q commit %q branch %q", remote, commit, branch)
	}
	_, err := b.run(ctx, "", nil, nil, "push", "--quiet", "--porcelain", "--no-verify", "--end-of-options", remote, commit+":refs/heads/"+branch)
	return err
}

// RemoteBranch returns the commit branch of remote points at, and false when remote has no such branch.
func (b Bare) RemoteBranch(ctx context.Context, remote, branch string) (string, bool, error) {
	if strings.HasPrefix(remote, "-") {
		return "", false, fmt.Errorf("git ls-remote: refusing remote %q", remote)
	}
	out, err := b.run(ctx, "", nil, nil, "ls-remote", "--refs", remote, "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	for line := range strings.Lines(string(out)) {
		id, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && ref == "refs/heads/"+branch {
			return id, true, nil
		}
	}
	return "", false, nil
}

// Fetch fetches rev, a branch or a full commit id, from remote into the local ref into, and returns the commit
// fetched.
func (b Bare) Fetch(ctx context.Context, remote, rev, into string) (string, error) {
	if strings.HasPrefix(remote, "-") || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("git fetch: refusing remote %q rev %q", remote, rev)
	}
	src := rev
	if !digest.ValidHead(rev) {
		src = "refs/heads/" + rev
	}
	if _, err := b.run(ctx, "", nil, nil, "fetch", "--quiet", "--no-tags", "--end-of-options", remote, "+"+src+":"+into); err != nil {
		return "", err
	}
	id, ok, err := b.Resolve(ctx, into)
	if err == nil && !ok {
		err = fmt.Errorf("git fetch %s: %s fetched no commit", rev, into)
	}
	return id, err
}

// FetchAll fetches every branch of remote into refs under prefix, such as "refs/guildhall/all/".
func (b Bare) FetchAll(ctx context.Context, remote, prefix string) error {
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("git fetch: refusing remote %q", remote)
	}
	_, err := b.run(ctx, "", nil, nil, "fetch", "--quiet", "--no-tags", "--end-of-options", remote, "+refs/heads/*:"+prefix+"*")
	return err
}

// Archive returns commit's tree as a tar archive.
func (b Bare) Archive(ctx context.Context, commit string) ([]byte, error) {
	return b.run(ctx, "", nil, nil, "archive", "--format=tar", "--end-of-options", commit)
}

// GC runs git gc --auto, which packs loose objects once there are enough of them.
func (b Bare) GC(ctx context.Context) error {
	_, err := b.run(ctx, "", nil, nil, "gc", "--auto", "--quiet")
	return err
}
