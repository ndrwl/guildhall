package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// minAbbrevCommit is the shortest abbreviated commit id Subject and Behind take: a Go pseudo-version's.
const minAbbrevCommit = 12

// validCommit reports whether rev is a full commit id or one abbreviated to at least minAbbrevCommit lowercase hex
// characters, which git resolves in the repository or refuses as missing or ambiguous.
func validCommit(rev string) bool {
	if digest.ValidHead(rev) {
		return true
	}
	if len(rev) < minAbbrevCommit || len(rev) > 64 {
		return false
	}
	for _, c := range []byte(rev) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Subject returns the subject line of commit rev, a full or abbreviated commit id (validCommit), in the repository
// at repo. A commit the repository lacks, an ambiguous or invalid id and any git failure are errors.
func (p Program) Subject(ctx context.Context, repo, rev string) (string, error) {
	if !validCommit(rev) {
		return "", fmt.Errorf("%q is not a commit id", rev)
	}
	out, err := p.git(ctx, repo, "log", "-1", "--no-show-signature", "--format=%s", rev+"^{commit}", "--")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchDefault records the commit remote's default branch is at as ref in the repository at repo, with `git fetch
// --no-tags --no-write-fetch-head <remote> +HEAD:<ref>`. ref must start with refs/guildhall/ and remote must not
// start with "-"; an unreachable remote, a run past the bound and any git failure are errors, leaving ref as it was.
func (p Program) FetchDefault(ctx context.Context, repo, remote, ref string) error {
	if !strings.HasPrefix(ref, "refs/guildhall/") {
		return fmt.Errorf("ref %q is not under refs/guildhall/", ref)
	}
	if remote == "" || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("remote %q is not a remote", remote)
	}
	_, err := p.git(ctx, repo, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", remote, "+HEAD:"+ref)
	return err
}

// Behind returns how many first-parent commits ref has after rev, a full or abbreviated commit id (validCommit), in
// the repository at repo, and their subjects, newest first: `git rev-list --count --first-parent` and `git log
// --first-parent --format=%s` of rev..ref, read with no network call. A missing ref, a commit the repository lacks
// and any git failure are errors.
func (p Program) Behind(ctx context.Context, repo, rev, ref string) (int, []string, error) {
	if !validCommit(rev) {
		return 0, nil, fmt.Errorf("%q is not a commit id", rev)
	}
	if !strings.HasPrefix(ref, "refs/") {
		return 0, nil, fmt.Errorf("ref %q is not a full ref name", ref)
	}
	span := rev + ".." + ref
	out, err := p.git(ctx, repo, "rev-list", "--count", "--first-parent", span, "--")
	if err != nil {
		return 0, nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, nil, fmt.Errorf("git rev-list --count: %w", err)
	}
	out, err = p.git(ctx, repo, "log", "--first-parent", "--no-show-signature", "--format=%s", span, "--")
	if err != nil {
		return 0, nil, err
	}
	var subjects []string
	for line := range strings.Lines(string(out)) {
		subjects = append(subjects, strings.TrimRight(line, "\r\n"))
	}
	if len(subjects) != count {
		return 0, nil, errors.New("git log and git rev-list disagree on the commits behind")
	}
	return count, subjects, nil
}
