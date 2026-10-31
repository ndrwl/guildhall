package workflow

import (
	"slices"
	"strconv"
	"strings"
)

// sliceMark joins a step id and its slice: slice k >= 2 of step s runs as s--s<k>. A workflow that declares a slice
// range has no step id with "--" in it (Validate), so a qualified id is never one of its steps' ids, and it still
// passes ValidID, so every record, mutation and message id built from it stays valid.
const sliceMark = "--s"

// SliceStep is the id step runs under in slice k of a multi-PR task: step itself in slice 1, so a one-slice task's
// records keep their names, and step--s<k> from slice 2 on.
func SliceStep(step string, k int) string {
	if k <= 1 {
		return step
	}
	return step + sliceMark + strconv.Itoa(k)
}

// ParseSliceStep is the inverse of SliceStep: the step id and slice of id, or id and 1 when it carries no slice.
func ParseSliceStep(id string) (string, int) {
	i := strings.LastIndex(id, sliceMark)
	if i <= 0 {
		return id, 1
	}
	k, err := strconv.Atoi(id[i+len(sliceMark):])
	if err != nil || k < 2 || strconv.Itoa(k) != id[i+len(sliceMark):] {
		return id, 1
	}
	return id[:i], k
}

// InSlices reports whether step is inside d's slice range; false when d declares none.
func (d *Definition) InSlices(step string) bool {
	if d.Slices == nil {
		return false
	}
	from, to, at := d.stepIndex(d.Slices.From), d.stepIndex(d.Slices.To), d.stepIndex(step)
	return from >= 0 && at >= from && at <= to
}

// SliceView is def as slice k of a multi-PR task runs it: every step inside the slice range takes its slice id,
// SliceStep(id, k), and so do the references and loop rules naming one, so each slice reads and writes only its own
// records. Steps outside the range, and every step for k = 1 or a definition with no range, keep their ids; def is not
// modified. The view is for the engine's walk only: its digest is not the bound revision's.
func SliceView(def *Definition, k int) *Definition {
	if def.Slices == nil || k <= 1 {
		return def
	}
	rename := func(id string) string {
		if def.InSlices(id) {
			return SliceStep(id, k)
		}
		return id
	}
	view := *def
	view.Steps = make([]Step, len(def.Steps))
	for i, s := range def.Steps {
		s.ID = rename(s.ID)
		if len(s.Inputs) > 0 {
			inputs := make(map[string]Refs, len(s.Inputs))
			for port, refs := range s.Inputs {
				renamed := make(Refs, len(refs))
				for j, ref := range refs {
					from, out, _ := strings.Cut(ref, ".")
					renamed[j] = rename(from) + "." + out
				}
				inputs[port] = renamed
			}
			s.Inputs = inputs
		}
		if len(s.On) > 0 {
			on := make(map[string]Rule, len(s.On))
			for outcome, r := range s.On {
				r.Goto = rename(r.Goto)
				on[outcome] = r
			}
			s.On = on
		}
		view.Steps[i] = s
	}
	return &view
}

// checkSlices validates the slice range: both ends are steps, From is not after To, To is a deliver step, From comes
// after the step that accepts the spec, and no step id could be read as a slice id.
func (v *validator) checkSlices() {
	r := v.def.Slices
	if r == nil {
		return
	}
	for _, s := range v.def.Steps {
		if strings.Contains(s.ID, "--") {
			v.addf("step %q: a workflow with a slice range has no step id containing %q, which marks a slice", s.ID, "--")
		}
	}
	from, okFrom := v.index[r.From]
	to, okTo := v.index[r.To]
	if !okFrom {
		v.addf("slices: from %q is not a step", r.From)
	}
	if !okTo {
		v.addf("slices: to %q is not a step", r.To)
	}
	if !okFrom || !okTo {
		return
	}
	if from > to {
		v.addf("slices: from %q comes after to %q", r.From, r.To)
	}
	if v.def.Steps[to].Type != "deliver" {
		v.addf("slices: the range ends at %q, a %s step, not a deliver step", r.To, v.def.Steps[to].Type)
	}
	if accept := slices.IndexFunc(v.def.Steps, func(s Step) bool { return s.Type == "guildmaster-approval" }); accept < 0 || from <= accept {
		v.addf("slices: the range starts at %q, which does not come after the step that accepts the spec", r.From)
	}
}
