package config

import (
	"errors"
	"fmt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var ErrMissingAdapter = errors.New("artisan adapter is not configured")

func (c *Config) CheckArtisanAdapters(def *workflow.Definition) error {
	if err := c.CheckProfiles(def); err != nil {
		return err
	}
	for _, step := range def.Steps {
		if step.Actor() != workflow.ArtisanActor || step.Profile() != "" {
			continue
		}
		if role := workflow.RoleProfile(step.Type); role != "" {
			return fmt.Errorf("workflow %q step %q: %w; declare [profile.%s] with adapter, or set a profile on that step in a custom workflow", def.Name, step.ID, ErrMissingAdapter, role)
		}
		return fmt.Errorf("workflow %q step %q: %w; set a profile on that step in a custom workflow, referencing a declared [profile.<name>] with adapter", def.Name, step.ID, ErrMissingAdapter)
	}
	return nil
}
