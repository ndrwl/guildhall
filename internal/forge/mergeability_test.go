package forge_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

// This crosses real git and the strict command-specific gh stand-in. The base
// really diverges and merge-tree must recover paths from its exit-1 stdout.
func TestGitHubMergeabilityEvidence(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	gh := h.Forge.(forge.GitHub)
	head := h.Heads[0]
	pr, err := gh.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/conflict", Head: head, Title: "conflict"})
	if err != nil {
		t.Fatal(err)
	}
	g.git(t, h.Repository, "checkout", "-q", "--detach", h.BaseHead)
	if err := os.WriteFile(filepath.Join(h.Repository, "one"), []byte("different base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	g.git(t, h.Repository, "add", "one")
	g.git(t, h.Repository, "commit", "-q", "-m", "base moved")
	base := g.git(t, h.Repository, "rev-parse", "HEAD")
	g.git(t, h.Repository, "push", "-q", h.Remote, "HEAD:refs/heads/main")
	before := g.git(t, h.Repository, "status", "--porcelain")
	for _, tc := range []struct{ mergeable, status, want string }{
		{"CONFLICTING", "DIRTY", "conflicting"},
		{"MERGEABLE", "BEHIND", "behind"},
		// GitHub has not yet confirmed these against the moved main (d85), so the base is measured locally.
		{"UNKNOWN", "UNKNOWN", "behind"},
		{"MERGEABLE", "BLOCKED", "behind"},
		// GitHub confirmed these mergeable as they stand, so they stay clear though main moved.
		{"MERGEABLE", "CLEAN", ""},
		{"MERGEABLE", "UNSTABLE", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			s := g.readState(t)
			s.Mergeable, s.MergeStateStatus = tc.mergeable, tc.status
			g.writeState(t, s)
			got, err := gh.Mergeability(ctx, forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: head})
			if err != nil {
				t.Fatal(err)
			}
			// Base is the moved main tip, not the PR's stale recorded base, and empty when clear.
			wantBase := base
			if tc.want == "" {
				wantBase = ""
			}
			if got.State != tc.want || got.Head != head || got.Base != wantBase || got.URL != pr.URL {
				t.Fatalf("observation = %+v", got)
			}
			if tc.want != "" && (got.Behind != 1 || !slices.Equal(got.Files, []string{"one"})) {
				t.Fatalf("evidence = %+v", got)
			}
			forgetest.RunMergeabilityContract(t, forgetest.MergeabilityHarness{Reader: gh, Request: forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: head}, Want: got})
		})
	}
	if got := g.git(t, h.Repository, "rev-parse", "HEAD"); got != base {
		t.Fatalf("observation moved HEAD to %s", got)
	}
	if got := g.git(t, h.Repository, "status", "--porcelain"); got != before {
		t.Fatalf("observation changed worktree: %s", got)
	}
	_, err = gh.Mergeability(ctx, forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: h.Heads[1]})
	if !errors.Is(err, forge.ErrRefused) {
		t.Fatalf("moved head = %v", err)
	}
}

// An UNKNOWN pull request whose head already contains the base tip is measured
// locally, found not behind, and stays clear with Base empty.
func TestGitHubMergeabilityUnknownUpToDate(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	gh := h.Forge.(forge.GitHub)
	head := h.Heads[0]
	pr, err := gh.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/current", Head: head, Title: "current"})
	if err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	s.Mergeable, s.MergeStateStatus = "UNKNOWN", "UNKNOWN"
	g.writeState(t, s)
	req := forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: head}
	got, err := gh.Mergeability(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "" || got.Base != "" || got.Behind != 0 || got.Head != head || got.URL != pr.URL {
		t.Fatalf("observation = %+v", got)
	}
	forgetest.RunMergeabilityContract(t, forgetest.MergeabilityHarness{Reader: gh, Request: req, Want: got})
}

// A base branch deleted after pr create has no tip to measure against. Guildhall
// reports a plain error rather than guessing a state from GitHub's stale base.
func TestGitHubMergeabilityMissingBase(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	gh := h.Forge.(forge.GitHub)
	head := h.Heads[0]
	pr, err := gh.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/conflict", Head: head, Title: "conflict"})
	if err != nil {
		t.Fatal(err)
	}
	g.git(t, h.Repository, "--git-dir", g.bare, "update-ref", "-d", "refs/heads/main")
	s := g.readState(t)
	s.Mergeable, s.MergeStateStatus = "CONFLICTING", "DIRTY"
	g.writeState(t, s)
	got, err := gh.Mergeability(ctx, forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: head})
	if err == nil || errors.Is(err, forge.ErrRefused) || errors.Is(err, forge.ErrInvalid) {
		t.Fatalf("missing base = %v", err)
	}
	if got.State != "" {
		t.Fatalf("missing base reported %+v", got)
	}
}
