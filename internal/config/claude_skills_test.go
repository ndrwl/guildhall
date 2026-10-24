package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeSkillPresets(t *testing.T) {
	for preset, keep := range map[string][]string{"director-v1": nil, "artisan-v1": nil, "builder-v1": {"claude-api", "run", "dataviz"}, "reviewer-v1": {"security-review"}} {
		t.Run(preset, func(t *testing.T) {
			p, err := ParseClaudeSkills(`{"preset":"` + preset + `"}`)
			if err != nil {
				t.Fatal(err)
			}
			if p.SyncClaudeAiSkills || p.DisableBundledSkills != (len(keep) == 0) || p.EnabledPlugins["cc-plugin-plugin-authoring@builtin"] {
				t.Fatalf("wrong switches: %+v", p)
			}
			if len(p.EnabledPlugins) != 1 {
				t.Fatal("unrelated plugins must not change")
			}
			for _, name := range keep {
				if p.SkillOverrides[name] != "on" {
					t.Errorf("%s not kept", name)
				}
			}
			for _, name := range []string{"schedule", "design", "deep-research", "design-sync", "run-skill-generator"} {
				if p.SkillOverrides[name] != "off" {
					t.Errorf("%s not suppressed", name)
				}
			}
			b, _ := json.Marshal(p)
			q, _ := ParseClaudeSkills(`{"preset":"` + preset + `"}`)
			c, _ := json.Marshal(q)
			wantDigest := map[string]string{
				"director-v1": "sha256:e8dfb103fbb5ba1b7166108d43abe12e1287215ac17bcaa2fab4dcbd208649ba",
				"artisan-v1":  "sha256:e8dfb103fbb5ba1b7166108d43abe12e1287215ac17bcaa2fab4dcbd208649ba",
				"builder-v1":  "sha256:e54dec7e89af6cbb90f0d0ecf191c2eb6d8cab1e115b41bf51172637a09c9bf9",
				"reviewer-v1": "sha256:afef7b5370a15d61b2de85fc657de103b7f889e1eef3a2c23a2aebd97410f9c2",
			}[preset]
			if string(b) != string(c) || p.Digest() != wantDigest {
				t.Fatalf("unstable controls digest: %s, want %s", p.Digest(), wantDigest)
			}
		})
	}
}

func TestClaudeSkillValidation(t *testing.T) {
	for _, bad := range []string{`{}`, `null`, `{"preset":"builder-v2"}`, `{"preset":"artisan-v1","oops":true}`, `{"preset":"artisan-v1","preset":"builder-v1"}`, `{"preset":"artisan-v1","overrides":{"pdf":"yes"}}`, `{"preset":"artisan-v1","overrides":{"/review":"off"}}`, `{"preset":"artisan-v1","overrides":{"anthropic-skills:pdf":"on"}}`, `{"preset":"artisan-v1","plugins":{"plugin-authoring":false}}`, `{"preset":"artisan-v1","overrides":{"plugin-authoring":"off"}}`, `{"preset":"artisan-v1","account_sync":null}`, `{"preset":"artisan-v1","plugins":{"x@y":null}}`, `{"preset":"artisan-v1","overrides":{"review":"off"}}`, `{"preset":"artisan-v1","overrides":{"routines":"off"}}`, `{"preset":"artisan-v1","overrides":{"proactive":"off"}}`} {
		if _, err := ParseClaudeSkills(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	p, err := ParseClaudeSkills(`{"preset":"artisan-v1","account_sync":true,"overrides":{"anthropic-skills:pdf":"on","custom-skill":"name-only"}}`)
	if err != nil || !p.SyncClaudeAiSkills || p.SkillOverrides["anthropic-skills:pdf"] != "on" || p.SkillOverrides["anthropic-skills:xlsx"] != "off" {
		t.Fatalf("opt-in: %+v %v", p, err)
	}
	p, err = ParseClaudeSkills(`{"preset":"artisan-v1","overrides":{"run":"on"}}`)
	if err != nil || p.DisableBundledSkills || p.SkillOverrides["schedule"] != "off" {
		t.Fatalf("selective: %+v %v", p, err)
	}
}

func TestClaudeSkillResolution(t *testing.T) {
	base := strings.Replace(director(t), "[local]\nreview_instructions = \"Also check Windows paths.\"\nnotes_dir = \"notes\"\n", "", 1) + "\n[profile.worker]\nadapter='claude'\n"
	cfg, err := Parse([]byte(base + "\n[managed.claude_skills_artisan]\nvalue='{\"preset\":\"builder-v1\"}'\noverridable=true\n[local]\nclaude_skills_artisan='{\"preset\":\"reviewer-v1\"}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.ClaudeSkills("worker")
	if err != nil || p.Preset != "reviewer-v1" || p.Source != "local" || p.Key != "claude_skills_artisan" {
		t.Fatalf("resolution %+v %v", p, err)
	}
	for _, extra := range []string{
		"\n[managed.claude_skills_artisan]\nvalue='{}'\noverridable=true\n[local]\nclaude_skills_artisan='{\"preset\":\"artisan-v1\"}'\n",
		"\n[managed.claude_skills_artisan]\nvalue='{\"preset\":\"builder-v1\"}'\n[local]\nclaude_skills_artisan='{\"preset\":\"artisan-v1\"}'\n",
		"\n[profile.other]\nadapter='fake'\n[local]\nclaude_skills_profile_other='{\"preset\":\"builder-v1\"}'\n",
	} {
		if _, err := Parse([]byte(base + extra)); err == nil {
			t.Errorf("accepted invalid %s", extra)
		}
	}
	cfg, err = Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	p, err = cfg.ClaudeSkills("worker")
	if err != nil || p.Preset != "artisan-v1" || !strings.Contains(p.Summary(), "not isolated") {
		t.Fatalf("default %+v %v", p, err)
	}
}
