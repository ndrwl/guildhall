package forge_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
)

// TestGitHubStartCI: StartCI views the pull request, closes it with its comment and reopens it, in that order, and
// leaves it open at the same head with the pull_request reopened run's checks linked to it; a pull request that moved
// changes nothing, one not open is refused, and a reopen GitHub refuses is an error saying the pull request is left
// closed, with gh's reason.
func TestGitHubStartCI(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	s := h.Forge.(forge.CIStarter)
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "t7", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	r := forge.Retrigger{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: h.Heads[0], Comment: "Guildhall: no CI ran for this head"}
	repo := "github.com/acme/widgets"
	view := []string{"pr", "view", "1", "--repo", repo, "--json", "number,url,state,headRefName,headRefOid,mergeCommit"}
	closeCall := []string{"pr", "close", "1", "--repo", repo, "--comment", r.Comment}
	reopen := []string{"pr", "reopen", "1", "--repo", repo}
	since := func(n int) [][]string { return g.readState(t).Calls[n:] }
	calls := func() int { return len(g.readState(t).Calls) }

	// A reopen GitHub refuses leaves the pull request closed, and the error says so with gh's reason.
	st := g.readState(t)
	st.ReopenRefuse = "the head repository was deleted"
	g.writeState(t, st)
	n := calls()
	_, err = s.StartCI(ctx, r)
	if err == nil || !strings.Contains(err.Error(), "is left closed, reopen it") || !strings.Contains(err.Error(), "the head repository was deleted") {
		t.Fatalf("StartCI with a refused reopen = %v, want it to say the pull request is left closed, with gh's reason", err)
	}
	if got := since(n); !slices.EqualFunc(got, [][]string{view, closeCall, reopen, view}, slices.Equal) {
		t.Fatalf("gh calls = %q, want view, close, reopen, view", got)
	}
	// Closed now, it is refused and nothing is closed or reopened.
	st = g.readState(t)
	st.ReopenRefuse = ""
	g.writeState(t, st)
	n = calls()
	if _, err := s.StartCI(ctx, r); !errors.Is(err, forge.ErrRefused) {
		t.Fatalf("StartCI of a closed pull request = %v, want ErrRefused", err)
	}
	if got := since(n); !slices.EqualFunc(got, [][]string{view}, slices.Equal) {
		t.Fatalf("gh calls = %q, want only the view", got)
	}
	st = g.readState(t)
	st.PRs[0].State = "OPEN"
	st.ReopenChecks = []map[string]any{{"__typename": "CheckRun", "id": "run-pr", "name": "test", "status": "QUEUED", "conclusion": nil}}
	g.writeState(t, st)

	n = calls()
	if got, err := s.StartCI(ctx, r); got != forge.Started || err != nil {
		t.Fatalf("StartCI = %q, %v, want started", got, err)
	}
	if got := since(n); !slices.EqualFunc(got, [][]string{view, closeCall, reopen, view}, slices.Equal) {
		t.Fatalf("gh calls = %q, want view, close, reopen, view, in order", got)
	}
	st = g.readState(t)
	if p := st.PRs[0]; p.State != "OPEN" || p.Reopens != 1 || remoteBranch(g.bare, p.Branch) != h.Heads[0] {
		t.Fatalf("pull request after StartCI = %+v, want it open at the same head", p)
	}
	bound := forge.Commit{Repository: h.Repository, Remote: h.Remote, Head: h.Heads[0], PullRequest: pr.Number}
	if got, err := h.Forge.Checks(ctx, bound); err != nil || len(got) != 1 || got[0].ID != "run-pr" || got[0].State != forge.CheckPending {
		t.Fatalf("the pull request's checks after StartCI = %+v, %v; want the reopened run", got, err)
	}

	// The pull request moved on: nothing is closed or reopened.
	if _, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: h.Heads[1], Title: "t7", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	n = calls()
	if got, err := s.StartCI(ctx, r); got != forge.HeadMoved || err != nil {
		t.Fatalf("StartCI of a head the pull request left = %q, %v, want head-moved", got, err)
	}
	if got := since(n); !slices.EqualFunc(got, [][]string{view}, slices.Equal) {
		t.Fatalf("gh calls = %q, want only the view", got)
	}
}

// TestGitHubChecksBoundToPullRequest is d176's regression on the forge: a workflow_dispatch run on a pull request's
// head is in the commit's rollup but not the pull request's, so a read bound to the pull request reports no check,
// as gh pr checks and the pull request's required checks do, and a read of a head the pull request left reports none.
func TestGitHubChecksBoundToPullRequest(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "t7", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	st := g.readState(t)
	st.Unlinked = map[string][]map[string]any{h.Heads[0]: {{"__typename": "CheckRun", "id": "run-dispatch", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS"}}}
	g.writeState(t, st)
	commit := forge.Commit{Repository: h.Repository, Remote: h.Remote, Head: h.Heads[0]}
	if got, err := h.Forge.Checks(ctx, commit); err != nil || len(got) != 1 || got[0].ID != "run-dispatch" {
		t.Fatalf("the commit's checks = %+v, %v; want the dispatch run", got, err)
	}
	bound := commit
	bound.PullRequest = pr.Number
	n := len(g.readState(t).Calls)
	if got, err := h.Forge.Checks(ctx, bound); err != nil || len(got) != 0 {
		t.Fatalf("the pull request's checks = %+v, %v; want none, as gh pr checks reports", got, err)
	}
	call := g.readState(t).Calls[n]
	if !slices.Contains(call, "-F") || !slices.Contains(call, "number=1") || slices.ContainsFunc(call, func(a string) bool { return strings.HasPrefix(a, "oid=") }) {
		t.Fatalf("bound read = %q, want the pull request query by number", call)
	}
	st = g.readState(t)
	st.Checks = map[string][]map[string]any{h.Heads[0]: {{"__typename": "CheckRun", "id": "run-pr", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS"}}}
	g.writeState(t, st)
	if got, err := h.Forge.Checks(ctx, bound); err != nil || len(got) != 1 || got[0].ID != "run-pr" {
		t.Fatalf("the pull request's checks = %+v, %v; want its own run only", got, err)
	}
	// Review 2's s1: a pull request at the head whose last listed commit is another, as GitHub's date order can make
	// it after a rebase, is an error naming both, not a read of no checks that would wait for ever.
	st = g.readState(t)
	st.LastCommit = map[string]string{"1": h.Heads[1]}
	g.writeState(t, st)
	if _, err := h.Forge.Checks(ctx, bound); err == nil || !strings.Contains(err.Error(), "at head "+h.Heads[0]) || !strings.Contains(err.Error(), h.Heads[1]) {
		t.Fatalf("checks with another last commit = %v, want an error naming both", err)
	}
	st = g.readState(t)
	st.LastCommit = nil
	g.writeState(t, st)
	if _, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: h.Heads[1], Title: "t7", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if got, err := h.Forge.Checks(ctx, bound); err != nil || len(got) != 0 {
		t.Fatalf("the pull request's checks of a head it left = %+v, %v; want none", got, err)
	}
	bound.PullRequest = 9
	if _, err := h.Forge.Checks(ctx, bound); err == nil || !strings.Contains(err.Error(), "no pull request 9") {
		t.Fatalf("checks of a missing pull request = %v, want an error naming it", err)
	}
}
