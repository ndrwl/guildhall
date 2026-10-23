package config

import (
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
	  {"id": "build", "type": "build", "version": 1, "settings": {"profile": "builder"}},
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
	for _, want := range []string{`step "build" names unknown profile "builder"`, `step "review-c" names unknown profile "reviewer-strict"`} {
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
// a profile without them grants nothing.
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
	if got := cfg.Profiles["builder"].AllowedTools; !slices.Equal(got, want) {
		t.Errorf("builder allowed_tools = %q, want %q", got, want)
	}
	if got := cfg.Profiles["reviewer"].AllowedTools; len(got) != 0 {
		t.Errorf("reviewer allowed_tools = %q, want none", got)
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
