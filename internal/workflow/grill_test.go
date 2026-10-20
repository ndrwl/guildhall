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
			{"skills", `{"skills":["guildhall-grill-notes"]}`, true},
			{"empty", `{"skills":[]}`, true},
			{"omitted", `{}`, true},
			{"string", `{"skills":"guildhall-grill-notes"}`, false},
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
				if tc.name == "skills" && !slices.Equal(step.Skills(), []string{"guildhall-grill-notes"}) {
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
