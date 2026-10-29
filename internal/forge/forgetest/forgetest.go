// Package forgetest holds the shared forge contract suite and Fake, the in-memory forge, for tests in other
// packages (docs/contracts/forge-forgetest.md).
package forgetest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
)

// Forge is the operations under the contract: forge.GitHub's, or a double's.
type Forge interface {
	Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error)
	Merge(ctx context.Context, m forge.Merge) (forge.PullRequest, error)
	Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error)
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
	// SetChecks makes checks the checks the forge reports for a pushed head; nil skips the cases that need checks.
	SetChecks func(t *testing.T, head string, checks []forge.Check)
	// Text returns the title and body pull request number reads; nil skips the cases that read them.
	Text func(t *testing.T, number int) (title, body string)
	// Edits returns how many times the forge was asked to edit an open pull request's title or body; nil skips
	// counting them.
	Edits func(t *testing.T) int
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

	t.Run("sets the title and body on a new and on the open pull request", func(t *testing.T) {
		h := newHarness(t)
		if h.Text == nil {
			t.Skip("this forge cannot read a pull request's text")
		}
		text := func(t *testing.T, head, title, body string, edits int) forge.PullRequest {
			t.Helper()
			c := change(h, head)
			c.Title, c.Body = title, body
			pr, err := h.Forge.Publish(ctx, c)
			if err != nil {
				t.Fatalf("Publish(%s) = %v", head, err)
			}
			if gotTitle, gotBody := h.Text(t, pr.Number); gotTitle != title || gotBody != body {
				t.Errorf("pull request %d reads %q, %q, want %q, %q", pr.Number, gotTitle, gotBody, title, body)
			}
			if h.Edits != nil {
				if got := h.Edits(t); got != edits {
					t.Errorf("edits = %d, want %d", got, edits)
				}
			}
			return pr
		}
		pr := text(t, h.Heads[0], "Title A", "Body A", 0)
		if got := text(t, h.Heads[1], "Title B", "Body B", 1); got.Number != pr.Number {
			t.Errorf("Publish with new text = %+v, want pull request %d", got, pr.Number)
		}
		text(t, h.Heads[1], "Title B", "Body B", 1)
		text(t, h.Heads[1], "Title B", "Body C", 2)
	})

	t.Run("refuses a title or body over the forge's limits", func(t *testing.T) {
		h := newHarness(t)
		for _, edit := range []func(*forge.Change){
			func(c *forge.Change) { c.Title = strings.Repeat("é", forge.MaxTitle+1) },
			func(c *forge.Change) { c.Body = strings.Repeat("x", forge.MaxBody+1) },
		} {
			c := change(h, h.Heads[0])
			edit(&c)
			if _, err := h.Forge.Publish(ctx, c); !errors.Is(err, forge.ErrInvalid) {
				t.Errorf("Publish of a %d-byte title and %d-byte body = %v, want ErrInvalid", len(c.Title), len(c.Body), err)
			}
		}
		c := change(h, h.Heads[0])
		c.Title = strings.Repeat("é", forge.MaxTitle)
		if err := c.Validate(); err != nil {
			t.Errorf("Validate of a %d-character title = %v", forge.MaxTitle, err)
		}
		if got := h.Branch(t, "guildhall/t7"); got != "" {
			t.Errorf("branch guildhall/t7 is at %q, want none", got)
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

	commit := func(h Harness, head string) forge.Commit {
		return forge.Commit{Repository: h.Repository, Remote: h.Remote, Head: head}
	}
	checks := func(t *testing.T, h Harness, head string) []forge.Check {
		t.Helper()
		got, err := h.Forge.Checks(ctx, commit(h, head))
		if err != nil {
			t.Fatalf("Checks(%s) = %v", head, err)
		}
		t.Logf("Checks(%s) = %+v", head, got)
		return got
	}

	t.Run("reports no checks for a head nothing checked", func(t *testing.T) {
		h := newHarness(t)
		publish(t, h, h.Heads[0])
		if got := checks(t, h, h.Heads[0]); len(got) != 0 {
			t.Errorf("Checks = %+v, want none", got)
		}
	})

	t.Run("reports every check of the head with its run id sorted by name", func(t *testing.T) {
		h := newHarness(t)
		if h.SetChecks == nil {
			t.Skip("this forge cannot be given checks")
		}
		publish(t, h, h.Heads[0])
		h.SetChecks(t, h.Heads[0], []forge.Check{
			{Name: "test", State: forge.CheckPending, URL: "https://ci.invalid/test", ID: "run-3"},
			{Name: "build", State: forge.CheckFailed, URL: "https://ci.invalid/build", ID: "run-1"},
			{Name: "lint", State: forge.CheckPassed, ID: "run-2"},
		})
		want := []forge.Check{
			{Name: "build", State: forge.CheckFailed, URL: "https://ci.invalid/build", ID: "run-1"},
			{Name: "lint", State: forge.CheckPassed, ID: "run-2"},
			{Name: "test", State: forge.CheckPending, URL: "https://ci.invalid/test", ID: "run-3"},
		}
		if got := checks(t, h, h.Heads[0]); !slices.Equal(got, want) {
			t.Errorf("Checks = %+v, want %+v", got, want)
		}
	})

	t.Run("reports a re-run check as its new run", func(t *testing.T) {
		h := newHarness(t)
		if h.SetChecks == nil {
			t.Skip("this forge cannot be given checks")
		}
		publish(t, h, h.Heads[0])
		h.SetChecks(t, h.Heads[0], []forge.Check{{Name: "ci", State: forge.CheckFailed, ID: "run-1"}})
		checks(t, h, h.Heads[0])
		h.SetChecks(t, h.Heads[0], []forge.Check{{Name: "ci", State: forge.CheckFailed, ID: "run-2"}})
		if got := checks(t, h, h.Heads[0]); !slices.Equal(got, []forge.Check{{Name: "ci", State: forge.CheckFailed, ID: "run-2"}}) {
			t.Errorf("Checks after a re-run = %+v, want the new run", got)
		}
	})

	t.Run("reads the checks of the head asked for, not of the pull request", func(t *testing.T) {
		h := newHarness(t)
		if h.SetChecks == nil {
			t.Skip("this forge cannot be given checks")
		}
		publish(t, h, h.Heads[0])
		h.SetChecks(t, h.Heads[0], []forge.Check{{Name: "ci", State: forge.CheckFailed}})
		publish(t, h, h.Heads[1])
		h.SetChecks(t, h.Heads[1], []forge.Check{{Name: "ci", State: forge.CheckPassed}})
		if got := checks(t, h, h.Heads[0]); !slices.Equal(got, []forge.Check{{Name: "ci", State: forge.CheckFailed}}) {
			t.Errorf("Checks of the earlier head = %+v, want its failure", got)
		}
		if got := checks(t, h, h.Heads[1]); !slices.Equal(got, []forge.Check{{Name: "ci", State: forge.CheckPassed}}) {
			t.Errorf("Checks of the published head = %+v, want its pass", got)
		}
	})

	t.Run("refuses a head the remote does not hold and an invalid commit", func(t *testing.T) {
		h := newHarness(t)
		publish(t, h, h.Heads[0])
		if _, err := h.Forge.Checks(ctx, commit(h, h.Heads[2])); !errors.Is(err, forge.ErrUnknownHead) {
			t.Errorf("Checks of an unpushed head = %v, want ErrUnknownHead", err)
		}
		for _, c := range []forge.Commit{commit(h, "HEAD"), {Repository: h.Repository, Head: h.Heads[0]}, {Remote: h.Remote, Head: h.Heads[0]}} {
			if _, err := h.Forge.Checks(ctx, c); !errors.Is(err, forge.ErrInvalid) {
				t.Errorf("Checks(%+v) = %v, want ErrInvalid", c, err)
			}
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
