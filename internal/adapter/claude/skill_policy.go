package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// SkillPolicyFile is an owned provenance receipt, not Claude settings.
const SkillPolicyFile = "claude-skill-policy.json"

// MinimumPolicyVersion is the oldest tested Claude Code skill-settings version.
const MinimumPolicyVersion = "2.1.310"

var policyVersion = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?: \(Claude Code\))?$`)

// CheckPolicyVersion refuses unknown/prerelease and older versions, since print
// mode can silently ignore unsupported settings. It makes no model call.
func CheckPolicyVersion(version string) error {
	m := policyVersion.FindStringSubmatch(strings.TrimSpace(version))
	if m != nil {
		major, e1 := strconv.Atoi(m[1])
		minor, e2 := strconv.Atoi(m[2])
		patch, e3 := strconv.Atoi(m[3])
		if e1 == nil && e2 == nil && e3 == nil && (major > 2 || major == 2 && (minor > 1 || minor == 1 && patch >= 310)) {
			return nil
		}
	}
	return fmt.Errorf("claude skill policy requires Claude Code >= %s (tested baseline); got %q; install a supported version", MinimumPolicyVersion, strings.TrimSpace(version))
}

// CheckPolicyEnvironment refuses an inherited bundle kill switch when the policy
// keeps bundled skills; otherwise --settings could not deliver its advertised keeps.
func CheckPolicyEnvironment(p config.ClaudeSkillPolicy) error {
	if !p.DisableBundledSkills {
		switch strings.ToLower(os.Getenv("CLAUDE_CODE_DISABLE_BUNDLED_SKILLS")) {
		case "1", "true", "yes":
			return fmt.Errorf("claude skill policy %s keeps bundles but CLAUDE_CODE_DISABLE_BUNDLED_SKILLS is enabled; unset it or choose a bundle-disabled policy", p.Preset)
		}
	}
	return nil
}

func checkProgramPolicy(ctx context.Context, path string, p config.ClaudeSkillPolicy) error {
	if err := CheckPolicyEnvironment(p); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("claude skill policy version probe: %w", err)
	}
	return CheckPolicyVersion(string(b))
}

// attemptPolicy is the Claude skill policy for an attempt of profile launched from the Director copy at copyDir,
// whose config is admitted through the copy's configuration-admission boundary (internal/admit); with no copy or
// no profile it is the artisan-v1 preset.
func attemptPolicy(copyDir, profile string) (config.ClaudeSkillPolicy, error) {
	if copyDir == "" || profile == "" {
		p, err := config.ParseClaudeSkills(`{"preset":"artisan-v1"}`)
		p.Key = config.ClaudeSkillsArtisanKey
		return p, err
	}
	cfg, _, err := admit.Admit(filepath.Join(copyDir, "config.toml"))
	if err != nil {
		return config.ClaudeSkillPolicy{}, err
	}
	return cfg.ClaudeSkills(profile)
}

// ArtisanPolicySettings is the attempt settings document used by launch and
// preflight, with explicit empty grants and flag-scoped skill source controls.
func ArtisanPolicySettings(rules []string, p config.ClaudeSkillPolicy) []byte {
	return mergePolicy(ArtisanSettings(rules), p)
}

// ValidatePolicySettings checks that a generated document contains every explicit
// source switch and exactly the resolved skill/plugin controls. Other settings
// (permissions, hooks and status line) remain the renderer's independent contract.
// This validates configured JSON, not runtime enforcement or live discovery.
func ValidatePolicySettings(data []byte, p config.ClaudeSkillPolicy) error {
	var obj map[string]json.RawMessage
	if err := strictjson.Decode(data, &obj, strictjson.Options{}); err != nil {
		return fmt.Errorf("claude skill policy: generated settings: %w", err)
	}
	var controls config.ClaudeSkillPolicy
	for _, key := range []string{"disableBundledSkills", "syncClaudeAiSkills", "skillOverrides", "enabledPlugins"} {
		if raw, ok := obj[key]; !ok || string(raw) == "null" {
			return fmt.Errorf("claude skill policy: generated settings missing %s", key)
		}
	}
	if err := json.Unmarshal(data, &controls); err != nil {
		return fmt.Errorf("claude skill policy: generated controls: %w", err)
	}
	if controls.Digest() != p.Digest() {
		return fmt.Errorf("claude skill policy: generated controls do not match %s", p.Digest())
	}
	return nil
}

func mergePolicy(settings []byte, p config.ClaudeSkillPolicy) []byte {
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(settings, &obj)
	b, _ := json.Marshal(p)
	var controls map[string]json.RawMessage
	_ = json.Unmarshal(b, &controls)
	for key, value := range controls {
		obj[key] = value
	}
	b, _ = json.MarshalIndent(obj, "", "  ")
	return append(b, '\n')
}
