package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

const profiles = `
[profile.reviewer-opus]
adapter = "claude"
model = "opus"
effort = "high"
role = "roles/reviewer.md"

[profile.reviewer-astra]
adapter = "pi"
model = "openai/gpt-6-astra"
`

func TestProfilesParse(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + profiles))
	if err != nil {
		t.Fatalf("profiles refused: %v", err)
	}
	p, err := cfg.Profile("reviewer-opus")
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Adapter: "claude", Model: "opus", Effort: "high", Role: "roles/reviewer.md"}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("Profile = %+v, want %+v", p, want)
	}
	if p, err := cfg.Profile("reviewer-astra"); err != nil || p.Role != "" || p.Adapter != "pi" {
		t.Fatalf("Profile(reviewer-astra) = %+v, %v", p, err)
	}
}

func TestUnknownProfileRefused(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + profiles))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Profile("reviewer-strict"); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("Profile(unknown) err = %v", err)
	}
	def, err := workflow.Parse([]byte(`{"schema": "guildhall.workflow/1", "name": "three", "steps": [
	  {"id": "build", "type": "build", "version": 1, "settings": {"profile": "careful"}},
	  {"id": "review-a", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "reviewer-opus"}},
	  {"id": "review-b", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "reviewer-astra"}},
	  {"id": "review-c", "type": "document-review", "version": 1, "settings": {"actor": "guildmaster", "profile": "reviewer-strict"}},
	  {"id": "plan", "type": "plan", "version": 1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.CheckProfiles(def)
	if !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("CheckProfiles err = %v", err)
	}
	for _, want := range []string{`step "build" names unknown profile "careful"`, `step "review-c" names unknown profile "reviewer-strict"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckProfiles err %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "reviewer-opus") || strings.Contains(err.Error(), "reviewer-astra") {
		t.Errorf("CheckProfiles refused a declared profile: %v", err)
	}
	def.Steps = def.Steps[1:3]
	if err := cfg.CheckProfiles(def); err != nil {
		t.Fatalf("declared profiles refused: %v", err)
	}
}

func TestInvalidProfilesRefused(t *testing.T) {
	cases := map[string]string{
		"[profile.Bad]\nadapter = \"claude\"\n":                     `invalid profile name "Bad"`,
		"[profile.p]\nmodel = \"opus\"\n":                           `profile "p": adapter is required`,
		"[profile.p]\nadapter = \"Claude Code\"\n":                  `profile "p": invalid adapter "Claude Code"`,
		"[profile.p]\nadapter = \"claude\"\nrole = \"/etc/r.md\"\n": `profile "p": role must be a relative slash-separated path inside the config directory`,
		"[profile.p]\nadapter = \"claude\"\nrole = \"../r.md\"\n":   `profile "p": role must be a relative slash-separated path inside the config directory`,
		"[profile.p]\nadapter = \"claude\"\nrole = 'roles\\r.md'\n": `profile "p": role must be a relative slash-separated path inside the config directory`,
		"[profile.p]\nadapter = \"claude\"\nrole = 'C:/r.md'\n":     `profile "p": role must be a relative slash-separated path inside the config directory`,
		"[profile.p]\nadapter = \"claude\"\ntoken = \"x\"\n":        `unknown keys: profile.p.token`,
	}
	for extra, want := range cases {
		_, err := Parse([]byte(director(t) + extra))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", extra, err, want)
		}
	}
}

// TestProfileAllowedTools: a profile's allowed_tools are the Director's own grant, read in order from its config;
// a profile without them sets none.
func TestProfileAllowedTools(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + `
[profile.builder]
adapter = "claude"
allowed_tools = ["Bash(go test:*)", "Bash(go vet:*)", "Bash(git add:*)", "Bash(git commit:*)", "Bash(git status)", "Bash(git diff:*)", "Read", "mcp__docs__search"]

[profile.reviewer]
adapter = "claude"
`))
	if err != nil {
		t.Fatalf("allowed_tools refused: %v", err)
	}
	want := []string{"Bash(go test:*)", "Bash(go vet:*)", "Bash(git add:*)", "Bash(git commit:*)", "Bash(git status)", "Bash(git diff:*)", "Read", "mcp__docs__search"}
	if got := cfg.Profiles["builder"].AllowedToolsList(); !slices.Equal(got, want) {
		t.Errorf("builder allowed_tools = %q, want %q", got, want)
	}
	if p := cfg.Profiles["reviewer"]; p.AllowedToolsSet() || len(p.AllowedToolsList()) != 0 {
		t.Errorf("reviewer allowed_tools = %q, set %v, want none and unset", p.AllowedToolsList(), p.AllowedToolsSet())
	}
}

// TestProfileAllowedToolsEmptyIsSet: allowed_tools = [] is set, granting nothing in place of a shipped default, and
// differs from an omitted key; the difference survives a JSON round trip, while a config that sets no empty list
// marshals as it did before the key could be told apart.
func TestProfileAllowedToolsEmptyIsSet(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + `
[profile.builder]
adapter = "claude"
allowed_tools = []

[profile.reviewer]
adapter = "claude"

[profile.planner]
adapter = "claude"
allowed_tools = ["Read"]
`))
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string, cfg *Config) {
		t.Helper()
		if p := cfg.Profiles["builder"]; !p.AllowedToolsSet() || len(p.AllowedToolsList()) != 0 {
			t.Errorf("%s: builder set %v %q, want set and empty", when, p.AllowedToolsSet(), p.AllowedToolsList())
		}
		if p := cfg.Profiles["reviewer"]; p.AllowedToolsSet() {
			t.Errorf("%s: reviewer set %q, want unset", when, p.AllowedToolsList())
		}
		if p := cfg.Profiles["planner"]; !p.AllowedToolsSet() || !slices.Equal(p.AllowedToolsList(), []string{"Read"}) {
			t.Errorf("%s: planner %q, want [Read]", when, p.AllowedToolsList())
		}
	}
	check("parsed", cfg)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	check("round trip", &back)
	for name, want := range map[string]string{"builder": `"AllowedTools":[]`, "planner": `"AllowedTools":["Read"]`} {
		got, _ := json.Marshal(cfg.Profiles[name])
		if !strings.Contains(string(got), want) {
			t.Errorf("%s marshals as %s, want %s", name, got, want)
		}
	}
	if got, _ := json.Marshal(cfg.Profiles["reviewer"]); strings.Contains(string(got), "AllowedTools") {
		t.Errorf("unset reviewer marshals as %s, want no AllowedTools", got)
	}
}

// TestRepositoryProfileGrant: [repository.profiles.<name>] under a [[repository]] entry adds rules to that declared
// claude profile for that repository only.
func TestRepositoryProfileGrant(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + `
[repository.profiles.builder]
allowed_tools = ["Bash(go test:*)", "Bash(go vet:*)"]

[profile.builder]
adapter = "claude"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Repositories[1].Profiles["builder"].AllowedTools; !slices.Equal(got, []string{"Bash(go test:*)", "Bash(go vet:*)"}) {
		t.Errorf("site builder grant = %q", got)
	}
	if got := cfg.Repositories[0].Profiles; got != nil {
		t.Errorf("tool grants = %v, want none", got)
	}
}

// TestInvalidRepositoryProfileGrantRefused: a repository grant is refused for a bad or repeated rule, a profile the
// copy does not declare, a profile of another adapter or an unknown key.
func TestInvalidRepositoryProfileGrantRefused(t *testing.T) {
	cases := map[string]string{
		"[repository.profiles.builder]\nallowed_tools = [\"Bash(*)\"]\n":                `repository "site": profile "builder": allowed_tools: "Bash(*)" allows every shell command`,
		"[repository.profiles.builder]\nallowed_tools = [\"go test\"]\n":                `repository "site": profile "builder": allowed_tools: `,
		"[repository.profiles.builder]\nallowed_tools = [\"Read\", \"Read\"]\n":         `repository "site": profile "builder": allowed_tools lists "Read" twice`,
		"[repository.profiles.biulder]\nallowed_tools = [\"Read\"]\n":                   `repository "site": profile "biulder": the copy declares no such profile`,
		"[repository.profiles.faker]\nallowed_tools = [\"Read\"]\n":                     `repository "site": profile "faker": runs the fake adapter, which grants no allowed_tools`,
		"[repository.profiles.builder]\nallowed_tools = [\"Read\"]\nmodel = \"opus\"\n": `unknown keys: repository.profiles.builder.model`,
		"[repository.profiles.builder]\nallowed_tool = [\"Read\"]\n":                    `unknown keys: repository.profiles.builder.allowed_tool`,
	}
	for extra, want := range cases {
		_, err := Parse([]byte(director(t) + extra + "\n[profile.builder]\nadapter = \"claude\"\n\n[profile.faker]\nadapter = \"fake\"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", extra, err, want)
		}
	}
}

// TestInvalidAllowedToolsRefused: each allowed_tools entry is a narrow rule; a malformed rule, a duplicate or a rule
// allowing every shell command is refused naming the profile.
func TestInvalidAllowedToolsRefused(t *testing.T) {
	cases := map[string]string{
		`allowed_tools = [""]`:                        `profile "p": allowed_tools: `,
		`allowed_tools = ["Bash(go test:*"]`:          `profile "p": allowed_tools: `,
		`allowed_tools = ["go test"]`:                 `profile "p": allowed_tools: `,
		`allowed_tools = ["Bash()"]`:                  `profile "p": allowed_tools: `,
		`allowed_tools = ["Bash(go test)x"]`:          `profile "p": allowed_tools: `,
		`allowed_tools = ["Bash(go test\nrm -rf /)"]`: `profile "p": allowed_tools: `,
		`allowed_tools = ["Bash"]`:                    `every shell command`,
		`allowed_tools = ["Bash(*)"]`:                 `every shell command`,
		`allowed_tools = ["Bash(:*)"]`:                `every shell command`,
		`allowed_tools = ["Read", "Read"]`:            `profile "p": allowed_tools lists "Read" twice`,
		`allowed_tools = "Bash(go test:*)"`:           `allowed_tools`,
	}
	for extra, want := range cases {
		_, err := Parse([]byte(director(t) + "[profile.p]\nadapter = \"claude\"\n" + extra + "\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", extra, err, want)
		}
	}
}

// TestShippedProfiles: a parsed Director config has every shipped role profile it does not declare, each on the
// claude adapter with nothing else set but designer's effort high; a copy section replaces the shipped profile as a
// whole, so a copy designer without effort runs at default_effort; and every check of a declared profile accepts the
// shipped names. A Steward config gets none.
func TestShippedProfiles(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"researcher", "planner", "builder", "reviewer", "rebaser"} {
		p, err := cfg.Profile(name)
		if err != nil || !reflect.DeepEqual(p, Profile{Adapter: "claude"}) {
			t.Errorf("Profile(%s) = %+v, %v; want the shipped claude profile", name, p, err)
		}
	}
	if p, err := cfg.Profile("designer"); err != nil || !reflect.DeepEqual(p, Profile{Adapter: "claude", Effort: "high"}) || cfg.EffectiveEffort(p.Effort) != "high" {
		t.Errorf("Profile(designer) = %+v, %v; want the shipped claude profile at effort high", p, err)
	}
	if len(cfg.Profiles) != len(ShippedProfiles) {
		t.Errorf("profiles = %v, want only the shipped ones", cfg.Profiles)
	}

	cfg, err = Parse([]byte(director(t) + "\n[profile.designer]\nadapter = \"claude\"\nmodel = \"m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Profiles["designer"]; !reflect.DeepEqual(p, Profile{Adapter: "claude", Model: "m"}) || cfg.EffectiveEffort(p.Effort) != cfg.EffectiveEffort("") {
		t.Errorf("designer = %+v, want the copy's section alone, at default_effort", p)
	}

	cfg, err = Parse([]byte(director(t) + "\n[profile.reviewer]\nadapter = \"fake\"\nmodel = \"m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Profiles["reviewer"]; !reflect.DeepEqual(p, Profile{Adapter: "fake", Model: "m"}) {
		t.Errorf("reviewer = %+v, want the copy's section alone", p)
	}
	if ShippedProfiles["reviewer"] != (Profile{Adapter: "claude"}) {
		t.Errorf("a copy section changed the shipped reviewer: %+v", ShippedProfiles["reviewer"])
	}

	grants := strings.Replace(director(t), "[local]\n", "[local]\nclaude_skills_profile_reviewer = '{\"preset\":\"reviewer-v1\"}'\nquality_judge_profiles = \"reviewer, builder\"\n", 1)
	grants = strings.Replace(grants, "[[repository]]\nid = \"site\"", "[repository.profiles.builder]\nallowed_tools = [\"Bash(go test:*)\"]\n\n[repository.profiles.reviewer]\nallowed_tools = [\"Bash(go test:*)\"]\n\n[repository.profiles.rebaser]\nallowed_tools = [\"Bash(go test:*)\"]\n\n[[repository]]\nid = \"site\"", 1)
	cfg, err = Parse([]byte(grants))
	if err != nil {
		t.Fatalf("shipped names refused in repository grants, skill policy or judge profiles: %v", err)
	}
	if got := cfg.Repositories[0].Profiles["rebaser"].AllowedTools; !slices.Equal(got, []string{"Bash(go test:*)"}) {
		t.Errorf("rebaser repository grant = %q", got)
	}
	if q, err := cfg.Quality(); err != nil || !slices.Equal(q.JudgeProfiles, []string{"reviewer", "builder"}) {
		t.Errorf("Quality() = %+v, %v", q, err)
	}
	if _, err := cfg.ClaudeSkills("reviewer"); err != nil {
		t.Errorf("ClaudeSkills(reviewer): %v", err)
	}

	steward, err := Parse([]byte("schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"git@github.com:example/guildhall-config.git\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(steward.Profiles) != 0 {
		t.Errorf("steward profiles = %v, want none", steward.Profiles)
	}
}

func TestStewardHasNoProfiles(t *testing.T) {
	data := `schema_version = 1
mode = "steward"

[private_config]
location = "git@github.com:example/guildhall-config.git"

[profile.p]
adapter = "claude"
`
	_, err := Parse([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "profiles belong to a director, not the steward") {
		t.Fatalf("steward profile accepted: %v", err)
	}
}
