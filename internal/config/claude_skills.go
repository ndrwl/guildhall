package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/strictjson"
)

const (
	ClaudeSkillsDirectorKey   = "claude_skills_director"
	ClaudeSkillsArtisanKey    = "claude_skills_artisan"
	ClaudeSkillsProfilePrefix = "claude_skills_profile_"
)

// ClaudeSkillPolicy is a resolved external-skill selection, not a tool sandbox.
// Source, Key and Overridable describe Guildhall precedence, not Claude organization policy.
// See docs/contracts/config-claude-skills.md.
type ClaudeSkillPolicy struct {
	Preset               string            `json:"-"`
	Key                  string            `json:"-"`
	Source               string            `json:"-"`
	Overridable          bool              `json:"-"`
	DisableBundledSkills bool              `json:"disableBundledSkills"`
	SyncClaudeAiSkills   bool              `json:"syncClaudeAiSkills"`
	SkillOverrides       map[string]string `json:"skillOverrides"`
	EnabledPlugins       map[string]bool   `json:"enabledPlugins"`
}

type claudeSkillInput struct {
	Preset      string            `json:"preset"`
	AccountSync bool              `json:"account_sync"`
	Overrides   map[string]string `json:"overrides"`
	Plugins     map[string]bool   `json:"plugins"`
}

var bundledSkills = []string{"update-config", "keybindings-help", "code-review", "simplify", "fewer-permission-prompts", "loop", "schedule", "claude-api", "workflow-authoring", "run", "init", "security-review", "dataviz", "artifact-design", "artifact-diagramming", "artifact-capabilities", "batch", "verify", "commit", "pr", "debug", "design", "deep-research", "design-sync", "run-skill-generator"}
var accountSkills = []string{"built-in-browser", "chrome-browser", "computer-use", "deep-research", "docs", "docx", "import-memory", "morning", "pdf", "pptx", "skill-creator", "xlsx"}
var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(?::[a-z0-9][a-z0-9._-]*)*$`)
var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[a-z0-9][a-z0-9._-]*$`)

// ParseClaudeSkills expands a versioned preset then applies explicit overrides.
// It rejects ineffective account keeps, duplicate JSON keys and plugin skill overrides;
// unknown canonical custom skill names are accepted, never claimed discovered.
func ParseClaudeSkills(value string) (ClaudeSkillPolicy, error) {
	var in claudeSkillInput
	if err := strictjson.Decode([]byte(value), &in, strictjson.Options{MaxBytes: 64 << 10}); err != nil {
		return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: %w", err)
	}
	var document any
	_ = json.Unmarshal([]byte(value), &document)
	if policyHasNull(document) {
		return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: null values are not supported")
	}
	p := ClaudeSkillPolicy{Preset: in.Preset, Overridable: true, Source: "default", DisableBundledSkills: true, SyncClaudeAiSkills: in.AccountSync, SkillOverrides: map[string]string{}, EnabledPlugins: map[string]bool{"cc-plugin-plugin-authoring@builtin": false}}
	var keep []string
	switch in.Preset {
	case "director-v1", "artisan-v1":
	case "builder-v1":
		keep = []string{"claude-api", "run", "dataviz"}
	case "reviewer-v1":
		keep = []string{"security-review"}
	default:
		return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: unsupported preset %q", in.Preset)
	}
	for _, name := range bundledSkills {
		p.SkillOverrides[name] = "off"
	}
	for _, name := range accountSkills {
		p.SkillOverrides["anthropic-skills:"+name] = "off"
	}
	for _, name := range keep {
		p.SkillOverrides[name] = "on"
	}
	for _, name := range slices.Sorted(maps.Keys(in.Overrides)) {
		state := in.Overrides[name]
		switch name {
		case "review", "routines", "proactive":
			return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: ambiguous alias %q; use code-review, schedule or loop respectively", name)
		}
		if !skillName.MatchString(name) || name == "plugin-authoring" || strings.Contains(name, "@") {
			return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: %q is not a canonical non-plugin skill name; configure plugin enablement separately", name)
		}
		if !slices.Contains([]string{"on", "off", "name-only", "user-invocable-only"}, state) {
			return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: invalid state %q for %s", state, name)
		}
		if strings.HasPrefix(name, "anthropic-skills:") && state != "off" && !in.AccountSync {
			return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: %s requires account_sync=true", name)
		}
		p.SkillOverrides[name] = state
	}
	for _, name := range slices.Sorted(maps.Keys(in.Plugins)) {
		if !pluginName.MatchString(name) {
			return ClaudeSkillPolicy{}, fmt.Errorf("claude skill policy: invalid plugin ID %q; use plugin@source", name)
		}
		p.EnabledPlugins[name] = in.Plugins[name]
	}
	for _, name := range bundledSkills {
		if p.SkillOverrides[name] != "off" {
			p.DisableBundledSkills = false
		}
	}
	return p, nil
}

// ClaudeSkills resolves one entire effective setting; profile-specific settings replace
// the generic Artisan setting. Role names are not inferred from arbitrary profile names.
func (c *Config) ClaudeSkills(profile string) (ClaudeSkillPolicy, error) {
	key, preset := ClaudeSkillsDirectorKey, "director-v1"
	if profile != "" {
		if _, err := c.Profile(profile); err != nil {
			return ClaudeSkillPolicy{}, err
		}
		key, preset = ClaudeSkillsArtisanKey, "artisan-v1"
		if _, ok := c.Setting(ClaudeSkillsProfilePrefix + profile); ok {
			key = ClaudeSkillsProfilePrefix + profile
		}
	}
	value, ok := c.Setting(key)
	if !ok {
		value = `{"preset":"` + preset + `"}`
	}
	p, err := ParseClaudeSkills(value)
	if err != nil {
		return p, fmt.Errorf("%s: %w", key, err)
	}
	p.Key = key
	if m, found := c.Managed[key]; found {
		p.Source = "managed"
		p.Overridable = m.Overridable
	}
	if _, found := c.Local[key]; found && p.Overridable {
		p.Source = "local"
	}
	return p, nil
}

// Digest identifies normalized effective controls, independent of their configuration source.
func (p ClaudeSkillPolicy) Digest() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// Receipt is an owned provenance document, never passed as Claude settings.
func (p ClaudeSkillPolicy) Receipt() []byte {
	b, _ := json.MarshalIndent(struct {
		Preset      string            `json:"preset"`
		Key         string            `json:"key"`
		Source      string            `json:"source"`
		Overridable bool              `json:"overridable"`
		Digest      string            `json:"digest"`
		Controls    ClaudeSkillPolicy `json:"controls"`
	}{p.Preset, p.Key, p.Source, p.Overridable, p.Digest(), p}, "", "  ")
	return append(b, '\n')
}

// Summary describes configured controls, not a running session's discovered inventory.
func (p ClaudeSkillPolicy) Summary() string {
	var kept, dropped, unknown, disabled []string
	for _, name := range slices.Sorted(maps.Keys(p.SkillOverrides)) {
		state := p.SkillOverrides[name]
		if state == "off" {
			dropped = append(dropped, name)
		} else {
			kept = append(kept, name+"="+state)
		}
		if !slices.Contains(bundledSkills, name) && !slices.Contains(accountSkills, strings.TrimPrefix(name, "anthropic-skills:")) {
			unknown = append(unknown, name)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.EnabledPlugins)) {
		if !p.EnabledPlugins[id] {
			disabled = append(disabled, id)
		}
	}
	mode := "bundles disabled (disableBundledSkills)"
	if !p.DisableBundledSkills {
		mode = "selective skillOverrides; unknown future bundles may load"
	}
	sync := "disabled"
	if p.SyncClaudeAiSkills {
		sync = "enabled; unknown future account skills may load; opt-ins may post/schedule/spend; enable backing document/browser capabilities and safety skills together"
	}
	result := fmt.Sprintf("%s; key=%s source=%s overridable=%t digest=%s; %s; kept=%s; dropped known=%s; account sync %s (--settings); disabled plugins=%s (enabledPlugins); configured/validated, not live discovery; ambient personal/project/plugin skills not isolated; organization policy may override controls", p.Preset, p.Key, p.Source, p.Overridable, p.Digest(), mode, strings.Join(kept, ","), strings.Join(dropped, ","), sync, strings.Join(disabled, ","))
	if len(unknown) > 0 {
		result += "; not discovered here=" + strings.Join(unknown, ",") + "; plugin-provided skills cannot be hidden with skillOverrides; use whole-plugin enablement"
	}
	if len(kept) > 0 {
		result += "; kept skills require backing capabilities; no tool authority granted"
	}
	if p.SkillOverrides["security-review"] != "off" {
		result += "; security-review requires usable origin/HEAD and is supplementary, never replaces Guildhall review"
	}
	return result
}

// policyHasNull rejects JSON null rather than letting encoding/json interpret it
// as a false switch or an empty map; the policy schema has no nullable fields.
func policyHasNull(v any) bool {
	if v == nil {
		return true
	}
	if obj, ok := v.(map[string]any); ok {
		for _, value := range obj {
			if policyHasNull(value) {
				return true
			}
		}
	}
	return false
}

func (c *Config) checkClaudeSkills() []string {
	var problems []string
	check := func(key, value, source string) {
		if key != ClaudeSkillsDirectorKey && key != ClaudeSkillsArtisanKey && !strings.HasPrefix(key, ClaudeSkillsProfilePrefix) {
			return
		}
		if _, err := ParseClaudeSkills(value); err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", source, key, err))
		}
		if name, ok := strings.CutPrefix(key, ClaudeSkillsProfilePrefix); ok {
			if profile, found := c.Profiles[name]; !found || profile.Adapter != "claude" {
				problems = append(problems, fmt.Sprintf("%s %s: explicit Claude policy requires a declared claude profile", source, key))
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.Managed)) {
		check(key, c.Managed[key].Value, "managed")
	}
	for _, key := range slices.Sorted(maps.Keys(c.Local)) {
		check(key, c.Local[key], "local")
	}
	return problems
}
