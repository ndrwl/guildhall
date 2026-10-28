package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Subject returns the subject line of commit rev, a full commit id, in the repository at repo. A commit the
// repository lacks, an invalid id and any git failure are errors.
func (p Program) Subject(ctx context.Context, repo, rev string) (string, error) {
	if !digest.ValidHead(rev) {
		return "", fmt.Errorf("%q is not a full commit id", rev)
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

// Behind returns how many first-parent commits ref has after rev, a full commit id, in the repository at repo, and
// their subjects, newest first: `git rev-list --count --first-parent` and `git log --first-parent --format=%s` of
// rev..ref, read with no network call. A missing ref, a commit the repository lacks and any git failure are errors.
func (p Program) Behind(ctx context.Context, repo, rev, ref string) (int, []string, error) {
	if !digest.ValidHead(rev) {
		return 0, nil, fmt.Errorf("%q is not a full commit id", rev)
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
