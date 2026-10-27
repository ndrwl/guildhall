package forge

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Mergeability is an observation of one PR head against its base branch.
// State is conflicting, behind (blocked by strict branch protection), or empty
// when no such obstruction is known, including GitHub's UNKNOWN state.
// Base is the base tip: the base branch's commit on the forge when measured, read
// with ls-remote and fetched exactly. It is empty when no obstruction is reported.
// Files and Behind are computed locally against that Base, without checking out,
// merging or rebasing any worktree. Files are sorted, unique paths.
// A reader must compare Head to the head it is waiting on before using evidence.
type Mergeability struct {
	URL    string   `json:"url"`
	Head   string   `json:"head"`
	Base   string   `json:"base"`
	State  string   `json:"state"`
	Files  []string `json:"files"`
	Behind int      `json:"behind"`
}

// Mergeability reads PR m.Number with command-specific gh flags. Invalid requests
// are ErrInvalid; unsupported remotes, process and fetch failures are errors, not
// assumed conflicts. A moved PR is ErrRefused. Only an open PR can obstruct work.
// Only a conflicting or behind PR resolves its base tip, with git ls-remote of the
// PR's baseRefName, and fetches that exact OID; a missing, ambiguous or invalid
// tip is an error. Clear observations leave Base empty and need only gh pr view.
// Concurrent observations are safe; they fetch objects but change no branches.
func (g GitHub) Mergeability(ctx context.Context, m Merge) (Mergeability, error) {
	if err := m.Validate(); err != nil {
		return Mergeability{}, err
	}
	repo, err := GitHubRepository(m.Remote)
	if err != nil {
		return Mergeability{}, err
	}
	var p struct {
		URL, State, HeadRefOid, BaseRefName, Mergeable, MergeStateStatus string
	}
	if err := g.ghJSON(ctx, m.Repository, &p, "pr", "view", strconv.Itoa(m.Number), "--repo", repo, "--json", "url,state,headRefOid,baseRefName,mergeable,mergeStateStatus"); err != nil {
		return Mergeability{}, err
	}
	out := Mergeability{URL: p.URL, Head: p.HeadRefOid, Files: []string{}}
	if p.HeadRefOid != m.Head {
		return Mergeability{}, fmt.Errorf("%w: pull request %s moved from %s to %s", ErrRefused, p.URL, m.Head, p.HeadRefOid)
	}
	if p.State != "OPEN" {
		return out, nil
	}
	switch {
	case p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY":
		out.State = "conflicting"
	case p.MergeStateStatus == "BEHIND":
		out.State = "behind"
	default:
		return out, nil
	}
	// GitHub's recorded base is the one it last compared against and goes stale, so
	// read the base branch's tip now and measure everything against that one OID.
	if !ValidBranch(p.BaseRefName) {
		return Mergeability{}, fmt.Errorf("pull request %s has invalid base branch %q", p.URL, p.BaseRefName)
	}
	ref := "refs/heads/" + p.BaseRefName
	lines, err := g.git(ctx, m.Repository, "ls-remote", m.Remote, ref)
	if err != nil {
		return Mergeability{}, err
	}
	tip, name, ok := strings.Cut(strings.TrimSuffix(string(lines), "\n"), "\t")
	if !ok || name != ref || !digest.ValidHead(tip) {
		return Mergeability{}, fmt.Errorf("pull request %s base branch %q has no single tip on %s: %q", p.URL, p.BaseRefName, m.Remote, lines)
	}
	if _, err := g.git(ctx, m.Repository, "fetch", "--no-tags", "--no-write-fetch-head", m.Remote, tip); err != nil {
		return Mergeability{}, err
	}
	out.Base = tip
	count, err := g.git(ctx, m.Repository, "rev-list", "--count", m.Head+".."+tip)
	if err != nil {
		return Mergeability{}, err
	}
	out.Behind, err = strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil {
		return Mergeability{}, err
	}
	data, err := g.git(ctx, m.Repository, "merge-tree", "--write-tree", "--name-only", "-z", m.Head, tip)
	if err != nil {
		var run *runError
		var exit *exec.ExitError
		if !errors.As(err, &run) || !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return Mergeability{}, err
		}
	}
	// A conflicted merge-tree exits 1 but still writes its tree and NUL-delimited
	// paths to stdout. The empty field after paths separates informational messages.
	fields := strings.Split(string(data), "\x00")
	if len(fields) == 0 || !digest.ValidHead(strings.TrimSpace(fields[0])) {
		return Mergeability{}, fmt.Errorf("git merge-tree returned no tree for %s", p.URL)
	}
	for _, file := range fields[1:] {
		if file == "" {
			break
		}
		out.Files = append(out.Files, file)
	}
	slices.Sort(out.Files)
	out.Files = slices.Compact(out.Files)
	return out, nil
}
