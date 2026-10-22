// Package forgetest holds the shared forge contract suite and Fake, the in-memory forge, for tests in other
// packages (docs/contracts/forge-forgetest.md).
package forgetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
)

// Forge is the operations under the contract: forge.GitHub's, or a double's.
type Forge interface {
	Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error)
	Merge(ctx context.Context, m forge.Merge) (forge.PullRequest, error)
}

// Harness is one forge under the contract suite, over a repository whose remote's default branch is Base, at
// BaseHead.
type Harness struct {
	Forge              Forge
	Repository, Remote string
	Base, BaseHead     string
	// Heads are commits of Repository: Heads[0] and Heads[2] are children of BaseHead, and Heads[1] a child of
	// Heads[0], so publishing Heads[2] after Heads[1] rewrites the branch.
	Heads [3]string
	// Branch returns the commit a branch of Remote is at, or "" when it has none.
	Branch func(t *testing.T, name string) string
	// Refuse makes the forge refuse every later merge with reason; nil skips the refusal case.
	Refuse func(t *testing.T, reason string)
}

// RunForgeContract runs the forge contract against fresh harnesses from newHarness.
func RunForgeContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	change := func(h Harness, head string) forge.Change {
		return forge.Change{Repository: h.Repository, Remote: h.Remote, Branch: "guildhall/t7", Head: head, Title: "Contract task", Body: "Published by the forge contract."}
	}
	publish := func(t *testing.T, h Harness, head string) forge.PullRequest {
		t.Helper()
		pr, err := h.Forge.Publish(ctx, change(h, head))
		if err != nil {
			t.Fatalf("Publish(%s) = %v", head, err)
		}
		t.Logf("Publish(%s) = %+v", head, pr)
		if pr.Number < 1 || pr.URL == "" || pr.Branch != "guildhall/t7" || pr.Head != head || pr.State != forge.Open || pr.MergeCommit != "" {
			t.Fatalf("Publish(%s) = %+v, want an open pull request of guildhall/t7 at the head", head, pr)
		}
		if got := h.Branch(t, "guildhall/t7"); got != head {
			t.Fatalf("branch guildhall/t7 is at %q, want %s", got, head)
		}
		return pr
	}
	merge := func(h Harness, pr forge.PullRequest, head string) (forge.PullRequest, error) {
		return h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: pr.Number, Head: head})
	}

	t.Run("opens a pull request for the pushed head and replays it", func(t *testing.T) {
		h := newHarness(t)
		pr := publish(t, h, h.Heads[0])
		if again := publish(t, h, h.Heads[0]); again != pr {
			t.Errorf("repeated Publish = %+v, want %+v", again, pr)
		}
		if got := h.Branch(t, h.Base); got != h.BaseHead {
			t.Errorf("base %s moved to %q", h.Base, got)
		}
	})

	t.Run("updates the open pull request with a new and a rewritten head", func(t *testing.T) {
		h := newHarness(t)
		pr := publish(t, h, h.Heads[0])
		for _, head := range []string{h.Heads[1], h.Heads[2]} {
			if got := publish(t, h, head); got.Number != pr.Number || got.URL != pr.URL {
				t.Errorf("Publish(%s) = %+v, want pull request %d updated", head, got, pr.Number)
			}
		}
	})

	t.Run("refuses an unknown head and pushes nothing", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.Forge.Publish(ctx, change(h, strings.Repeat("f", 40)))
		if !errors.Is(err, forge.ErrUnknownHead) {
			t.Errorf("Publish of an unknown head = %v, want ErrUnknownHead", err)
		}
		if got := h.Branch(t, "guildhall/t7"); got != "" {
			t.Errorf("branch guildhall/t7 is at %q, want none", got)
		}
	})

	t.Run("refuses the base branch and invalid changes", func(t *testing.T) {
		h := newHarness(t)
		c := change(h, h.Heads[0])
		c.Branch = h.Base
		bad := []forge.Change{c}
		for _, edit := range []func(*forge.Change){
			func(c *forge.Change) { c.Branch = "guildhall/../t7" },
			func(c *forge.Change) { c.Head = "HEAD" },
			func(c *forge.Change) { c.Remote = "" },
			func(c *forge.Change) { c.Title = " " },
		} {
			c := change(h, h.Heads[0])
			edit(&c)
			bad = append(bad, c)
		}
		for _, c := range bad {
			if _, err := h.Forge.Publish(ctx, c); !errors.Is(err, forge.ErrInvalid) {
				t.Errorf("Publish(%+v) = %v, want ErrInvalid", c, err)
			}
		}
		if got := h.Branch(t, h.Base); got != h.BaseHead {
			t.Errorf("base %s moved to %q", h.Base, got)
		}
		if got := h.Branch(t, "guildhall/t7"); got != "" {
			t.Errorf("branch guildhall/t7 is at %q, want none", got)
		}
	})

	t.Run("merges only at the approved head and replays the merge", func(t *testing.T) {
		h := newHarness(t)
		pr := publish(t, h, h.Heads[1])
		if _, err := merge(h, pr, h.Heads[0]); !errors.Is(err, forge.ErrRefused) {
			t.Fatalf("Merge at another head = %v, want ErrRefused", err)
		}
		got, err := merge(h, pr, h.Heads[1])
		if err != nil {
			t.Fatalf("Merge = %v", err)
		}
		t.Logf("Merge = %+v", got)
		if got.Number != pr.Number || got.URL != pr.URL || got.State != forge.Merged || got.Head != h.Heads[1] || !digest.ValidHead(got.MergeCommit) {
			t.Fatalf("Merge = %+v, want pull request %d merged at %s with a merge commit", got, pr.Number, h.Heads[1])
		}
		again, err := merge(h, pr, h.Heads[1])
		if err != nil || again != got {
			t.Errorf("repeated Merge = %+v, %v, want %+v", again, err, got)
		}
		if _, err := merge(h, pr, h.Heads[0]); !errors.Is(err, forge.ErrRefused) {
			t.Errorf("Merge of a pull request merged at another head = %v, want ErrRefused", err)
		}
		if _, err := h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: h.Remote, Number: 0, Head: h.Heads[1]}); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("Merge of pull request 0 = %v, want ErrInvalid", err)
		}
	})

	t.Run("reports a forge refusal with its reason", func(t *testing.T) {
		h := newHarness(t)
		if h.Refuse == nil {
			t.Skip("this forge cannot be made to refuse")
		}
		pr := publish(t, h, h.Heads[0])
		const reason = `Required status check "ci" is failing`
		h.Refuse(t, reason)
		_, err := merge(h, pr, h.Heads[0])
		if !errors.Is(err, forge.ErrRefused) || !strings.Contains(err.Error(), reason) {
			t.Fatalf("Merge = %v, want ErrRefused with %q", err, reason)
		}
		if again := publish(t, h, h.Heads[0]); again.State != forge.Open {
			t.Errorf("after a refusal the pull request is %+v, want open", again)
		}
	})
}
