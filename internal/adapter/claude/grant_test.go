package claude_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/config"
)

// grantConfig is a Director config with two repositories, app and site, that adds extra.
func grantConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(`schema_version = 1
mode = "director"
name = "test"

[[repository]]
id = "app"
path = "/src/app"
remote = "git@example.com:app.git"
owner_machine = "test"

[repository.profiles.builder]
allowed_tools = ["Bash(go test:*)", "Bash(git commit:*)"]

[repository.profiles.worker]
allowed_tools = ["Bash(make:*)"]

[repository.profiles.rebaser]
allowed_tools = ["Bash(go test:*)"]

[[repository]]
id = "site"
path = "/src/site"
remote = "git@example.com:site.git"
owner_machine = "test"

[repository.profiles.builder]
allowed_tools = ["Bash(npm test:*)"]

` + extra))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func grants(source claude.GrantSource, rules ...string) []claude.Grant {
	var gs []claude.Grant
	for _, r := range rules {
		gs = append(gs, claude.Grant{Rule: r, Source: source})
	}
	return gs
}

// TestEffectiveGrant: the copy's list, set even to empty, replaces a claude profile's shipped default, which only a
// well-known name has; the task's repository grant is appended without repeating a rule, and no other
// repository's grant leaks; every claude profile, with or without a copy list, gets guildhall ci log and no gh.
func TestEffectiveGrant(t *testing.T) {
	readOnly := []string{"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git rev-list:*)", "Bash(git merge-base:*)", "Bash(git ls-files:*)"}
	builder := append(slices.Clone(readOnly), "Bash(git add:*)", "Bash(git commit:*)", "Bash(git fetch:*)", "Bash(git rebase:*)", "Bash(git switch:*)")
	rebaser := append(slices.Clone(readOnly), "Bash(git add:*)", "Bash(git commit:*)", "Bash(git fetch:*)", "Bash(git rebase:*)")
	reviewer := append(slices.Clone(readOnly), "Bash(guildhall review check:*)")
	claudeProfiles := "[profile.builder]\nadapter = \"claude\"\n[profile.planner]\nadapter = \"claude\"\n[profile.reviewer]\nadapter = \"claude\"\n" +
		"[profile.researcher]\nadapter = \"claude\"\n[profile.worker]\nadapter = \"claude\"\n"
	for name, c := range map[string]struct {
		extra, profile, repository string
		want                       []claude.Grant
	}{
		"builder default":    {claudeProfiles, "builder", "", grants(claude.SourceDefault, builder...)},
		"planner default":    {claudeProfiles, "planner", "", grants(claude.SourceDefault, readOnly...)},
		"reviewer default":   {claudeProfiles, "reviewer", "", grants(claude.SourceDefault, reviewer...)},
		"researcher default": {claudeProfiles, "researcher", "", grants(claude.SourceDefault, append(slices.Clone(readOnly), "WebSearch", "WebFetch")...)},
		"rebaser default":    {claudeProfiles, "rebaser", "", grants(claude.SourceDefault, rebaser...)},
		"shipped rebaser and repository": {"[profile.worker]\nadapter = \"claude\"\n", "rebaser", "app",
			append(grants(claude.SourceDefault, rebaser...), grants(claude.SourceRepository, "Bash(go test:*)")...)},
		"shipped builder, no copy section": {"[profile.worker]\nadapter = \"claude\"\n", "builder", "",
			grants(claude.SourceDefault, builder...)},
		"other name":            {claudeProfiles, "worker", "", nil},
		"other name repository": {claudeProfiles, "worker", "app", grants(claude.SourceRepository, "Bash(make:*)")},
		"repository appended": {claudeProfiles, "builder", "app",
			append(grants(claude.SourceDefault, builder...), grants(claude.SourceRepository, "Bash(go test:*)")...)},
		"other repository": {claudeProfiles, "builder", "site",
			append(grants(claude.SourceDefault, builder...), grants(claude.SourceRepository, "Bash(npm test:*)")...)},
		"unlisted repository": {claudeProfiles, "builder", "elsewhere", grants(claude.SourceDefault, builder...)},
		"copy list replaces": {"[profile.builder]\nadapter = \"claude\"\nallowed_tools = [\"Read\", \"Bash(go test:*)\"]\n[profile.worker]\nadapter = \"claude\"\n", "builder", "app",
			append(grants(claude.SourceCopy, "Read", "Bash(go test:*)"), grants(claude.SourceRepository, "Bash(git commit:*)")...)},
		"copy empty grants nothing":  {"[profile.builder]\nadapter = \"claude\"\nallowed_tools = []\n[profile.worker]\nadapter = \"claude\"\n", "builder", "", nil},
		"copy empty with repository": {"[profile.builder]\nadapter = \"claude\"\nallowed_tools = []\n[profile.worker]\nadapter = \"claude\"\n", "builder", "site", grants(claude.SourceRepository, "Bash(npm test:*)")},
		"fake builder":               {"[profile.builder]\nadapter = \"claude\"\n[profile.worker]\nadapter = \"claude\"\n[profile.planner]\nadapter = \"fake\"\n", "planner", "app", nil},
	} {
		t.Run(name, func(t *testing.T) {
			// Every claude profile, even one whose copy list is empty, is granted guildhall ci log last.
			want := c.want
			if name != "fake builder" {
				want = append(slices.Clone(want), grants(claude.SourceGuildhall, "Bash(guildhall ci log:*)")...)
			}
			got, err := claude.EffectiveGrant(grantConfig(t, c.extra), c.profile, c.repository)
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("EffectiveGrant = %v, %v; want %v", got, err, want)
			}
			for _, r := range claude.GrantRules(got) {
				if strings.Contains(r, "gh ") || strings.Contains(r, "curl") || strings.Contains(r, "gh:") {
					t.Errorf("EffectiveGrant grants %s", r)
				}
			}
		})
	}
	if _, err := claude.EffectiveGrant(grantConfig(t, claudeProfiles), "undeclared", "app"); err == nil {
		t.Error("EffectiveGrant accepted an undeclared profile")
	}
}

// TestShippedProfilesHaveShippedGrants: every profile the release ships runs claude and has a shipped default
// grant, so a copy that declares no profiles still gives each role its permissions.
func TestShippedProfilesHaveShippedGrants(t *testing.T) {
	for name, p := range config.ShippedProfiles {
		if p.Adapter != "claude" || p.AllowedToolsSet() || len(claude.ShippedDefaultGrants[name]) == 0 {
			t.Errorf("shipped profile %s = %+v has no shipped grant", name, p)
		}
	}
}

// TestFakeBuilderGetsNoDefault:a profile of the fake adapter named builder gets no shipped default.
func TestFakeBuilderGetsNoDefault(t *testing.T) {
	cfg, err := config.Parse([]byte("schema_version = 1\nmode = \"director\"\nname = \"test\"\n[profile.builder]\nadapter = \"fake\"\n" +
		"[[repository]]\nid = \"app\"\npath = \"/src/app\"\nremote = \"git@example.com:app.git\"\nowner_machine = \"test\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := claude.EffectiveGrant(cfg, "builder", "app"); err != nil || got != nil {
		t.Errorf("fake builder grant = %v, %v; want none", got, err)
	}
}

// TestShippedClaudeRoleDefaults: the shipped example, which declares no [profile.*], parsed by its real consumer,
// gives the shipped role profiles their skill presets and effective grants: only the researcher may search and fetch
// the web, and in the Go repository the builder gets go test and go vet, and the reviewer and rebaser go test, on
// top of their shipped defaults.
func TestShippedClaudeRoleDefaults(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "director.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	goRepo := ""
	for _, r := range cfg.Repositories {
		if _, ok := r.Profiles["builder"]; ok {
			goRepo = r.ID
		}
	}
	if goRepo == "" {
		t.Fatal("the example has no repository granting its builder")
	}
	for name, p := range cfg.Profiles {
		if p != config.ShippedProfiles[name] {
			t.Errorf("profile %s = %+v, want only the shipped profiles", name, p)
		}
	}
	goTools := map[string][]string{"builder": claude.SuggestedGoAllowedTools, "reviewer": {"Bash(go test:*)"}, "rebaser": {"Bash(go test:*)"}}
	for name, preset := range map[string]string{"researcher": "artisan-v1", "planner": "artisan-v1", "builder": "builder-v1", "reviewer": "reviewer-v1", "rebaser": "builder-v1"} {
		p, err := cfg.ClaudeSkills(name)
		if err != nil || p.Preset != preset {
			t.Fatalf("%s: %+v %v", name, p, err)
		}
		for _, repo := range cfg.Repositories {
			grant, err := claude.EffectiveGrant(cfg, name, repo.ID)
			if err != nil {
				t.Fatal(err)
			}
			rules := claude.GrantRules(grant)
			if !slices.Equal(rules[:len(claude.ShippedDefaultGrants[name])], claude.ShippedDefaultGrants[name]) {
				t.Errorf("%s in %s: grant %q does not start with its shipped default", name, repo.ID, rules)
			}
			for _, tool := range []string{"WebSearch", "WebFetch"} {
				if slices.Contains(rules, tool) != (name == "researcher") {
					t.Errorf("%s in %s: wrong %s authority", name, repo.ID, tool)
				}
			}
			for _, tool := range claude.SuggestedGoAllowedTools {
				if slices.Contains(rules, tool) != (repo.ID == goRepo && slices.Contains(goTools[name], tool)) {
					t.Errorf("%s in %s: wrong %s authority", name, repo.ID, tool)
				}
			}
		}
	}
}
