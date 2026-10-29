package config

import (
	"errors"
	"fmt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var ErrMissingAdapter = errors.New("artisan adapter is not configured")

// CheckArtisanAdapters refuses a workflow with an Artisan step that resolves no declared profile
// (workflow.EffectiveProfile): a step naming an unknown profile (CheckProfiles), a step whose type has no role and
// that names none, and a role step whose role profile this config lacks, which a parsed Director config never does,
// since it has the shipped profiles.
func (c *Config) CheckArtisanAdapters(def *workflow.Definition) error {
	if err := c.CheckProfiles(def); err != nil {
		return err
	}
	for _, step := range def.Steps {
		if step.Actor() != workflow.ArtisanActor || step.Profile() != "" {
			continue
		}
		role := workflow.EffectiveProfile(step)
		if role == "" {
			return fmt.Errorf("workflow %q step %q: %w; set a profile on that step in a custom workflow, referencing a declared [profile.<name>] with adapter", def.Name, step.ID, ErrMissingAdapter)
		}
		if _, ok := c.Profiles[role]; !ok {
			return fmt.Errorf("workflow %q step %q: %w; declare [profile.%s] with adapter, or set a profile on that step in a custom workflow", def.Name, step.ID, ErrMissingAdapter, role)
		}
	}
	return nil
}
