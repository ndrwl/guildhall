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
		data, err := os.ReadFile(filepath.Join(root, "workflows", repo.Workflow+".json"))
		if err != nil {
			t.Fatal(err)
		}
		def, err := workflow.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		def, err = workflow.ApplyOverrides(def, repo.WorkflowOverrides)
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
			if step.Actor() == workflow.ArtisanActor && c.Profiles[step.Profile()].Adapter != "claude" {
				t.Fatalf("%s/%s does not select Claude", repo.ID, step.ID)
			}
		}
	}
}
