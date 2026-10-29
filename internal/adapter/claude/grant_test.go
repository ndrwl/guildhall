package claude_test

import (
	"os"
	"path/filepath"
	"slices"
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
// repository's grant leaks.
func TestEffectiveGrant(t *testing.T) {
	readOnly := []string{"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git rev-list:*)", "Bash(git merge-base:*)"}
	builder := append(slices.Clone(readOnly), "Bash(git add:*)", "Bash(git commit:*)", "Bash(git fetch:*)", "Bash(git rebase:*)", "Bash(git switch:*)")
	claudeProfiles := "[profile.builder]\nadapter = \"claude\"\n[profile.planner]\nadapter = \"claude\"\n[profile.reviewer]\nadapter = \"claude\"\n" +
		"[profile.researcher]\nadapter = \"claude\"\n[profile.worker]\nadapter = \"claude\"\n"
	for name, c := range map[string]struct {
		extra, profile, repository string
		want                       []claude.Grant
	}{
		"builder default":       {claudeProfiles, "builder", "", grants(claude.SourceDefault, builder...)},
		"planner default":       {claudeProfiles, "planner", "", grants(claude.SourceDefault, readOnly...)},
		"reviewer default":      {claudeProfiles, "reviewer", "", grants(claude.SourceDefault, readOnly...)},
		"researcher default":    {claudeProfiles, "researcher", "", grants(claude.SourceDefault, append(slices.Clone(readOnly), "WebSearch", "WebFetch")...)},
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
			got, err := claude.EffectiveGrant(grantConfig(t, c.extra), c.profile, c.repository)
			if err != nil || !slices.Equal(got, c.want) {
				t.Errorf("EffectiveGrant = %v, %v; want %v", got, err, c.want)
			}
		})
	}
	if _, err := claude.EffectiveGrant(grantConfig(t, claudeProfiles), "undeclared", "app"); err == nil {
		t.Error("EffectiveGrant accepted an undeclared profile")
	}
}

// TestFakeBuilderGetsNoDefault: a profile of the fake adapter named builder gets no shipped default.
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

// TestShippedClaudeRoleDefaults: the shipped example, parsed by its real consumer, gives its profiles their skill
// presets and effective grants: only the researcher may search and fetch the web, and the builder in the Go
// repository gets go test and go vet on top of its shipped default.
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
	for name, preset := range map[string]string{"researcher": "artisan-v1", "planner": "artisan-v1", "builder": "builder-v1", "reviewer": "reviewer-v1"} {
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
				if slices.Contains(rules, tool) != (name == "builder" && repo.ID == goRepo) {
					t.Errorf("%s in %s: wrong %s authority", name, repo.ID, tool)
				}
			}
		}
	}
}
