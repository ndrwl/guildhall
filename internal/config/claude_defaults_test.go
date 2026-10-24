package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The shipped example is a machine-consumed config artifact, parsed by its real consumer.
func TestShippedClaudeRoleDefaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "director.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for name, preset := range map[string]string{"researcher": "artisan-v1", "planner": "artisan-v1", "builder": "builder-v1", "reviewer": "reviewer-v1"} {
		p, err := cfg.ClaudeSkills(name)
		if err != nil || p.Preset != preset {
			t.Fatalf("%s: %+v %v", name, p, err)
		}
		for _, tool := range []string{"WebSearch", "WebFetch"} {
			if slices.Contains(cfg.Profiles[name].AllowedTools, tool) != (name == "researcher") {
				t.Errorf("%s: wrong %s authority", name, tool)
			}
		}
	}
}

func TestClaudeProfileReplacesGenericPolicyAndLocks(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{"worker": {Adapter: "claude"}}, Managed: map[string]ManagedSetting{
		ClaudeSkillsArtisanKey:               {Value: `{"preset":"builder-v1"}`, Overridable: true},
		ClaudeSkillsProfilePrefix + "worker": {Value: `{"preset":"reviewer-v1"}`, Overridable: false},
	}}
	p, err := cfg.ClaudeSkills("worker")
	if err != nil || p.Preset != "reviewer-v1" || p.Source != "managed" || p.Overridable {
		t.Fatalf("profile lock: %+v %v", p, err)
	}
	if p.SkillOverrides["run"] != "off" {
		t.Fatal("generic builder policy leaked into reviewer")
	}
	cfg.Local = map[string]string{ClaudeSkillsProfilePrefix + "worker": `{"preset":"artisan-v1"}`}
	if len(cfg.checkClaudeSkills()) != 0 {
		t.Fatal("syntax check should be valid; locking is Config.validate's responsibility")
	}
	if got, _ := cfg.ClaudeSkills("worker"); got.Preset != "reviewer-v1" {
		t.Fatal("locked policy replaced")
	}
}

func TestClaudeMatrixAndDigestIdentity(t *testing.T) {
	for _, preset := range []string{"director-v1", "artisan-v1", "builder-v1", "reviewer-v1"} {
		p, err := ParseClaudeSkills(`{"preset":"` + preset + `"}`)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range bundledSkills {
			keep := preset == "builder-v1" && slices.Contains([]string{"claude-api", "run", "dataviz"}, name) || preset == "reviewer-v1" && name == "security-review"
			if (p.SkillOverrides[name] == "on") != keep {
				t.Errorf("%s %s mismatch", preset, name)
			}
		}
		for _, name := range accountSkills {
			if p.SkillOverrides["anthropic-skills:"+name] != "off" {
				t.Errorf("%s %s not off", preset, name)
			}
		}
		for name := range p.SkillOverrides {
			if strings.HasPrefix(name, "guildhall-") {
				t.Fatal("release skills must remain independent")
			}
		}
	}
	p, _ := ParseClaudeSkills(`{"preset":"builder-v1","overrides":{"dataviz":"off","simplify":"on"}}`)
	q, _ := ParseClaudeSkills(`{"overrides":{"simplify":"on","dataviz":"off"},"preset":"builder-v1"}`)
	if p.Digest() != q.Digest() {
		t.Fatal("JSON key ordering changed identity")
	}
	q.SkillOverrides["simplify"] = "off"
	if p.Digest() == q.Digest() {
		t.Fatal("controls changed without digest changing")
	}
}
