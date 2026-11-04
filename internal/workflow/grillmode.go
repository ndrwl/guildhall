package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// GrillMode is how a task's grill runs (docs/tasks.md#grill-mode): live, the Director interviewing the Guildmaster, or
// unattended, an Artisan writing each open decision with the answer it proceeds on.
type GrillMode string

const (
	GrillLive       GrillMode = "live"
	GrillUnattended GrillMode = "unattended"
)

// GrillModes are the grill modes, in documented order.
var GrillModes = []GrillMode{GrillLive, GrillUnattended}

// UnattendedGrillSkills are the skills an unattended grill runs with, in place of the live grill's.
var UnattendedGrillSkills = []string{"guildhall-grill-unattended", "guildhall-domain-model"}

// DirectorGrill returns the index of def's grill step run by the Director, the step a task's grill mode applies to,
// or -1 when def has none: a bug's workflow, or one whose repository overrides the grill's actor.
func DirectorGrill(def *Definition) int {
	return slices.IndexFunc(def.Steps, func(s Step) bool { return s.Type == "grill" && s.Actor() == DirectorActor })
}

// ApplyGrillMode returns def as a task whose grill mode is mode runs it, and whether the mode changed anything. For
// unattended, the Director grill (DirectorGrill) becomes a step run by an Artisan with UnattendedGrillSkills; its id,
// wiring, loop rules and every other step stay as they are. A live mode, or a def with no Director grill, returns def
// itself. def is never modified, and the result's digest is not the bound one: a task binds def, and the engine
// applies its grill mode after checking that binding.
func ApplyGrillMode(def *Definition, mode GrillMode) (*Definition, bool) {
	i := DirectorGrill(def)
	if mode != GrillUnattended || i < 0 {
		return def, false
	}
	out := *def
	out.Steps = slices.Clone(def.Steps)
	step := &out.Steps[i]
	step.Settings = maps.Clone(step.Settings)
	for name, v := range map[string]any{ActorSetting: ArtisanActor, "skills": UnattendedGrillSkills} {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("workflow: encoding grill %s: %v", name, err))
		}
		step.Settings[name] = raw
	}
	return &out, true
}
