package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ValidationError lists every problem found in a workflow, so one run names them all.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid workflow: " + strings.Join(e.Problems, "; ")
}

// Validate checks a parsed workflow before any task may start it:
// step kinds exist in this release, settings match their kind,
// every input is wired to an earlier step's output of the same type,
// and every loop returns to the same or an earlier step with a bound.
func Validate(def *Definition) error {
	v := validator{def: def, index: map[string]int{}}
	v.check()
	if len(v.problems) > 0 {
		return &ValidationError{Problems: v.problems}
	}
	return nil
}

func ValidateStepSettings(s Step) error {
	kind, err := lookup(s.Type, s.Version)
	if err != nil {
		return err
	}
	v := validator{}
	v.checkSettings(s, kind)
	if len(v.problems) > 0 {
		return &ValidationError{Problems: v.problems}
	}
	return nil
}

type validator struct {
	def      *Definition
	index    map[string]int
	problems []string
}

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) check() {
	if !ValidID(v.def.Name) {
		v.addf("invalid workflow name %q", v.def.Name)
	}
	if len(v.def.Steps) == 0 {
		v.addf("workflow has no steps")
	}
	for i, s := range v.def.Steps {
		if !ValidID(s.ID) {
			v.addf("step %d: invalid step id %q", i+1, s.ID)
		}
		if s.ID == JudgeStep {
			v.addf("step %d: step id %q is reserved for the judge attempts of delivered tasks", i+1, s.ID)
		}
		if _, dup := v.index[s.ID]; dup {
			v.addf("duplicate step id %q", s.ID)
			continue
		}
		v.index[s.ID] = i
	}
	for i, s := range v.def.Steps {
		kind, err := lookup(s.Type, s.Version)
		if err != nil {
			v.addf("step %q: %v", s.ID, err)
			continue
		}
		v.checkSettings(s, kind)
		v.checkInputs(i, s, kind)
		v.checkRules(i, s, kind)
	}
}

func (v *validator) checkSettings(s Step, kind Kind) {
	v.checkActor(s, kind)
	for _, name := range sortedKeys(s.Settings) {
		if name == ActorSetting {
			continue
		}
		spec, ok := kind.Settings[name]
		if !ok {
			v.addf("step %q: unknown setting %q for %s", s.ID, name, kind.Type)
			continue
		}
		if !hasType(s.Settings[name], spec.Type) {
			v.addf("step %q: setting %q must be a %s", s.ID, name, spec.Type)
		} else if dup := duplicateName(s.Settings[name], spec.Type); dup != "" {
			v.addf("step %q: setting %q names %q twice", s.ID, name, dup)
		}
	}
	for _, name := range sortedKeys(kind.Settings) {
		if _, set := s.Settings[name]; kind.Settings[name].Required && !set {
			v.addf("step %q: required setting %q is missing", s.ID, name)
		}
	}
}

// checkActor requires an allowed "actor" setting on a kind with several actors and refuses one on a kind with a single actor.
func (v *validator) checkActor(s Step, kind Kind) {
	raw, set := s.Settings[ActorSetting]
	switch {
	case len(kind.Actors) == 1:
		if set {
			v.addf("step %q: %s always runs as %s; remove setting %q", s.ID, kind.Type, kind.Actors[0], ActorSetting)
		}
	case !set:
		v.addf("step %q: required setting %q is missing", s.ID, ActorSetting)
	case !hasType(raw, StringSetting):
		v.addf("step %q: setting %q must be a %s", s.ID, ActorSetting, StringSetting)
	default:
		var actor Actor
		_ = json.Unmarshal(raw, &actor)
		if !slices.Contains(kind.Actors, actor) {
			v.addf("step %q: %s does not allow actor %q", s.ID, kind.Type, actor)
		}
	}
}

func hasType(raw json.RawMessage, typ SettingType) bool {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch typ {
	case StringSetting:
		_, ok := value.(string)
		return ok
	case BoolSetting:
		_, ok := value.(bool)
		return ok
	case IntSetting:
		var n int
		return json.Unmarshal(raw, &n) == nil
	case NameSetting:
		s, ok := value.(string)
		return ok && ValidID(s)
	case NameListSetting:
		var names []string
		return json.Unmarshal(raw, &names) == nil && names != nil && !slices.ContainsFunc(names, func(n string) bool { return !ValidID(n) })
	}
	return false
}

// duplicateName returns a name listed twice in a list-of-names setting, or "".
func duplicateName(raw json.RawMessage, typ SettingType) string {
	var names []string
	if typ != NameListSetting || json.Unmarshal(raw, &names) != nil {
		return ""
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			return n
		}
		seen[n] = true
	}
	return ""
}

func (v *validator) checkInputs(at int, s Step, kind Kind) {
	for _, name := range sortedKeys(s.Inputs) {
		port, ok := kind.input(name)
		if !ok {
			v.addf("step %q: unknown input %q for %s", s.ID, name, kind.Type)
			continue
		}
		refs := s.Inputs[name]
		if len(refs) > 1 && !port.Many {
			v.addf("step %q: input %q takes one reference", s.ID, name)
		}
		for _, ref := range refs {
			v.checkRef(at, s, port, ref)
		}
	}
	for _, port := range kind.Inputs {
		if len(s.Inputs[port.Name]) == 0 && !port.Optional {
			v.addf("step %q: required input %q is not wired", s.ID, port.Name)
		}
	}
}

// checkRef enforces readiness: an input can only come from a step that runs earlier,
// which also rules out dependency cycles inside one workflow.
func (v *validator) checkRef(at int, s Step, port Port, ref string) {
	from, out, ok := strings.Cut(ref, ".")
	if !ok || from == "" || out == "" {
		v.addf("step %q: input %q reference %q: want \"step.output\"", s.ID, port.Name, ref)
		return
	}
	i, ok := v.index[from]
	if !ok {
		v.addf("step %q: input %q references unknown step %q", s.ID, port.Name, from)
		return
	}
	if i >= at {
		v.addf("step %q: input %q from %q must come from an earlier step", s.ID, port.Name, from)
		return
	}
	src := v.def.Steps[i]
	kind, ok := LookupKind(src.Type, src.Version)
	if !ok {
		return // already reported on the source step
	}
	output, ok := kind.output(out)
	if !ok {
		v.addf("step %q: input %q: step %q has no output %q", s.ID, port.Name, from, out)
		return
	}
	if output.Type != port.Type {
		v.addf("step %q: input %q wants %s but %s is %s", s.ID, port.Name, port.Type, ref, output.Type)
	}
}

func (v *validator) checkRules(at int, s Step, kind Kind) {
	for _, outcome := range sortedKeys(s.On) {
		rule := s.On[outcome]
		if !slices.Contains(kind.Loops, outcome) {
			v.addf("step %q: outcome %q cannot carry a rule for %s", s.ID, outcome, kind.Type)
			continue
		}
		i, ok := v.index[rule.Goto]
		switch {
		case !ok:
			v.addf("step %q: on %q goes to unknown step %q", s.ID, outcome, rule.Goto)
		case i > at:
			v.addf("step %q: on %q can only loop back to this or an earlier step, not %q", s.ID, outcome, rule.Goto)
		}
		if rule.Max < 1 || rule.Max > MaxLoopRounds {
			v.addf("step %q: on %q: max must be between 1 and %d", s.ID, outcome, MaxLoopRounds)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
