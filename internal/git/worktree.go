package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// WorkTree is a non-bare checkout at Dir, such as the Steward's private config checkout, that Guildhall commits named
// files to while a person may hold uncommitted edits in it (docs/steward.md#collection): a commit is built with
// plumbing in a private index, so no other path is ever swept in, and the checkout then moves to it by a two-way
// merge that keeps every other staged and unstaged change, as `git checkout` does.
type WorkTree struct {
	Program
	Dir string
}

// ErrPushRefused reports a push the remote refused again after the one replay onto its newer tip.
var ErrPushRefused = errors.New("the remote refused the push")

// run runs git in the work tree with extra environment and stdin and returns its standard output.
func (w WorkTree) run(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
	out, code, err := w.status(ctx, w.Dir, stdin, env, args...)
	if err != nil || code != 0 {
		return out, fmt.Errorf("git %s: exit %d: %w", strings.Join(args, " "), code, err)
	}
	return out, nil
}

func (w WorkTree) line(ctx context.Context, args ...string) (string, error) {
	out, err := w.run(ctx, nil, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// Head returns the commit HEAD names.
func (w WorkTree) Head(ctx context.Context) (string, error) {
	return w.line(ctx, "rev-parse", "--verify", "HEAD^{commit}")
}

// Show returns file, a slash-separated path, as commit rev holds it, and false when rev holds no such file.
func (w WorkTree) Show(ctx context.Context, rev, file string) ([]byte, bool, error) {
	_, code, err := w.status(ctx, w.Dir, nil, nil, "cat-file", "-e", rev+":"+file)
	switch {
	case code == 128 || code == 1:
		return nil, false, nil
	case err != nil || code != 0:
		return nil, false, fmt.Errorf("git cat-file -e %s:%s: exit %d: %w", rev, file, code, err)
	}
	data, err := w.run(ctx, nil, nil, "cat-file", "blob", rev+":"+file)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Grep returns the files below dirs, slash-separated directories of the work tree, that commit rev holds with a line
// matching one of the extended regular expressions patterns, sorted; none is an empty list.
func (w WorkTree) Grep(ctx context.Context, rev string, patterns []string, dirs ...string) ([]string, error) {
	args := []string{"grep", "-l", "-I", "--no-color", "-E"}
	for _, p := range patterns {
		args = append(args, "-e", p)
	}
	args = append(append(args, rev, "--"), dirs...)
	out, code, err := w.status(ctx, w.Dir, nil, nil, args...)
	switch {
	case code == 1:
		return []string{}, nil
	case err != nil || code != 0:
		return nil, fmt.Errorf("git grep: exit %d: %w", code, err)
	}
	files := []string{}
	for line := range strings.Lines(string(out)) {
		files = append(files, strings.TrimPrefix(strings.TrimRight(line, "\n"), rev+":"))
	}
	slices.Sort(files)
	return files, nil
}

// validFile reports whether name is a relative slash-separated path with no empty, "." or ".." segment and no
// leading '-', so it can name only a file below the work tree.
func validFile(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\\\x00\n\t") {
		return false
	}
	for seg := range strings.SplitSeq(name, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == ".git" {
			return false
		}
	}
	return path.Clean(name) == name
}

// CommitFiles commits files, slash-separated paths below the work tree and their contents, on top of HEAD with
// message and sig, and returns the new commit; when every file already has that content at HEAD it commits nothing
// and returns HEAD. No other path is staged or committed: the commit's tree is HEAD's with only these files set.
// The checkout's branch, index and work tree then move to the commit, writing only these files; every other local
// change, staged or not, is kept. A named file with a local change of its own fails the commit and moves nothing.
func (w WorkTree) CommitFiles(ctx context.Context, files map[string][]byte, message string, sig Signature) (string, error) {
	head, err := w.Head(ctx)
	if err != nil {
		return "", err
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range files {
			if !yield(n) {
				return
			}
		}
	})
	var info bytes.Buffer
	changed := false
	for _, name := range names {
		if !validFile(name) {
			return "", fmt.Errorf("git: refusing to commit %q: not a plain path below the work tree", name)
		}
		blob, err := w.run(ctx, nil, files[name], "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(blob))
		if old, ok, err := w.Show(ctx, head, name); err != nil {
			return "", err
		} else if !ok || !bytes.Equal(old, files[name]) {
			changed = true
		}
		fmt.Fprintf(&info, "100644 %s\t%s\n", id, name)
	}
	if !changed {
		return head, nil
	}
	commit, err := w.commitOn(ctx, head, info.Bytes(), message, sig)
	if err != nil {
		return "", err
	}
	return commit, w.moveTo(ctx, head, commit)
}

// commitOn writes a commit of parent's tree with the index-info lines applied, with message and sig, on parent.
func (w WorkTree) commitOn(ctx context.Context, parent string, indexInfo []byte, message string, sig Signature) (string, error) {
	tmp, err := os.MkdirTemp("", "guildhall-index-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := w.run(ctx, env, nil, "read-tree", parent); err != nil {
		return "", err
	}
	if _, err := w.run(ctx, env, indexInfo, "update-index", "--index-info"); err != nil {
		return "", err
	}
	tree, err := w.run(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	date := fmt.Sprintf("@%d +0000", sig.When.Unix())
	cenv := []string{"GIT_AUTHOR_NAME=" + sig.Name, "GIT_AUTHOR_EMAIL=" + sig.Email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + sig.Name, "GIT_COMMITTER_EMAIL=" + sig.Email, "GIT_COMMITTER_DATE=" + date}
	out, err := w.run(ctx, cenv, []byte(strings.TrimRight(message, "\n")+"\n"), "commit-tree", "--no-gpg-sign", strings.TrimSpace(string(tree)), "-p", parent)
	return strings.TrimSpace(string(out)), err
}

// moveTo moves the checkout from commit from to commit to: a two-way merge updates the index and the work tree for
// the paths that differ between them, keeping every other local change and failing, with nothing moved, when one of
// those paths has a local change, and the branch HEAD names then moves to to only if it still points at from.
func (w WorkTree) moveTo(ctx context.Context, from, to string) error {
	if _, err := w.run(ctx, nil, nil, "read-tree", "-m", "-u", from, to); err != nil {
		return fmt.Errorf("the checkout has local changes to a file the commit writes; commit or discard them: %w", err)
	}
	_, err := w.run(ctx, nil, nil, "update-ref", "-m", "guildhall: commit", "HEAD", to, from)
	return err
}

// upstream is the remote and branch the checked-out branch tracks.
func (w WorkTree) upstream(ctx context.Context) (remote, branch string, err error) {
	ref, err := w.line(ctx, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("the checkout has no branch checked out: %w", err)
	}
	name := strings.TrimPrefix(ref, "refs/heads/")
	remote, err1 := w.line(ctx, "config", "--get", "branch."+name+".remote")
	merge, err2 := w.line(ctx, "config", "--get", "branch."+name+".merge")
	if err1 != nil || err2 != nil || remote == "" || remote == "." || !strings.HasPrefix(merge, "refs/heads/") || strings.HasPrefix(remote, "-") {
		return "", "", fmt.Errorf("branch %s of the checkout tracks no remote branch to push to; set one with git branch --set-upstream-to", name)
	}
	return remote, strings.TrimPrefix(merge, "refs/heads/"), nil
}

// Publish pushes HEAD to the remote branch the checked-out branch tracks, fast-forward only, and returns the commit
// pushed; it pushes nothing when the remote-tracking ref already contains HEAD. When the remote refuses because it
// moved ahead, Publish fetches it, replays the local commits it lacks onto its tip with their messages, moves the
// checkout there as CommitFiles does, and pushes once more; a second refusal wraps ErrPushRefused.
func (w WorkTree) Publish(ctx context.Context, sig Signature) (string, error) {
	remote, branch, err := w.upstream(ctx)
	if err != nil {
		return "", err
	}
	head, err := w.Head(ctx)
	if err != nil {
		return "", err
	}
	tracking := "refs/remotes/" + remote + "/" + branch
	if _, code, _ := w.status(ctx, w.Dir, nil, nil, "merge-base", "--is-ancestor", head, tracking); code == 0 {
		return head, nil
	}
	if err := w.push(ctx, remote, branch); err == nil {
		return head, nil
	} else if _, ferr := w.run(ctx, nil, nil, "fetch", "--quiet", "--no-tags", "--end-of-options", remote, "+refs/heads/"+branch+":"+tracking); ferr != nil {
		return "", fmt.Errorf("%w; fetching %s: %w", err, remote, ferr)
	} else if _, code, _ := w.status(ctx, w.Dir, nil, nil, "merge-base", "--is-ancestor", tracking, head); code == 0 {
		// The remote did not move ahead; the push failed for another reason.
		return "", err
	}
	tip, err := w.line(ctx, "rev-parse", "--verify", tracking+"^{commit}")
	if err != nil {
		return "", err
	}
	replayed, err := w.replay(ctx, head, tip, sig)
	if err != nil {
		return "", err
	}
	if err := w.moveTo(ctx, head, replayed); err != nil {
		return "", err
	}
	if err := w.push(ctx, remote, branch); err != nil {
		return "", fmt.Errorf("%w: %w", ErrPushRefused, err)
	}
	return replayed, nil
}

func (w WorkTree) push(ctx context.Context, remote, branch string) error {
	_, err := w.run(ctx, nil, nil, "push", "--quiet", "--porcelain", "--no-verify", "--end-of-options", remote, "HEAD:refs/heads/"+branch)
	return err
}

// replay writes each commit of head that onto lacks again on top of onto, oldest first, each with its own changes
// and message, and returns the last; a merge among them is refused.
func (w WorkTree) replay(ctx context.Context, head, onto string, sig Signature) (string, error) {
	out, err := w.line(ctx, "rev-list", "--reverse", "--parents", onto+".."+head)
	if err != nil {
		return "", err
	}
	parent := onto
	for rec := range strings.Lines(out) {
		ids := strings.Fields(rec)
		if len(ids) != 2 {
			return "", fmt.Errorf("git: cannot replay %s onto the remote: it is a merge or a root commit", ids[0])
		}
		diff, err := w.run(ctx, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--no-commit-id", ids[1], ids[0])
		if err != nil {
			return "", err
		}
		var info bytes.Buffer
		fields := strings.Split(strings.TrimSuffix(string(diff), "\x00"), "\x00")
		for i := 0; i+1 < len(fields); i += 2 {
			// ":<old mode> <new mode> <old id> <new id> <status>" then the path.
			meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
			if len(meta) != 5 {
				return "", fmt.Errorf("git diff-tree: unexpected record %q", fields[i])
			}
			if meta[4] == "D" {
				fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", len(meta[3])), fields[i+1])
				continue
			}
			fmt.Fprintf(&info, "%s %s\t%s\n", meta[1], meta[3], fields[i+1])
		}
		msg, err := w.run(ctx, nil, nil, "log", "-1", "--format=%B", ids[0])
		if err != nil {
			return "", err
		}
		if parent, err = w.commitOn(ctx, parent, info.Bytes(), string(msg), sig); err != nil {
			return "", err
		}
	}
	return parent, nil
}
