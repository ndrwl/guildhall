package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/policies"
	"github.com/ndrwl/guildhall/internal/render"
)

// PolicyGit fetches each repository's default branch into policies.Ref and reads the policy file there
// (docs/configuration.md#repository-policies); git.Program is the real one.
type PolicyGit interface {
	// FetchDefault records the commit remote's default branch is at as ref in the repository at repo.
	FetchDefault(ctx context.Context, repo, remote, ref string) error
	policies.Git
}

var _ PolicyGit = git.Program{}

// policyGitTimeout bounds each git run that fetches or reads a repository's policies.
const policyGitTimeout = 30 * time.Second

// policyGit is the App's PolicyGit; nil is git.Program bounded by policyGitTimeout.
func (a *App) policyGit() PolicyGit {
	if a.PolicyGit != nil {
		return a.PolicyGit
	}
	return git.Program{Timeout: policyGitTimeout}
}

// refreshPolicies fetches each repository's default branch into policies.Ref, at director start, at reload and on
// each advance pass; an unreachable remote leaves the ref as it was.
func (a *App) refreshPolicies(ctx context.Context, repos ...config.Repository) {
	g := a.policyGit()
	for _, r := range repos {
		_ = g.FetchDefault(ctx, r.Path, r.Remote, policies.Ref)
	}
}

// repositoryPolicies is one repository's policies as read.
type repositoryPolicies struct {
	id string
	policies.Read
}

// readPolicies reads each repository's policies from policies.Ref, with no fetch.
func (a *App) readPolicies(ctx context.Context, repos ...config.Repository) []repositoryPolicies {
	g := a.policyGit()
	out := make([]repositoryPolicies, 0, len(repos))
	for _, r := range repos {
		out = append(out, repositoryPolicies{id: r.ID, Read: policies.Load(ctx, g, r.Path)})
	}
	return out
}

// startupPolicies are startup.md's policy lines: "<repo>: <policy>" for each policy, and "<repo>: policies cannot
// be read (<reason>)" for a policy file that is refused or cannot be read. A repository with no file, or whose
// ref is not read yet, has none.
func startupPolicies(reads []repositoryPolicies) []string {
	var lines []string
	for _, r := range reads {
		if r.Err != nil {
			lines = append(lines, fmt.Sprintf("%s: policies cannot be read (%s)", r.id, oneLine(r.Err.Error())))
			continue
		}
		for _, p := range r.Policies {
			lines = append(lines, r.id+": "+oneLine(p))
		}
	}
	return lines
}

// statusPolicies is one entry of the status feed's repository_policies: the repository's policies and the commit
// they were read at, why its policy file cannot be read, or that its default branch is not read yet.
type statusPolicies struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit,omitempty"`
	// Policies is empty, not absent, for a repository without a policy file.
	Policies   []string `json:"policies,omitzero"`
	Error      string   `json:"error,omitempty"`
	NotReadYet bool     `json:"not_read_yet,omitempty"`
}

// feedPolicies is the status feed's repository_policies, one entry per repository; none when no repository has a
// policy file and none has an error.
func feedPolicies(reads []repositoryPolicies) []statusPolicies {
	var out []statusPolicies
	shown := false
	for _, r := range reads {
		e := statusPolicies{Repository: r.id}
		switch {
		case r.NotRead:
			e.NotReadYet = true
		case r.Err != nil:
			e.Error, shown = oneLine(r.Err.Error()), true
		default:
			e.Commit, e.Policies = r.Commit, append([]string{}, r.Policies...)
			shown = shown || len(r.Policies) > 0
		}
		out = append(out, e)
	}
	if !shown {
		return nil
	}
	return out
}

// promptPolicies fills b's policies for the repository id from cfg, read from policies.Ref with no fetch: a policy
// file that is refused or cannot be read is b's PolicyError, which does not stop rendering, and a repository whose
// ref is not read yet has none.
func (a *App) promptPolicies(cfg *config.Config, id string, b *render.Baseline) {
	repo, err := cfg.ListedRepository(id)
	if err != nil {
		return
	}
	r := a.readPolicies(context.Background(), repo)[0]
	if r.Err != nil {
		b.PolicyError = oneLine(r.Err.Error())
		return
	}
	b.Policies = r.Policies
}
