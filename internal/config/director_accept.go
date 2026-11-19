package config

import (
	"fmt"
	"slices"
)

// DirectorAccept is a repository's Director acceptance threshold as it applies (docs/configuration.md#repositories):
// the plan sizes, in non-test lines and files, at or under which the Director may accept a small feature's plan and
// tell the Guildmaster rather than ask them. Lines and Files are the repository's director_accept_lines and
// director_accept_files, each capped by the repository's own size budget, BudgetLines and BudgetFiles, where that
// half of the budget is non-zero.
type DirectorAccept struct {
	// Lines and Files are the effective thresholds; On is false unless both are non-zero.
	Lines, Files int
	// SetLines and SetFiles are the keys as written.
	SetLines, SetFiles int
	// BudgetLines and BudgetFiles are the quality size budget that capped them, 0 where that half is off.
	BudgetLines, BudgetFiles int
}

// On reports whether Director acceptance is turned on: both thresholds are non-zero.
func (a DirectorAccept) On() bool { return a.Lines > 0 && a.Files > 0 }

// DirectorAccept returns repository's Director acceptance threshold under c. c must be the copy's config, never a
// task's effective config (WithTaskOverrides): the cap is the repository's own, local or managed, size budget, so a
// task override that raises the budget never raises the threshold. A repository c does not configure, and a
// quality budget that does not parse, are errors.
func (c *Config) DirectorAccept(repository string) (DirectorAccept, error) {
	i := slices.IndexFunc(c.Repositories, func(r Repository) bool { return r.ID == repository })
	if i < 0 {
		return DirectorAccept{}, fmt.Errorf("repository %s is not configured", repository)
	}
	r := c.Repositories[i]
	a := DirectorAccept{Lines: r.DirectorAcceptLines, Files: r.DirectorAcceptFiles, SetLines: r.DirectorAcceptLines, SetFiles: r.DirectorAcceptFiles}
	if !a.On() {
		return DirectorAccept{SetLines: a.SetLines, SetFiles: a.SetFiles}, nil
	}
	q, err := c.Quality()
	if err != nil {
		return DirectorAccept{}, err
	}
	a.BudgetLines, a.BudgetFiles = q.SizeBudgetLines, q.SizeBudgetFiles
	if a.BudgetLines > 0 {
		a.Lines = min(a.Lines, a.BudgetLines)
	}
	if a.BudgetFiles > 0 {
		a.Files = min(a.Files, a.BudgetFiles)
	}
	return a, nil
}

// AcceptRequiresGuildmasterOverride is the task override that keeps one task's plan acceptance with the Guildmaster
// whatever the repository's Director acceptance threshold (docs/configuration.md#setting-scopes). Like a merge
// requirement it is stricter only: true applies, and false or any other value is refused. It is not a setting, so
// WithTaskOverrides leaves it out of Local; AcceptRequiresGuildmaster reads it.
const AcceptRequiresGuildmasterOverride = "accept.requires_guildmaster"

// acceptOverrideRefusal reports why value cannot be the accept.requires_guildmaster override, or nil.
func acceptOverrideRefusal(value string) error {
	switch value {
	case "true":
		return nil
	case "false":
		return fmt.Errorf("`%s`: a task override may only keep plan acceptance with the Guildmaster; it cannot be false", AcceptRequiresGuildmasterOverride)
	}
	return fmt.Errorf("`%s` = %q is not a boolean; a task override may only set it to true", AcceptRequiresGuildmasterOverride, value)
}

// AcceptRequiresGuildmaster reports whether a task's overrides in effect, key to value, keep its plan acceptance with
// the Guildmaster: accept.requires_guildmaster is true. Any other recorded value does not apply, and is skipped with
// its reason by WithTaskOverrides.
func AcceptRequiresGuildmaster(overrides map[string]string) bool {
	return overrides[AcceptRequiresGuildmasterOverride] == "true"
}
