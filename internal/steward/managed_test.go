package steward

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
)

// copyFixture is a Director copy's config as the Director left it, with comments and local values.
const copyFixture = `# Director copy for tools.
schema_version = 1
mode = "director"
name = "tools"

# Written only by Steward rollouts.
[managed.standards_revision]
value = "standards-1"

[managed.review_instructions]
value = "Cite the requirement."
overridable = true

# Owned by this Director.
[local]
review_instructions = "Cite the requirement and check Windows paths." # kept
notes_dir = "notes"

[[repository]]
id = "cli"
path = '/srv/src/cli'
remote = "git@github.com:example/cli.git"
owner_machine = "workstation"

[[repository]]
id = "site"
path = 'C:/src/site'
remote = "https://github.com/example/site.git"
owner_machine = "workstation"
merge_policy = "auto"
`

const managedFixture = `schema_version = 1

[managed.standards_revision]
value = "standards-2"
policy = true

[managed.review_instructions]
value = "Cite the requirement."
overridable = true

[managed.lint_profile]
value = "strict \"quoted\"\nsecond line"
`

func tools() Target {
	return Target{Machine: "workstation", Director: Director{Name: "tools", CopyDir: "/home/guild/directors/tools", Repositories: []string{"cli", "site"}}}
}

func mustManaged(t *testing.T, data string) *ManagedSource {
	t.Helper()
	src, err := ParseManaged([]byte(data))
	if err != nil {
		t.Fatalf("managed source refused: %v", err)
	}
	return src
}

func TestParseManaged(t *testing.T) {
	src := mustManaged(t, managedFixture)
	want := map[string]config.ManagedSetting{
		"standards_revision":  {Value: "standards-2"},
		"review_instructions": {Value: "Cite the requirement.", Overridable: true},
		"lint_profile":        {Value: "strict \"quoted\"\nsecond line"},
	}
	got := src.Settings()
	if len(got) != len(want) {
		t.Fatalf("settings = %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("setting %q = %+v, want %+v", k, got[k], v)
		}
	}
	for name, c := range map[string]struct{ data, want string }{
		"newer schema":    {strings.Replace(managedFixture, "schema_version = 1", "schema_version = 9", 1), "newer"},
		"unknown key":     {strings.Replace(managedFixture, "policy = true", "policy = true\nforce = true", 1), "unknown keys"},
		"credential key":  {managedFixture + "\n[managed.api_key]\nvalue = \"x\"\n", "looks like a credential"},
		"invalid key":     {managedFixture + "\n[managed.\"Bad Key\"]\nvalue = \"x\"\n", "invalid key"},
		"top-level value": {"schema_version = 1\nmanaged = \"x\"\n", "managed"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManaged([]byte(c.data))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
	if _, err := ParseManaged([]byte(strings.Replace(managedFixture, "schema_version = 1", "schema_version = 9", 1))); !errors.Is(err, config.ErrNewerSchema) {
		t.Errorf("newer managed schema not ErrNewerSchema: %v", err)
	}
}

func TestRenderReplacesOnlyTheManagedSection(t *testing.T) {
	after, changes, err := Render([]byte(copyFixture), mustManaged(t, managedFixture), tools())
	if err != nil {
		t.Fatalf("render refused: %v", err)
	}
	text := string(after)
	// Everything the Director owns survives byte for byte, including comments.
	for _, kept := range []string{
		"# Director copy for tools.\nschema_version = 1\nmode = \"director\"\nname = \"tools\"\n\n# Written only by Steward rollouts.\n[managed.",
		"\n\n# Owned by this Director.\n[local]\nreview_instructions = \"Cite the requirement and check Windows paths.\" # kept\nnotes_dir = \"notes\"\n\n[[repository]]\nid = \"cli\"\n",
		"path = 'C:/src/site'\nremote = \"https://github.com/example/site.git\"\nowner_machine = \"workstation\"\nmerge_policy = \"auto\"\n",
	} {
		if !strings.Contains(text, kept) {
			t.Errorf("rendered config lost %q:\n%s", kept, text)
		}
	}
	cfg, err := config.Parse(after)
	if err != nil {
		t.Fatalf("rendered config invalid: %v", err)
	}
	if got, _ := cfg.Setting("review_instructions"); got != "Cite the requirement and check Windows paths." {
		t.Errorf("local override lost: %q", got)
	}
	if got, _ := cfg.Setting("lint_profile"); got != "strict \"quoted\"\nsecond line" {
		t.Errorf("lint_profile = %q", got)
	}
	wantChanges := []Change{
		{Key: "lint_profile", Kind: Added, After: &config.ManagedSetting{Value: "strict \"quoted\"\nsecond line"}},
		{Key: "standards_revision", Kind: Changed, Before: &config.ManagedSetting{Value: "standards-1"}, After: &config.ManagedSetting{Value: "standards-2"},
			NeedsApproval: true, Reason: "the key is marked policy"},
	}
	if len(changes) != len(wantChanges) {
		t.Fatalf("changes = %+v", changes)
	}
	for i, want := range wantChanges {
		got := changes[i]
		if got.Key != want.Key || got.Kind != want.Kind || got.NeedsApproval != want.NeedsApproval || got.Reason != want.Reason ||
			!sameSetting(got.Before, want.Before) || !sameSetting(got.After, want.After) {
			t.Errorf("change %d = %+v, want %+v", i, got, want)
		}
	}
}

func sameSetting(a, b *config.ManagedSetting) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestRenderIsIdempotent(t *testing.T) {
	src := mustManaged(t, managedFixture)
	once, _, err := Render([]byte(copyFixture), src, tools())
	if err != nil {
		t.Fatal(err)
	}
	twice, changes, err := Render(once, src, tools())
	if err != nil {
		t.Fatal(err)
	}
	if string(twice) != string(once) || len(changes) != 0 {
		t.Fatalf("second render changed the file (%d changes):\n%s\n---\n%s", len(changes), once, twice)
	}
}

func TestRenderKeepsCRLFAndInsertsWhenNoManagedSection(t *testing.T) {
	plain := strings.Replace(copyFixture, "# Written only by Steward rollouts.\n[managed.standards_revision]\nvalue = \"standards-1\"\n\n[managed.review_instructions]\nvalue = \"Cite the requirement.\"\noverridable = true\n\n", "", 1)
	crlf := strings.ReplaceAll(plain, "\n", "\r\n")
	src := mustManaged(t, "schema_version = 1\n[managed.standards_revision]\nvalue = \"standards-2\"\n")
	after, changes, err := Render([]byte(crlf), src, tools())
	if err != nil {
		t.Fatalf("render refused: %v", err)
	}
	if strings.Contains(strings.ReplaceAll(string(after), "\r\n", ""), "\n") {
		t.Errorf("rendered config mixes line endings:\n%q", after)
	}
	if !strings.Contains(string(after), "name = \"tools\"\r\n\r\n[managed.standards_revision]\r\nvalue = \"standards-2\"\r\n\r\n# Owned by this Director.\r\n[local]") {
		t.Errorf("managed table not inserted before the first table:\n%q", after)
	}
	if len(changes) != 1 || changes[0].Kind != Added || changes[0].NeedsApproval {
		t.Errorf("changes = %+v", changes)
	}
	again, _, err := Render(after, src, tools())
	if err != nil || string(again) != string(after) {
		t.Errorf("re-render not idempotent (%v):\n%q", err, again)
	}
}

func TestChangeClassification(t *testing.T) {
	cases := []struct {
		name, managed string
		approval      bool
		reason        string
	}{
		{"value change is routine", "[managed.standards_revision]\nvalue = \"standards-1\"\n[managed.review_instructions]\nvalue = \"Cite more.\"\noverridable = true\n", false, ""},
		{"tightening is routine", "[managed.standards_revision]\nvalue = \"standards-1\"\n[managed.review_instructions]\nvalue = \"Cite the requirement.\"\noverridable = true\n[managed.notes_style]\nvalue = \"short\"\n", false, ""},
		{"removal needs approval", "[managed.standards_revision]\nvalue = \"standards-1\"\n", true, "removing a managed key lets copies set it locally"},
		{"loosening needs approval", "[managed.standards_revision]\nvalue = \"standards-1\"\noverridable = true\n[managed.review_instructions]\nvalue = \"Cite the requirement.\"\noverridable = true\n", true, "making a managed key overridable lets copies replace it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, changes, err := Render([]byte(copyFixture), mustManaged(t, "schema_version = 1\n"+c.managed), tools())
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) != 1 || changes[0].NeedsApproval != c.approval || changes[0].Reason != c.reason {
				t.Fatalf("changes = %+v", changes)
			}
		})
	}
}

func TestRenderRefusals(t *testing.T) {
	lock := strings.Replace(managedFixture, "value = \"Cite the requirement.\"\noverridable = true", "value = \"Cite the requirement.\"", 1)
	cases := []struct {
		name, current, managed, want string
	}{
		// Locking a key the Director overrides locally refuses rather than dropping the local value.
		{"locking a locally overridden key", copyFixture, lock, "overrides a managed key that is not overridable"},
		{"dotted managed key", strings.Replace(copyFixture, "name = \"tools\"\n", "name = \"tools\"\nmanaged.extra = { value = \"x\" }\n", 1), managedFixture, "[managed.<key>] tables"},
		{"wrong director", strings.Replace(copyFixture, `name = "tools"`, `name = "other"`, 1), managedFixture, `names director "other"`},
		{"steward copy", "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"/x\"\n", managedFixture, "not a director copy"},
		{"other owner machine", strings.ReplaceAll(copyFixture, `owner_machine = "workstation"`, `owner_machine = "laptop"`), managedFixture, `owner machine "laptop"`},
		{"unlisted repository", strings.Replace(copyFixture, `id = "site"`, `id = "blog"`, 1), managedFixture, "roster lists repositories cli, site"},
		{"invalid current config", "schema_version = 1\nmode = ", managedFixture, "current config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Render([]byte(c.current), mustManaged(t, c.managed), tools())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}
