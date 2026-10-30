package workflow

import (
	"encoding/json"
	"slices"
	"testing"
)

func declared(names ...string) func(string) bool {
	return func(n string) bool { return slices.Contains(names, n) }
}

func profiles(def *Definition) map[string]string {
	out := map[string]string{}
	for _, s := range def.Steps {
		if p := s.Profile(); p != "" {
			out[s.ID] = p
		}
	}
	return out
}

// The ApplyProfileDefaults tests pin the legacy digest a task bound while role profiles were written into the
// definition, which boundWorkflow still recognises.

func TestApplyProfileDefaultsByRole(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	before := def.Digest()
	got := ApplyProfileDefaults(def, declared("researcher", "planner", "builder", "reviewer", "unused"))
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"research": "researcher", "plan": "planner", "build": "builder", "review": "reviewer"}
	if p := profiles(got); len(p) != len(want) || p["research"] != "researcher" || p["plan"] != "planner" || p["build"] != "builder" || p["review"] != "reviewer" {
		t.Fatalf("profiles = %v, want %v", p, want)
	}
	if def.Digest() != before || len(profiles(def)) != 0 {
		t.Fatalf("ApplyProfileDefaults modified its input: %v", profiles(def))
	}
	if got.Digest() == before {
		t.Fatal("the defaults are not part of the digest")
	}
	// The Director's grill and the engine's steps have no role and get nothing.
	for _, s := range got.Steps {
		if s.ID == "grill" || s.ID == "publish" {
			if _, set := s.Settings["profile"]; set {
				t.Errorf("step %s got a profile", s.ID)
			}
		}
	}
}

func TestApplyProfileDefaultsOnlyWhereDeclared(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	got := ApplyProfileDefaults(def, declared("builder"))
	if p := profiles(got); len(p) != 1 || p["build"] != "builder" {
		t.Fatalf("profiles = %v, want only build", p)
	}
	if got := ApplyProfileDefaults(def, declared()); got.Digest() != def.Digest() {
		t.Fatal("with nothing declared the definition changed")
	}
}

func TestApplyProfileDefaultsExplicitProfileWins(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for i := range def.Steps {
		if def.Steps[i].ID == "build" {
			def.Steps[i].Settings = map[string]json.RawMessage{"profile": json.RawMessage(`"careful"`)}
		}
	}
	got := ApplyProfileDefaults(def, declared("researcher", "planner", "builder", "reviewer", "careful"))
	if p := profiles(got); p["build"] != "careful" {
		t.Fatalf("build profile = %q, want the explicit careful", p["build"])
	}
}

func TestApplyProfileDefaultsSkipsAGuildmasterReview(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	human, err := ApplyOverrides(def, map[string]Override{"review": {Actor: GuildmasterActor}})
	if err != nil {
		t.Fatal(err)
	}
	got := ApplyProfileDefaults(human, declared("researcher", "planner", "builder", "reviewer"))
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if p := profiles(got); p["review"] != "" || p["build"] != "builder" {
		t.Fatalf("profiles = %v, want none on the Guildmaster's review", p)
	}
}

func TestRoleProfile(t *testing.T) {
	for typ, want := range map[string]string{"research": "researcher", "plan": "planner", "build": "builder", "review": "reviewer", "rebase": "rebaser", "design": "designer", "refute": "", "document-review": "", "grill": ""} {
		if got := RoleProfile(typ); got != want {
			t.Errorf("RoleProfile(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestApplyProfileDefaultsLeavesARebaseStep(t *testing.T) {
	def := &Definition{Name: "x", Steps: []Step{{ID: "r", Type: "rebase", Version: 1}, {ID: "d", Type: "design", Version: 1}}}
	if p := profiles(ApplyProfileDefaults(def, declared("rebaser", "designer"))); len(p) != 0 {
		t.Fatalf("profiles = %v, want none: the legacy defaults never covered rebase or design", p)
	}
}

func TestEffectiveProfile(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	digest := def.Digest()
	step := func(d *Definition, id string) Step {
		for _, s := range d.Steps {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("no step %s", id)
		return Step{}
	}
	for id, want := range map[string]string{"research": "researcher", "plan": "planner", "build": "builder", "review": "reviewer", "grill": "", "publish": ""} {
		if got := EffectiveProfile(step(def, id)); got != want {
			t.Errorf("EffectiveProfile(%s) = %q, want %q", id, got, want)
		}
	}
	if def.Digest() != digest || len(profiles(def)) != 0 {
		t.Fatal("EffectiveProfile wrote into the definition")
	}

	explicit := Step{ID: "build", Type: "build", Version: 1, Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"careful"`)}}
	if got := EffectiveProfile(explicit); got != "careful" {
		t.Errorf("explicit profile: got %q, want careful", got)
	}
	if got := EffectiveProfile(Step{ID: "x-rebase", Type: "rebase", Version: 1}); got != "rebaser" {
		t.Errorf("rebase: got %q, want rebaser", got)
	}
	for _, typ := range []string{"refute", "document-review"} {
		if got := EffectiveProfile(Step{ID: "s", Type: typ, Version: 1}); got != "" {
			t.Errorf("%s has no role, got %q", typ, got)
		}
	}

	human, err := ApplyOverrides(def, map[string]Override{"review": {Actor: GuildmasterActor}})
	if err != nil {
		t.Fatal(err)
	}
	if got := EffectiveProfile(step(human, "review")); got != "" {
		t.Errorf("a Guildmaster review got profile %q", got)
	}
}
