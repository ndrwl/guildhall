package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/config"
)

// preflightSkillPolicies uses App's existing process boundary for a bounded,
// no-model version probe and configures only the Claude runtime's generated files.
func (a *App) preflightSkillPolicies(p *startPlan) error {
	type selected struct {
		name     string
		policy   config.ClaudeSkillPolicy
		settings []byte
	}
	var policies []selected
	if rt, ok := p.runtime.(claude.Adapter); ok {
		policy, err := p.cfg.ClaudeSkills("")
		if err != nil {
			return err
		}
		rt.SkillPolicy = &policy
		p.runtime = rt
		policies = append(policies, selected{"Director", policy, rt.SessionFiles(filepath.Dir(p.cfgPath))[claude.SettingsFile]})
	}
	for _, name := range slices.Sorted(maps.Keys(p.cfg.Profiles)) {
		if p.cfg.Profiles[name].Adapter != "claude" {
			continue
		}
		policy, err := p.cfg.ClaudeSkills(name)
		if err != nil {
			return err
		}
		policies = append(policies, selected{"profile " + name, policy, claude.ArtisanPolicySettings(p.cfg.Profiles[name].AllowedTools, policy)})
	}
	if len(policies) == 0 {
		return nil
	}
	path, err := a.LookPath(claude.Program)
	if err != nil {
		return fmt.Errorf("claude skill policy: claude not found on PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := a.Output(ctx, path, "--version")
	if err != nil {
		return fmt.Errorf("claude skill policy version probe: %w", err)
	}
	if err := claude.CheckPolicyVersion(string(version)); err != nil {
		return err
	}
	a.preflightLine("skills", "Claude %s (%s); policy support validated against baseline %s; inherited bundle-disable=%q; organization managed restrictions and feature/account/plugin inventory not observed", strings.TrimSpace(string(version)), path, claude.MinimumPolicyVersion, os.Getenv("CLAUDE_CODE_DISABLE_BUNDLED_SKILLS"))
	for _, s := range policies {
		if err := claude.ValidatePolicySettings(s.settings, s.policy); err != nil {
			return err
		}
		if err := claude.CheckPolicyEnvironment(s.policy); err != nil {
			return err
		}
		a.preflightLine("skills", "%s %s; rendered JSON validated", s.name, s.policy.Summary())
	}
	return nil
}
