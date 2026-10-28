package steward

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
)

const localCopyFixture = `# Director copy for tools.
schema_version = 1
mode = "director"
name = "tools"

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

[repository.merge.requires]
guildmaster_approval = false
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
	if got := src.Settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v, want %+v", got, want)
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

func TestRolloutManagedChanges(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation")
	want := []Change{
		{Key: "lint_profile", Kind: Added, After: &config.ManagedSetting{Value: "strict \"quoted\"\nsecond line"}},
		{Key: "standards_revision", Kind: Changed, Before: &config.ManagedSetting{Value: "standards-1"}, After: &config.ManagedSetting{Value: "standards-2"}, NeedsApproval: true, Reason: "the key is marked policy"},
	}
	if !reflect.DeepEqual(p.Copies[0].Changes, want) {
		t.Fatalf("changes = %+v", p.Copies[0].Changes)
	}
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); err != nil {
		t.Fatal(err)
	}
	if got := s.get("guild-ws", toolsPath); got != localCopyFixture {
		t.Fatal("local config changed")
	}
	cfg, err := config.ParseCombined([]byte(localCopyFixture), []byte(s.get("guild-ws", managedPath(toolsPath))))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"review_instructions": "Cite the requirement and check Windows paths.", "lint_profile": "strict \"quoted\"\nsecond line"} {
		if got, _ := cfg.Setting(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestRolloutChangeClassification(t *testing.T) {
	for _, c := range []struct {
		name, managed string
		approval      bool
		reason        string
	}{
		{"value change is routine", "[managed.standards_revision]\nvalue = \"standards-1\"\n[managed.review_instructions]\nvalue = \"Cite more.\"\noverridable = true\n", false, ""},
		{"addition is routine", "[managed.standards_revision]\nvalue = \"standards-1\"\n[managed.review_instructions]\nvalue = \"Cite the requirement.\"\noverridable = true\n[managed.notes_style]\nvalue = \"short\"\n", false, ""},
		{"removal needs approval", "[managed.standards_revision]\nvalue = \"standards-1\"\n", true, "removing a managed key lets copies set it locally"},
		{"loosening needs approval", "[managed.standards_revision]\nvalue = \"standards-1\"\noverridable = true\n[managed.review_instructions]\nvalue = \"Cite the requirement.\"\noverridable = true\n", true, "making a managed key overridable lets copies replace it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := fleet(t)
			r.Managed = mustManaged(t, "schema_version = 1\n"+c.managed)
			p := dryRun(t, r, "workstation")
			changes := p.Copies[0].Changes
			if p.Copies[0].Status != StatusChange || len(changes) != 1 || changes[0].NeedsApproval != c.approval || changes[0].Reason != c.reason || p.NeedsApproval() != c.approval {
				t.Fatalf("plan = %+v", p)
			}
			s.calls()
			_, err := r.Apply(context.Background(), p.StageDir, "", p.Machines...)
			if c.approval {
				if !errors.Is(err, ErrApprovalRequired) {
					t.Fatalf("approval bypassed: %v", err)
				}
				for _, call := range s.calls() {
					if !strings.Contains(call, "-- guild-") {
						t.Fatal("unapproved write", call)
					}
				}
				if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRolloutOwnershipAndOverrideRefusals(t *testing.T) {
	lock := strings.Replace(managedFixture, "value = \"Cite the requirement.\"\noverridable = true", "value = \"Cite the requirement.\"", 1)
	for _, c := range []struct{ name, current, managed, want string }{
		{"locking a locally overridden key", localCopyFixture, lock, "overrides a managed key that is not overridable"},
		{"wrong director", strings.Replace(localCopyFixture, `name = "tools"`, `name = "other"`, 1), managedFixture, `names director "other"`},
		{"steward copy", "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"/x\"\n", managedFixture, "not a director copy"},
		{"other owner machine", strings.ReplaceAll(localCopyFixture, `owner_machine = "workstation"`, `owner_machine = "laptop"`), managedFixture, `owner machine "laptop"`},
		{"unlisted repository", strings.Replace(localCopyFixture, `id = "site"`, `id = "blog"`, 1), managedFixture, "roster lists repositories cli, site"},
		{"invalid current config", "schema_version = 1\nmode = ", managedFixture, "config:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := fleet(t)
			s.put("guild-ws", toolsPath, c.current)
			r.Managed = mustManaged(t, c.managed)
			p := dryRun(t, r, "workstation")
			if got := p.Copies[0]; got.Status != StatusRefused || !strings.Contains(got.Problem, c.want) {
				t.Fatalf("copy = %+v", got)
			}
			s.calls()
			if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); !errors.Is(err, ErrNotApplicable) {
				t.Fatalf("refused copy applied: %v", err)
			}
			for _, call := range s.calls() {
				if !strings.Contains(call, "-- guild-") {
					t.Fatal("refused apply wrote", call)
				}
			}
		})
	}
}
