package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestArtisanAdapterAdmission(t *testing.T) {
	for _, name := range []string{"fake", "claude", "mock"} {
		t.Run(name, func(t *testing.T) {
			def, err := workflow.Default()
			if err != nil {
				t.Fatal(err)
			}
			c := &Config{Local: map[string]string{"default_adapter": name}, Profiles: map[string]Profile{"explicit": {Adapter: name}}}
			if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrMissingAdapter) || !strings.Contains(err.Error(), `step "research"`) || !strings.Contains(err.Error(), "custom workflow") {
				t.Fatalf("missing: %v", err)
			}
			for i := range def.Steps {
				if def.Steps[i].Actor() == workflow.ArtisanActor {
					if def.Steps[i].Settings == nil {
						def.Steps[i].Settings = map[string]json.RawMessage{}
					}
					def.Steps[i].Settings["profile"] = json.RawMessage(`"explicit"`)
				}
			}
			if err := c.CheckArtisanAdapters(def); err != nil {
				t.Fatalf("explicit profiles: %v", err)
			}
			for i := range def.Steps {
				if def.Steps[i].Actor() != workflow.ArtisanActor {
					continue
				}
				saved := def.Steps[i].Settings["profile"]
				delete(def.Steps[i].Settings, "profile")
				if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrMissingAdapter) || !strings.Contains(err.Error(), `step "`+def.Steps[i].ID+`"`) {
					t.Fatalf("missing %s: %v", def.Steps[i].ID, err)
				}
				def.Steps[i].Settings["profile"] = saved
			}
			delete(c.Profiles, "explicit")
			if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrUnknownProfile) {
				t.Fatalf("unknown: %v", err)
			}
		})
	}
}

func TestShippedDirectorSelectsRealArtisan(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	c, err := Load(filepath.Join(root, "director.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range c.Repositories {
		for _, taskKind := range TaskKinds {
			name, _ := repo.TaskKindWorkflow(taskKind)
			var def *workflow.Definition
			switch name {
			case DefaultWorkflow:
				def, err = workflow.Default()
			case TaskKindBug:
				def, err = workflow.Bug()
			case TaskKindHallmark:
				def, err = workflow.Hallmark()
			default:
				var data []byte
				if data, err = os.ReadFile(filepath.Join(root, "workflows", name+".json")); err == nil {
					def, err = workflow.Parse(data)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			overrides := repo.WorkflowOverrides
			if taskKind != TaskKindFeature {
				overrides = workflow.OwnOverrides(def, overrides)
			}
			def, err = workflow.ApplyOverrides(def, overrides)
			if err != nil {
				t.Fatal(err)
			}
			if err := workflow.Validate(def); err != nil {
				t.Fatal(err)
			}
			if err := c.CheckArtisanAdapters(def); err != nil {
				t.Fatal(err)
			}
			for _, step := range def.Steps {
				if step.Actor() == workflow.ArtisanActor && c.Profiles[workflow.EffectiveProfile(step)].Adapter != "claude" {
					t.Fatalf("%s %s/%s does not select Claude", taskKind, repo.ID, step.ID)
				}
			}
		}
	}
}

// TestArtisanAdapterRemedyNamesTheRoleProfile: a config built without the shipped profiles, as in-memory test configs
// are, refuses a role step whose role profile it lacks with a remedy naming that profile; a step type with no role
// profile keeps the custom remedy, even in a parsed Director config, which has every role profile.
func TestArtisanAdapterRemedyNamesTheRoleProfile(t *testing.T) {
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	c := &Config{Profiles: map[string]Profile{"researcher": {Adapter: "fake"}, "planner": {Adapter: "fake"}, "builder": {Adapter: "fake"}}}
	err = c.CheckArtisanAdapters(def)
	if !errors.Is(err, ErrMissingAdapter) || !strings.Contains(err.Error(), `step "review"`) || !strings.Contains(err.Error(), "declare [profile.reviewer] with adapter, or set a profile on that step") {
		t.Fatalf("missing reviewer: %v", err)
	}
	refute := &workflow.Definition{Schema: "guildhall.workflow/1", Name: "refuting", Steps: []workflow.Step{{ID: "refute", Type: "refute", Version: 1}}}
	if err := c.CheckArtisanAdapters(refute); !errors.Is(err, ErrMissingAdapter) || !strings.Contains(err.Error(), "custom workflow, referencing a declared [profile.<name>]") {
		t.Fatalf("refute: %v", err)
	}
	parsed, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.CheckArtisanAdapters(def); err != nil {
		t.Fatalf("a parsed Director config with no [profile.*] refused the default workflow: %v", err)
	}
	if err := parsed.CheckArtisanAdapters(refute); !errors.Is(err, ErrMissingAdapter) {
		t.Fatalf("refute with the shipped profiles: %v", err)
	}
}
