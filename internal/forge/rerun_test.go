package forge_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
)

// TestGitHubRerunJob: RerunJob posts to the job's rerun endpoint on the remote's host, and GitHub's refusal, such as
// its 403 for a workflow run still in progress, or gh not signed in, is an error carrying gh's reason and not
// ErrInvalid.
func TestGitHubRerunJob(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	rerunner := h.Forge.(interface {
		RerunJob(ctx context.Context, c forge.Commit, job string) error
	})
	if _, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "t7", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	h.SetChecks(t, h.Heads[0], []forge.Check{{Name: "test", State: forge.CheckFailed, ID: "run-1", Job: "4242"}})
	c := forge.Commit{Repository: h.Repository, Remote: h.Remote, Head: h.Heads[0]}

	s := g.readState(t)
	s.RerunStatus = 403
	g.writeState(t, s)
	err := rerunner.RerunJob(ctx, c, "4242")
	if err == nil || errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "already running (HTTP 403)") {
		t.Fatalf("refused RerunJob = %v, want gh's reason", err)
	}
	s = g.readState(t)
	s.RerunStatus, s.SignedOut = 0, true
	g.writeState(t, s)
	if err := rerunner.RerunJob(ctx, c, "4242"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("signed-out RerunJob = %v, want gh's reason", err)
	}
	s = g.readState(t)
	s.SignedOut = false
	g.writeState(t, s)
	if err := rerunner.RerunJob(ctx, c, "4242"); err != nil {
		t.Fatalf("RerunJob = %v", err)
	}
	want := []string{"api", "repos/acme/widgets/actions/jobs/4242/rerun", "--method", "POST", "--hostname", "github.com"}
	calls := g.readState(t).Calls
	if !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, want) }) {
		t.Errorf("gh calls = %q, want %q", calls, want)
	}
}
