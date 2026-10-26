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

// Mergeability is an observation of one PR head against its current base.
// State is conflicting, behind (blocked by strict branch protection), or empty
// when no such obstruction is known, including GitHub's UNKNOWN state.
// Files and Behind are computed locally against the exact fetched Base, without
// checking out, merging or rebasing any worktree. Files are sorted, unique paths.
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
		URL, State, HeadRefOid, BaseRefOid, Mergeable, MergeStateStatus string
	}
	if err := g.ghJSON(ctx, m.Repository, &p, "pr", "view", strconv.Itoa(m.Number), "--repo", repo, "--json", "url,state,headRefOid,baseRefOid,mergeable,mergeStateStatus"); err != nil {
		return Mergeability{}, err
	}
	out := Mergeability{URL: p.URL, Head: p.HeadRefOid, Base: p.BaseRefOid, Files: []string{}}
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
	if !digest.ValidHead(p.BaseRefOid) {
		return Mergeability{}, fmt.Errorf("pull request %s has invalid base commit %q", p.URL, p.BaseRefOid)
	}
	if _, err := g.git(ctx, m.Repository, "fetch", "--no-tags", "--no-write-fetch-head", m.Remote, p.BaseRefOid); err != nil {
		return Mergeability{}, err
	}
	count, err := g.git(ctx, m.Repository, "rev-list", "--count", m.Head+".."+p.BaseRefOid)
	if err != nil {
		return Mergeability{}, err
	}
	out.Behind, err = strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil {
		return Mergeability{}, err
	}
	data, err := g.git(ctx, m.Repository, "merge-tree", "--write-tree", "--name-only", "-z", m.Head, p.BaseRefOid)
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
