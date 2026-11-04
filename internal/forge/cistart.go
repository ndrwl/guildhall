package forge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// StartOutcome is what StartCI did for a head with no CI run.
type StartOutcome string

// The outcomes of StartCI: Started closed and reopened the pull request, whose pull_request reopened event runs its
// CI on the same head; HeadMoved found the pull request no longer at the head and changed nothing.
const (
	Started   StartOutcome = "started"
	HeadMoved StartOutcome = "head-moved"
)

// Retrigger names the open pull request whose head the forge ran no CI for, and that head.
type Retrigger struct {
	// Repository and Remote are as in Change.
	Repository, Remote string
	// Number is the pull request's number on the forge.
	Number int
	// Head is the full commit id the pull request should be at.
	Head string
	// Comment is posted on the pull request as it is closed, saying why.
	Comment string
}

// Validate reports a retrigger missing a field or naming an invalid head, as ErrInvalid.
func (r Retrigger) Validate() error {
	if err := (View{Repository: r.Repository, Remote: r.Remote, Number: r.Number}).Validate(); err != nil {
		return err
	}
	if r.Comment == "" {
		return fmt.Errorf("%w: a retrigger posts a comment", ErrInvalid)
	}
	return (Commit{Repository: r.Repository, Remote: r.Remote, Head: r.Head}).Validate()
}

// CIStarter starts the CI of a pull request's head the forge ran none for, such as when GitHub drops a pull request's
// synchronize event; it is optional on a forge. Validate and the rebased merge-queue front call it at most once per
// head, once ci_start_grace has passed with no check reported (docs/interpreter.md#absent-checks). The run it starts
// must be one the pull request's own checks report, since only those satisfy its required checks: on GitHub a
// workflow_dispatch run on the head is not. GitHub and forgetest.Fake implement it; a forge without it starts
// nothing, and the Director is told.
type CIStarter interface {
	StartCI(ctx context.Context, r Retrigger) (StartOutcome, error)
}

var _ CIStarter = GitHub{}

// StartCI re-triggers the pull request CI of r.Head by closing pull request r.Number with r.Comment and reopening it:
// GitHub's pull_request reopened event runs the repository's pull request workflows on the same head, linked to the
// pull request, and the head, its review bindings and the branch are unchanged. It first views the pull request: one
// not open is ErrRefused, and one not at r.Head is HeadMoved with nothing changed. A close or reopen gh fails is
// checked by viewing the pull request again, as Close does; a pull request the reopen leaves closed is an error that
// says so, for the Director to reopen it. Any gh failure carries gh's reason. A cancelled ctx is returned as it is.
func (g GitHub) StartCI(ctx context.Context, r Retrigger) (StartOutcome, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	repo, err := GitHubRepository(r.Remote)
	if err != nil {
		return "", err
	}
	pr, err := g.view(ctx, r.Repository, repo, r.Number)
	if err != nil {
		return "", g.reason(ctx, "viewing pull request "+strconv.Itoa(r.Number), err)
	}
	switch {
	case pr.State != Open:
		return "", fmt.Errorf("%w: pull request %s is %s", ErrRefused, pr.URL, pr.State)
	case pr.Head != r.Head:
		return HeadMoved, nil
	}
	n := strconv.Itoa(r.Number)
	if _, err := g.gh(ctx, r.Repository, "pr", "close", n, "--repo", repo, "--comment", r.Comment); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if now, verr := g.view(ctx, r.Repository, repo, r.Number); verr != nil || now.State != Closed {
			return "", g.reason(ctx, "closing pull request "+pr.URL, err)
		}
	}
	_, reopenErr := g.gh(ctx, r.Repository, "pr", "reopen", n, "--repo", repo)
	if reopenErr != nil && ctx.Err() != nil {
		return "", fmt.Errorf("pull request %s is left closed, reopen it: %w", pr.URL, ctx.Err())
	}
	now, err := g.view(ctx, r.Repository, repo, r.Number)
	switch {
	case err == nil && now.State == Open:
		return Started, nil
	case reopenErr != nil:
		return "", fmt.Errorf("pull request %s is left closed, reopen it: %w", pr.URL, g.reason(ctx, "reopening it", reopenErr))
	case err != nil:
		return "", fmt.Errorf("pull request %s may be left closed, check it: %w", pr.URL, g.reason(ctx, "viewing it", err))
	}
	return "", fmt.Errorf("pull request %s is %s after the reopen, reopen it", pr.URL, now.State)
}

// reason is a gh failure while doing what, with gh's own output as its reason; a cancelled ctx is returned as it is.
func (g GitHub) reason(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if run, ok := errors.AsType[*runError](err); ok && run.output != "" {
		return fmt.Errorf("%s: %s", what, run.output)
	}
	return fmt.Errorf("%s: %w", what, err)
}
