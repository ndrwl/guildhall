package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
)

// roleProfiles maps each step type that has a role profile default to that profile's name.
// Profiles keep role names, so Claude skill policy and quality judge profiles keyed by them are unchanged.
var roleProfiles = map[string]string{
	"research": "researcher",
	"plan":     "planner",
	"build":    "builder",
	"review":   "reviewer",
}

// RoleProfile returns the profile name ApplyProfileDefaults gives a step of this type, or "" when the type has none.
func RoleProfile(stepType string) string { return roleProfiles[stepType] }

// ApplyProfileDefaults returns a copy of def in which every step run by an Artisan, with no "profile" setting and a
// type that has a role profile (RoleProfile), names that profile when declared reports it declared.
// Every other step is unchanged, an explicit profile always wins and def is not modified.
// Apply it after ApplyOverrides, so a step overridden to the Guildmaster gets no default, and before Validate;
// the default is part of the result's digest.
func ApplyProfileDefaults(def *Definition, declared func(name string) bool) *Definition {
	out := *def
	out.Steps = append([]Step(nil), def.Steps...)
	for i := range out.Steps {
		step := &out.Steps[i]
		name := RoleProfile(step.Type)
		if _, set := step.Settings["profile"]; set || name == "" || step.Actor() != ArtisanActor || !declared(name) {
			continue
		}
		raw, err := json.Marshal(name)
		if err != nil {
			panic(fmt.Sprintf("workflow: encoding profile %q: %v", name, err))
		}
		step.Settings = maps.Clone(step.Settings)
		if step.Settings == nil {
			step.Settings = map[string]json.RawMessage{}
		}
		step.Settings["profile"] = raw
	}
	return &out
}
