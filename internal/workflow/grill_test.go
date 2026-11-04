package workflow

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestGrillSkillsValidation(t *testing.T) {
	for _, actor := range []Actor{DirectorActor, ArtisanActor} {
		for _, tc := range []struct {
			name, settings string
			valid          bool
		}{
			{"skills", `{"skills":["guildhall-grill-unattended"]}`, true},
			{"empty", `{"skills":[]}`, true},
			{"omitted", `{}`, true},
			{"string", `{"skills":"guildhall-grill-unattended"}`, false},
			{"null", `{"skills":null}`, false},
			{"invalid-name", `{"skills":["../skill"]}`, false},
			{"duplicate", `{"skills":["skill","skill"]}`, false},
			{"profile", `{"profile":"reviewer"}`, false},
			{"instructions", `{"instructions":"notes"}`, false},
			{"unknown", `{"unknown":true}`, false},
		} {
			t.Run(string(actor)+"/"+tc.name, func(t *testing.T) {
				var settings map[string]json.RawMessage
				if err := json.Unmarshal([]byte(tc.settings), &settings); err != nil {
					t.Fatal(err)
				}
				rawActor, err := json.Marshal(actor)
				if err != nil {
					t.Fatal(err)
				}
				settings[ActorSetting] = rawActor
				step := Step{ID: "grill", Type: "grill", Version: 1, Settings: settings}
				for name, err := range map[string]error{
					"step":     ValidateStepSettings(step),
					"workflow": Validate(&Definition{Name: "grill-skills", Steps: []Step{step}}),
				} {
					if (err == nil) != tc.valid {
						t.Errorf("%s validation = %v, want valid %v", name, err, tc.valid)
					}
				}
				if tc.name == "skills" && !slices.Equal(step.Skills(), []string{"guildhall-grill-unattended"}) {
					t.Errorf("skills = %q", step.Skills())
				}
			})
		}
	}
	for _, raw := range []string{`null`, `42`, `"guildmaster"`, `"engine"`, `""`} {
		step := Step{ID: "grill", Type: "grill", Version: 1, Settings: map[string]json.RawMessage{ActorSetting: json.RawMessage(raw)}}
		if err := ValidateStepSettings(step); err == nil {
			t.Errorf("accepted actor %s", raw)
		}
	}
	if err := ValidateStepSettings(Step{ID: "grill", Type: "grill", Version: 1}); err == nil {
		t.Error("accepted missing actor")
	}
}

// TestDefaultGrillIsTheDirectorsLiveGrill: the shipped workflow's first stage is the Director's grill with the live
// grill's two skills (docs/decisions/grill-live-interview.md).
func TestDefaultGrillIsTheDirectorsLiveGrill(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	grill := def.Steps[0]
	if grill.ID != "grill" || grill.Actor() != DirectorActor || !slices.Equal(grill.Skills(), []string{"guildhall-grill", "guildhall-domain-model"}) {
		t.Errorf("default first step %s, actor %s, skills %q; want the Director's grill with guildhall-grill and guildhall-domain-model", grill.ID, grill.Actor(), grill.Skills())
	}
}

// TestApplyGrillModeUnattended: an unattended grill mode makes the shipped workflow's Director grill an Artisan step
// with the unattended skills, run under the researcher profile; every other setting, the wiring and the definition it
// was given, digest and all, stay as they were.
func TestApplyGrillModeUnattended(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	bound := def.Digest()
	got, applied := ApplyGrillMode(def, GrillUnattended)
	if !applied {
		t.Fatal("unattended mode applied nothing to the default workflow")
	}
	if def.Digest() != bound || def.Steps[0].Actor() != DirectorActor {
		t.Errorf("the bound definition changed: digest %s, grill actor %s", def.Digest(), def.Steps[0].Actor())
	}
	grill := got.Steps[0]
	if grill.ID != "grill" || grill.Actor() != ArtisanActor || !slices.Equal(grill.Skills(), UnattendedGrillSkills) || EffectiveProfile(grill) != "researcher" {
		t.Errorf("grill %s, actor %s, skills %q, profile %q; want an Artisan grill with %q under researcher", grill.ID, grill.Actor(), grill.Skills(), EffectiveProfile(grill), UnattendedGrillSkills)
	}
	if err := Validate(got); err != nil {
		t.Errorf("unattended default does not validate: %v", err)
	}
	if len(grill.Settings) != 2 {
		t.Errorf("grill settings = %v, want only actor and skills", grill.Settings)
	}
	for i := 1; i < len(def.Steps); i++ {
		a, err := json.Marshal(def.Steps[i])
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(got.Steps[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("step %s changed: %s, want %s", def.Steps[i].ID, b, a)
		}
	}
	if DirectorGrill(got) != -1 {
		t.Error("the unattended definition still has a Director grill")
	}
}

// TestApplyGrillModeLeavesTheRest: a live mode, a bug's workflow and a grill a repository already runs by an Artisan
// are given back unchanged.
func TestApplyGrillModeLeavesTheRest(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	bug, err := Bug()
	if err != nil {
		t.Fatal(err)
	}
	artisan, err := ApplyOverrides(def, map[string]Override{"grill": {Actor: ArtisanActor}})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		def  *Definition
		mode GrillMode
	}{
		"live":             {def, GrillLive},
		"bug":              {bug, GrillUnattended},
		"artisan override": {artisan, GrillUnattended},
	} {
		if got, applied := ApplyGrillMode(c.def, c.mode); applied || got != c.def {
			t.Errorf("%s: applied %v, definition %p, want %p unchanged", name, applied, got, c.def)
		}
	}
	if DirectorGrill(def) != 0 || DirectorGrill(bug) != -1 || DirectorGrill(artisan) != -1 {
		t.Errorf("DirectorGrill = %d, %d, %d; want 0 for the default and -1 for the bug and the override", DirectorGrill(def), DirectorGrill(bug), DirectorGrill(artisan))
	}
}

// TestLegacyDefaultsLeaveAnArtisanGrill: the legacy profile defaults never write a profile into a grill, whose kind
// takes none, so an Artisan grill's legacy variant is itself.
func TestLegacyDefaultsLeaveAnArtisanGrill(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	unattended, _ := ApplyGrillMode(def, GrillUnattended)
	if p := ApplyProfileDefaults(unattended, func(string) bool { return true }).Steps[0].Profile(); p != "" {
		t.Errorf("legacy defaults gave the Artisan grill profile %q", p)
	}
}
