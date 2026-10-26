package config

import (
	"errors"
	"fmt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// DefaultAdapterKey explicitly selects the runtime for Artisan stages without profiles.
const DefaultAdapterKey = "default_adapter"

// ErrMissingAdapter refuses an Artisan stage with neither a profile nor a default adapter.
var ErrMissingAdapter = errors.New("artisan adapter is not configured")

// DefaultAdapter returns the explicitly configured default, or empty when unset.
// It follows normal managed/local precedence and never supplies a fake fallback.
func (c *Config) DefaultAdapter() string {
	if c == nil {
		return ""
	}
	v, _ := c.Setting(DefaultAdapterKey)
	return v
}

// CheckArtisanAdapters checks profile references and refuses each Artisan stage lacking
// a profile when defaultAdapter is empty. It changes nothing and does not start a runtime.
// Call before admitting a task or mutating its records on advance.
func (c *Config) CheckArtisanAdapters(def *workflow.Definition, defaultAdapter string) error {
	if err := c.CheckProfiles(def); err != nil {
		return err
	}
	for _, step := range def.Steps {
		if step.Actor() == workflow.ArtisanActor && step.Profile() == "" && defaultAdapter == "" {
			return fmt.Errorf("workflow %q step %q: %w; set the step's profile to a declared [profile.<name>] with adapter, or set [local] default_adapter (for example \"claude\"; use \"fake\" only intentionally for demos/tests)", def.Name, step.ID, ErrMissingAdapter)
		}
	}
	return nil
}
