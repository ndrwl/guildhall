package config

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var ErrMissingAdapter = errors.New("artisan adapter is not configured")

// ErrReadOnlyAdapter reports a workflow step whose profile runs the codex adapter, which runs only in Codex's
// read-only sandbox, on a step kind not admitted as read-only (codexStepKinds).
var ErrReadOnlyAdapter = errors.New("the codex adapter runs read-only and may not run this step")

// codexStepKinds are the workflow step types a codex profile may run: the read-only kinds whose output the Codex
// wrapper writes from the model's answer. It starts empty, so every workflow step refuses codex, and only the quality
// judge, which runs outside the workflow engine and is always read-only, runs on it; d109 adds review and
// document-review.
var codexStepKinds = []string{}

// CheckArtisanAdapters refuses a workflow with an Artisan step that resolves no declared profile
// (workflow.EffectiveProfile): a step naming an unknown profile (CheckProfiles), a step whose type has no role and
// that names none, and a role step whose role profile this config lacks, which a parsed Director config never does,
// since it has the shipped profiles. It also refuses, with an error wrapping ErrReadOnlyAdapter, a step whose profile
// runs the codex adapter unless the step's type is one of codexStepKinds, and likewise a rebaser or designer profile
// that runs it, since the engine runs those steps for every workflow.
func (c *Config) CheckArtisanAdapters(def *workflow.Definition) error {
	if err := c.CheckProfiles(def); err != nil {
		return err
	}
	for _, step := range def.Steps {
		if step.Actor() != workflow.ArtisanActor {
			continue
		}
		role := workflow.EffectiveProfile(step)
		if step.Profile() == "" {
			if role == "" {
				return fmt.Errorf("workflow %q step %q: %w; set a profile on that step in a custom workflow, referencing a declared [profile.<name>] with adapter", def.Name, step.ID, ErrMissingAdapter)
			}
			if _, ok := c.Profiles[role]; !ok {
				return fmt.Errorf("workflow %q step %q: %w; declare [profile.%s] with adapter, or set a profile on that step in a custom workflow", def.Name, step.ID, ErrMissingAdapter, role)
			}
		}
		if c.Profiles[role].Adapter == adapter.CodexName && !slices.Contains(codexStepKinds, step.Type) {
			return fmt.Errorf("workflow %q step %q: profile %s: %w: a %s step may write, and Codex is trusted only where it cannot; run it on a claude profile", def.Name, step.ID, role, ErrReadOnlyAdapter, step.Type)
		}
	}
	// The engine runs the rebase and design steps itself, under their role profiles, whatever the workflow says.
	for _, kind := range syntheticKinds {
		role := workflow.RoleProfile(kind)
		if c.Profiles[role].Adapter == adapter.CodexName && !slices.Contains(codexStepKinds, kind) {
			return fmt.Errorf("workflow %q: profile %s runs the engine's %s steps: %w: a %s step may write, and Codex is trusted only where it cannot; run it on a claude profile", def.Name, role, kind, ErrReadOnlyAdapter, kind)
		}
	}
	return nil
}

// syntheticKinds are the step types the engine runs under their role profile (workflow.RoleProfile) without a
// workflow step of their own.
var syntheticKinds = []string{"rebase", "design"}
