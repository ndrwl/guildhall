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
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fake", "claude", "mock"} {
		c := &Config{}
		if err := c.CheckArtisanAdapters(def, name); err != nil {
			t.Errorf("explicit %s: %v", name, err)
		}
	}
	c := &Config{}
	if err := c.CheckArtisanAdapters(def, ""); !errors.Is(err, ErrMissingAdapter) || !strings.Contains(err.Error(), `step "research"`) {
		t.Fatalf("missing: %v", err)
	}
	c.Profiles = map[string]Profile{"demo": {Adapter: "fake"}}
	for i := range def.Steps {
		if def.Steps[i].Actor() == workflow.ArtisanActor {
			if def.Steps[i].Settings == nil {
				def.Steps[i].Settings = map[string]json.RawMessage{}
			}
			def.Steps[i].Settings["profile"] = json.RawMessage(`"demo"`)
		}
	}
	if err := c.CheckArtisanAdapters(def, ""); err != nil {
		t.Fatalf("explicit fake profiles: %v", err)
	}
	delete(c.Profiles, "demo")
	if err := c.CheckArtisanAdapters(def, "fake"); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("default masked bad profile: %v", err)
	}
}

func TestShippedDirectorSelectsRealArtisan(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "director.toml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultAdapter() != "claude" {
		t.Fatalf("shipped default %q", c.DefaultAdapter())
	}
	if err := c.CheckArtisanAdapters(def, c.DefaultAdapter()); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidDefaultAdapter(t *testing.T) {
	for _, value := range []string{"", "Claude Code", " fake", "fake "} {
		for _, section := range []string{"local", "managed"} {
			base := strings.Split(director(t), "[managed.")[0]
			suffix := "\n[local]\ndefault_adapter = '" + value + "'\n"
			if section == "managed" {
				suffix = "\n[managed.default_adapter]\nvalue = '" + value + "'\noverridable = true\n[local]\ndefault_adapter = 'fake'\n"
			}
			if _, err := Parse([]byte(base + suffix)); err == nil || !strings.Contains(err.Error(), DefaultAdapterKey) {
				t.Errorf("%s %q: %v", section, value, err)
			}
		}
	}
}

func TestDefaultAdapterPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed map[string]ManagedSetting
		local   map[string]string
		want    string
	}{
		{name: "unset"},
		{name: "local fake", local: map[string]string{DefaultAdapterKey: "fake"}, want: "fake"},
		{name: "managed", managed: map[string]ManagedSetting{DefaultAdapterKey: {Value: "claude"}}, local: map[string]string{DefaultAdapterKey: "fake"}, want: "claude"},
		{name: "overridable", managed: map[string]ManagedSetting{DefaultAdapterKey: {Value: "claude", Overridable: true}}, local: map[string]string{DefaultAdapterKey: "fake"}, want: "fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Managed: tc.managed, Local: tc.local}
			if got := c.DefaultAdapter(); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
