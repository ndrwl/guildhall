package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
)

// roleProfiles maps each step type that has a role profile to that profile's name.
// Profiles keep role names, so Claude skill policy and quality judge profiles keyed by them are unchanged.
// The rebase and design entries only name the profiles of the interpreter's synthetic rebaser and designer steps.
var roleProfiles = map[string]string{
	"research": "researcher",
	"plan":     "planner",
	"build":    "builder",
	"review":   "reviewer",
	"rebase":   "rebaser",
	"design":   "designer",
}

// RoleProfile returns the role profile of a step of this type, or "" when the type has none.
func RoleProfile(stepType string) string { return roleProfiles[stepType] }

// EffectiveProfile returns the profile step runs under: the profile it names, else, when an Artisan runs it, its
// type's role profile (RoleProfile), else "". The role profile is resolved at use and never written into the
// definition, so it is outside the workflow's digest (docs/decisions/role-profiles-outside-the-digest.md).
func EffectiveProfile(step Step) string {
	if name := step.Profile(); name != "" {
		return name
	}
	if step.Actor() != ArtisanActor {
		return ""
	}
	return RoleProfile(step.Type)
}

// ApplyProfileDefaults returns a copy of def in which every step run by an Artisan, with no "profile" setting and a
// research, plan, build or review type, names that type's role profile when declared reports it declared.
// Every other step is unchanged, an explicit profile always wins and def is not modified.
// It is kept only to recognise the legacy digest a task bound while role profiles were written into the definition
// (docs/decisions/role-profiles-outside-the-digest.md): apply it after ApplyOverrides and compare the result's digest.
// New tasks bind the bare definition, and steps resolve their profile with EffectiveProfile.
func ApplyProfileDefaults(def *Definition, declared func(name string) bool) *Definition {
	out := *def
	out.Steps = append([]Step(nil), def.Steps...)
	for i := range out.Steps {
		step := &out.Steps[i]
		name := RoleProfile(step.Type)
		if _, set := step.Settings["profile"]; set || name == "" || step.Type == "rebase" || step.Type == "design" || step.Actor() != ArtisanActor || !declared(name) {
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
