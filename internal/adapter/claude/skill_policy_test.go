package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/config"
)

func TestSkillPolicyDirectorSettingsAndFlag(t *testing.T) {
	p, err := config.ParseClaudeSkills(`{"preset":"director-v1"}`)
	if err != nil {
		t.Fatal(err)
	}
	a := claude.Adapter{SkillPolicy: &p}
	root := t.TempDir()
	files := a.SessionFiles(root)
	var settings struct {
		Disable     bool                           `json:"disableBundledSkills"`
		Sync        bool                           `json:"syncClaudeAiSkills"`
		Plugins     map[string]bool                `json:"enabledPlugins"`
		Hooks       map[string]any                 `json:"hooks"`
		Status      any                            `json:"statusLine"`
		Permissions struct{ Allow, Deny []string } `json:"permissions"`
	}
	if err := json.Unmarshal(files[claude.SettingsFile], &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Disable || settings.Sync || len(settings.Plugins) != 1 || settings.Plugins["cc-plugin-plugin-authoring@builtin"] || len(settings.Hooks) != 5 || settings.Status == nil || len(settings.Permissions.Allow) == 0 || len(settings.Permissions.Deny) == 0 {
		t.Fatalf("controls damaged session: %+v", settings)
	}
	if len(files[claude.SkillPolicyFile]) == 0 {
		t.Fatal("no provenance")
	}
	cmd, err := a.Session(adapter.SessionSpec{Dir: root, Copy: root, RolePrompt: filepath.Join(root, "role.md"), FirstMessage: "Start."})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(cmd.Argv, "--settings")
	if i < 0 || cmd.Argv[i+1] != filepath.Join(root, claude.SettingsFile) {
		t.Fatalf("missing flag scope: %q", cmd.Argv)
	}
}

func TestSkillPolicyArtisanWithoutGrants(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	spec.Copy, spec.Profile = grantProfile(t, nil)
	if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(claude.ArtisanSettingsPath(spec.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var p config.ClaudeSkillPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if !p.DisableBundledSkills || p.SyncClaudeAiSkills || p.EnabledPlugins["cc-plugin-plugin-authoring@builtin"] {
		t.Fatalf("policy not rendered: %s", b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(spec.ResultFile), claude.SkillPolicyFile)); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPolicyArtisanResolvesOverrides(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	spec.Copy, spec.Profile = grantProfile(t, []string{"WebSearch", "WebFetch"})
	cfgPath := filepath.Join(spec.Copy, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, []byte("\n[local]\nclaude_skills_profile_builder = '{\"preset\":\"builder-v1\",\"account_sync\":true,\"overrides\":{\"anthropic-skills:pdf\":\"on\",\"dataviz\":\"off\"}}'\n")...)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(claude.ArtisanSettingsPath(spec.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Sync        bool              `json:"syncClaudeAiSkills"`
		Disable     bool              `json:"disableBundledSkills"`
		Overrides   map[string]string `json:"skillOverrides"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Sync || settings.Disable || settings.Overrides["anthropic-skills:pdf"] != "on" || settings.Overrides["dataviz"] != "off" || settings.Overrides["run"] != "on" || !slices.Equal(settings.Permissions.Allow, []string{"WebSearch", "WebFetch", claude.CILogRule}) {
		t.Fatalf("wrong resolved settings: %s", b)
	}
	b, err = os.ReadFile(filepath.Join(filepath.Dir(spec.ResultFile), claude.SkillPolicyFile))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct{ Preset, Key, Source, Digest string }
	if err := json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Preset != "builder-v1" || receipt.Key != "claude_skills_profile_builder" || receipt.Source != "local" || !strings.HasPrefix(receipt.Digest, "sha256:") {
		t.Fatalf("receipt: %+v", receipt)
	}
}

func TestSkillPolicyRenderedValidation(t *testing.T) {
	p, _ := config.ParseClaudeSkills(`{"preset":"director-v1"}`)
	good := (claude.Adapter{SkillPolicy: &p}).SessionFiles(t.TempDir())[claude.SettingsFile]
	if err := claude.ValidatePolicySettings(good, p); err != nil {
		t.Fatal(err)
	}
	if err := claude.ValidatePolicySettings(claude.ArtisanPolicySettings(nil, p), p); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(good, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "syncClaudeAiSkills")
	missing, _ := json.Marshal(object)
	for _, bad := range [][]byte{nil, []byte(`{}`), missing} {
		if err := claude.ValidatePolicySettings(bad, p); err == nil {
			t.Fatalf("accepted missing controls %s", bad)
		}
	}
	other, _ := config.ParseClaudeSkills(`{"preset":"builder-v1"}`)
	if err := claude.ValidatePolicySettings(good, other); err == nil {
		t.Fatal("accepted mismatched rendered policy")
	}
}

func TestSkillPolicyEnvironment(t *testing.T) {
	p, _ := config.ParseClaudeSkills(`{"preset":"builder-v1"}`)
	t.Setenv("CLAUDE_CODE_DISABLE_BUNDLED_SKILLS", "1")
	if err := claude.CheckPolicyEnvironment(p); err == nil {
		t.Fatal("advertised unavailable kept bundles")
	}
	p, _ = config.ParseClaudeSkills(`{"preset":"director-v1"}`)
	if err := claude.CheckPolicyEnvironment(p); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPolicyVersionContract(t *testing.T) {
	for _, version := range []string{"2.1.310 (Claude Code)", "2.1.311 (Claude Code)", "2.2.0 (Claude Code)"} {
		if err := claude.CheckPolicyVersion(version); err != nil {
			t.Errorf("%s: %v", version, err)
		}
	}
	for _, version := range []string{"", "2.1.309 (Claude Code)", "2.1.310-beta", "not claude 2.1.310"} {
		if err := claude.CheckPolicyVersion(version); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}
