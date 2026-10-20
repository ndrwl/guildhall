package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// director returns a valid Director config whose repository paths are absolute on the host OS.
func director(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	return `schema_version = 1
mode = "director"
name = "alpha"

[managed.standards_revision]
value = "standards-1"

[managed.review_instructions]
value = "Cite the requirement for every finding."
overridable = true

[local]
review_instructions = "Also check Windows paths."
notes_dir = "notes"

[[repository]]
id = "tool"
path = '` + filepath.Join(root, "tool") + `'
remote = "git@github.com:example/tool.git"
owner_machine = "workstation"

[[repository]]
id = "site"
path = '` + filepath.Join(root, "site") + `'
remote = "https://github.com/example/site.git"
owner_machine = "workstation"
merge_policy = "auto"
workflow = "careful"

[repository.workflow_overrides.review]
type = "human-review"
version = 1
`
}

func TestDirectorConfig(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatalf("valid director config refused: %v", err)
	}
	if cfg.Mode != ModeDirector || cfg.Name != "alpha" || len(cfg.Repositories) != 2 {
		t.Fatalf("unexpected config %+v", cfg)
	}
	tool, site := cfg.Repositories[0], cfg.Repositories[1]
	if tool.MergePolicy != MergeApproval {
		t.Errorf("default merge policy = %q, want approval", tool.MergePolicy)
	}
	if tool.Workflow != DefaultWorkflow {
		t.Errorf("default workflow = %q", tool.Workflow)
	}
	if site.MergePolicy != MergeAuto || site.Workflow != "careful" {
		t.Errorf("explicit repository settings lost: %+v", site)
	}
	if got := site.WorkflowOverrides["review"]; got != (workflow.Override{Type: "human-review", Version: 1}) {
		t.Errorf("workflow override = %+v", got)
	}
	if r, ok := cfg.Repository("site"); !ok || r.ID != "site" {
		t.Error("repository lookup failed")
	}
}

func TestSettingPrecedence(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"standards_revision":  "standards-1",
		"review_instructions": "Also check Windows paths.",
		"notes_dir":           "notes",
	}
	for key, want := range cases {
		if got, ok := cfg.Setting(key); !ok || got != want {
			t.Errorf("Setting(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
	if _, ok := cfg.Setting("absent"); ok {
		t.Error("absent setting found")
	}
}

func TestLocalCannotOverrideLockedManagedKey(t *testing.T) {
	data := strings.Replace(director(t), `notes_dir = "notes"`, `standards_revision = "mine"`, 1)
	_, err := Parse([]byte(data))
	if err == nil || !strings.Contains(err.Error(), `local "standards_revision" overrides a managed key that is not overridable`) {
		t.Fatalf("locked managed key overridden: %v", err)
	}
}

func TestStewardConfig(t *testing.T) {
	data := `schema_version = 1
mode = "steward"

[private_config]
location = "git@github.com:example/guildhall-config.git"
`
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("valid steward config refused: %v", err)
	}
	if cfg.Mode != ModeSteward || cfg.PrivateConfig == nil || cfg.PrivateConfig.Location == "" {
		t.Fatalf("unexpected steward config %+v", cfg)
	}
}

func TestSchemaRefusals(t *testing.T) {
	newer := strings.Replace(director(t), "schema_version = 1", "schema_version = 2", 1)
	if _, err := Parse([]byte(newer)); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer schema: %v", err)
	}
	for name, data := range map[string]string{
		"missing schema": strings.Replace(director(t), "schema_version = 1\n", "", 1),
		"zero schema":    strings.Replace(director(t), "schema_version = 1", "schema_version = 0", 1),
		"malformed toml": "schema_version = 1\nmode = \"director",
		"unknown key":    strings.Replace(director(t), `name = "alpha"`, "name = \"alpha\"\nauto_merge_everything = true", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(data))
			if err == nil || errors.Is(err, ErrNewerSchema) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestValidationRefusals(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"unknown mode", `mode = "director"`, `mode = "guildmaster"`, `mode must be "steward" or "director"`},
		{"director without name", `name = "alpha"`, ``, `director needs a name`},
		{"director with private config", `name = "alpha"`, "name = \"alpha\"\n[private_config]\nlocation = \"x\"", `private_config is for the steward only`},
		{"unknown merge policy", `merge_policy = "auto"`, `merge_policy = "always"`, `merge_policy must be "approval" or "auto"`},
		{"duplicate repository", `id = "site"`, `id = "tool"`, `duplicate repository id "tool"`},
		{"invalid repository id", `id = "site"`, `id = "My Site"`, `invalid repository id`},
		{"relative path", `path = '`, `path = 'relative/`, `path must be absolute`},
		{"second owner machine", `owner_machine = "workstation"
merge_policy`, `owner_machine = "laptop"
merge_policy`, `one owner machine`},
		{"missing remote", `remote = "git@github.com:example/tool.git"`, ``, `remote is required`},
		{"credential in remote", `https://github.com/example/site.git`, `https://user:hunter2@github.com/example/site.git`, `must not contain credentials`},
		{"credential-like key", `notes_dir = "notes"`, `github_token = "x"`, `looks like a credential`},
		{"invalid workflow name", `workflow = "careful"`, `workflow = "../careful"`, `invalid workflow name`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := director(t)
			if !strings.Contains(base, c.old) {
				t.Fatalf("fixture does not contain %q", c.old)
			}
			_, err := Parse([]byte(strings.Replace(base, c.old, c.new, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

func TestStewardRefusals(t *testing.T) {
	cases := map[string]string{
		"missing private config":  "schema_version = 1\nmode = \"steward\"\n",
		"credential in reference": "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"https://x:secret@example.com/config.git\"\n",
		"repositories listed":     "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"x\"\n[[repository]]\nid = \"a\"\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "director.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// The example uses a POSIX path; substitute a host-absolute one so the test runs on every OS.
	text := strings.ReplaceAll(string(data), "'/srv/src/", "'"+t.TempDir()+string(filepath.Separator))
	if _, err := Parse([]byte(text)); err != nil {
		t.Fatalf("example config refused: %v", err)
	}
}
