package config

import (
	"encoding/json"
	"errors"
	"maps"
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

// TestCodexRefusedOnEveryWorkflowStep: a codex profile is refused on every workflow step kind, named or by role,
// with a message naming the step, the profile and the read-only reason, since no kind is yet admitted as read-only;
// admitting a kind admits a codex profile on it and on nothing else.
func TestCodexRefusedOnEveryWorkflowStep(t *testing.T) {
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, s := range def.Steps {
		if s.Actor() == workflow.ArtisanActor {
			kinds[s.Type] = true
		}
	}
	for _, kind := range []string{"build", "review", "plan"} {
		if !kinds[kind] {
			t.Fatalf("the default workflow has no %s step", kind)
		}
	}
	base := &Config{Profiles: map[string]Profile{"researcher": {Adapter: "fake"}, "planner": {Adapter: "fake"}, "builder": {Adapter: "fake"},
		"reviewer": {Adapter: "fake"}, "rebaser": {Adapter: "fake"}, "judge-codex": {Adapter: "codex"}}}
	if err := base.CheckArtisanAdapters(def); err != nil {
		t.Fatalf("no step on codex: %v", err)
	}
	for i, s := range def.Steps {
		if s.Actor() != workflow.ArtisanActor {
			continue
		}
		t.Run(s.ID+" by profile", func(t *testing.T) {
			named, err := workflow.Default()
			if err != nil {
				t.Fatal(err)
			}
			if named.Steps[i].Settings == nil {
				named.Steps[i].Settings = map[string]json.RawMessage{}
			}
			named.Steps[i].Settings["profile"] = json.RawMessage(`"judge-codex"`)
			err = base.CheckArtisanAdapters(named)
			if !errors.Is(err, ErrReadOnlyAdapter) || !strings.Contains(err.Error(), `step "`+s.ID+`": profile judge-codex`) || !strings.Contains(err.Error(), "a "+s.Type+" step may write") {
				t.Errorf("err = %v, want ErrReadOnlyAdapter naming the step, profile and kind", err)
			}
		})
		role := workflow.EffectiveProfile(s)
		if role == "" {
			continue
		}
		t.Run(s.ID+" by role", func(t *testing.T) {
			c := &Config{Profiles: maps.Clone(base.Profiles)}
			c.Profiles[role] = Profile{Adapter: "codex"}
			if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrReadOnlyAdapter) {
				t.Errorf("err = %v, want ErrReadOnlyAdapter", err)
			}
		})
	}
	// The engine runs rebase and design steps under the rebaser and designer profiles, with no workflow step.
	for _, role := range []string{"rebaser", "designer"} {
		t.Run(role, func(t *testing.T) {
			c := &Config{Profiles: maps.Clone(base.Profiles)}
			c.Profiles[role] = Profile{Adapter: "codex"}
			if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrReadOnlyAdapter) || !strings.Contains(err.Error(), "profile "+role+" runs the engine's") {
				t.Errorf("err = %v, want ErrReadOnlyAdapter naming %s", err, role)
			}
		})
	}
	t.Run("an admitted kind", func(t *testing.T) {
		saved := codexStepKinds
		t.Cleanup(func() { codexStepKinds = saved })
		codexStepKinds = []string{"review"}
		c := &Config{Profiles: maps.Clone(base.Profiles)}
		c.Profiles["reviewer"] = Profile{Adapter: "codex"}
		if err := c.CheckArtisanAdapters(def); err != nil {
			t.Errorf("codex reviewer with review admitted: %v", err)
		}
		c.Profiles["builder"] = Profile{Adapter: "codex"}
		if err := c.CheckArtisanAdapters(def); !errors.Is(err, ErrReadOnlyAdapter) || !strings.Contains(err.Error(), "a build step may write") {
			t.Errorf("codex builder with review admitted: %v", err)
		}
	})
}

func TestShippedDirectorSelectsRealArtisan(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	c, err := LoadUnadmitted(filepath.Join(root, "director.toml"))
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
