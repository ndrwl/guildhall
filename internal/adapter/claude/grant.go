package claude

import (
	"path/filepath"
	"slices"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
)

// artisanReadOnlyGit are the read-only git rules every well-known profile's shipped default grants.
var artisanReadOnlyGit = []string{"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git rev-list:*)", "Bash(git merge-base:*)", "Bash(git ls-files:*)"}

// ShippedDefaultGrants are the allowed_tools this release gives a claude profile with a well-known name that sets
// none (docs/decisions/0093-shipped-default-grants.md): read-only git for each, the git a build needs to commit and
// rebase for builder, the same without git switch for rebaser, guildhall review check for reviewer, so it can check
// its outcome as intake will before it finishes, and web search and fetch for researcher. They grant neither git
// checkout nor git push, nor any toolchain command; a repository grant adds those a repository needs, such as
// SuggestedGoAllowedTools. Every config.ShippedProfiles name has an entry.
var ShippedDefaultGrants = map[string][]string{
	"builder":    append(slices.Clone(artisanReadOnlyGit), "Bash(git add:*)", "Bash(git commit:*)", "Bash(git fetch:*)", "Bash(git rebase:*)", "Bash(git switch:*)"),
	"rebaser":    append(slices.Clone(artisanReadOnlyGit), "Bash(git add:*)", "Bash(git commit:*)", "Bash(git fetch:*)", "Bash(git rebase:*)"),
	"planner":    slices.Clone(artisanReadOnlyGit),
	"reviewer":   append(slices.Clone(artisanReadOnlyGit), "Bash(guildhall review check:*)"),
	"researcher": append(slices.Clone(artisanReadOnlyGit), "WebSearch", "WebFetch"),
}

// SuggestedGoAllowedTools is the documented repository grant for a Go repository's builder profile
// (docs/adapters.md#artisan-permissions): its checks. The builder's shipped default already grants its git.
var SuggestedGoAllowedTools = []string{"Bash(go test:*)", "Bash(go vet:*)"}

// GrantSource is where a rule of an effective grant came from.
type GrantSource string

// The sources of an effective grant's rules.
const (
	// SourceDefault is the profile's shipped default grant, from ShippedDefaultGrants.
	SourceDefault GrantSource = "default"
	// SourceCopy is the profile's own allowed_tools in the Director copy's config.
	SourceCopy GrantSource = "copy"
	// SourceRepository is the repository grant of the task's repository.
	SourceRepository GrantSource = "repository"
)

// Grant is one rule of an effective grant and its source.
type Grant struct {
	Rule   string
	Source GrantSource
}

// EffectiveGrant is the effective grant cfg gives profile for a task of repository, in order: the profile's own
// allowed_tools when it sets them, even to an empty list, or else its shipped default when it runs the claude
// adapter; then repository's grant for the profile, if any. A rule already granted is dropped, keeping the first.
// An empty repository, or one cfg does not list, adds nothing; a profile cfg does not declare is an error.
func EffectiveGrant(cfg *config.Config, profile, repository string) ([]Grant, error) {
	p, err := cfg.Profile(profile)
	if err != nil {
		return nil, err
	}
	var grants []Grant
	add := func(rules []string, source GrantSource) {
		for _, rule := range rules {
			if !slices.ContainsFunc(grants, func(g Grant) bool { return g.Rule == rule }) {
				grants = append(grants, Grant{rule, source})
			}
		}
	}
	switch {
	case p.AllowedToolsSet():
		add(p.AllowedToolsList(), SourceCopy)
	case p.Adapter == adapter.ClaudeName:
		add(ShippedDefaultGrants[profile], SourceDefault)
	}
	if r, ok := cfg.Repository(repository); ok && repository != "" {
		add(r.Profiles[profile].AllowedTools, SourceRepository)
	}
	return grants, nil
}

// GrantRules are the rules of grants, in order.
func GrantRules(grants []Grant) []string {
	var rules []string
	for _, g := range grants {
		rules = append(rules, g.Rule)
	}
	return rules
}

// ProfileTools are the rules of the effective grant the Director copy at copyDir gives profile for a task of
// repository, loaded from the copy's config.toml; with no copy or no profile there is no grant, and nothing is
// loaded. Only references travel to the wrapper, never a rule list.
func ProfileTools(copyDir, profile, repository string) ([]string, error) {
	if copyDir == "" || profile == "" {
		return nil, nil
	}
	cfg, err := config.Load(filepath.Join(copyDir, "config.toml"))
	if err != nil {
		return nil, err
	}
	grants, err := EffectiveGrant(cfg, profile, repository)
	if err != nil {
		return nil, err
	}
	return GrantRules(grants), nil
}
