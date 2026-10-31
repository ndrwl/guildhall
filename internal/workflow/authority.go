package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Weakening is one change from a baseline definition that the classifier cannot prove harmless.
type Weakening struct {
	// Step is the baseline step the change touches, or "" when it concerns the order of steps.
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason"`
}

func (w Weakening) String() string {
	if w.Step == "" {
		return w.Reason
	}
	return "step " + w.Step + ": " + w.Reason
}

// Guarded reports whether steps of this kind enforce review, approval, validation or delivery,
// so the classifier treats every change to them that it cannot prove harmless as weakening:
// review, document-review, guildmaster-approval, validate, publish and deliver.
func Guarded(typ string) bool {
	switch typ {
	case "review", "document-review", "guildmaster-approval", "validate", "publish", "deliver":
		return true
	}
	return false
}

// actorStrength ranks reviewers: a change to an actor of lower or unknown strength is weaker.
var actorStrength = map[Actor]int{ArtisanActor: 1, GuildmasterActor: 2}

// Weakenings classifies next as a change from base, both validated definitions, and returns every change
// it cannot prove harmless, in base step order (docs/configuration.md#workflow-edit-authority).
// Steps are matched by id. For each guarded step of base, it is weakening when next:
//   - removes it, or changes its type or version;
//   - changes its actor to a weaker one: the Guildmaster is stronger than an Artisan, and any other change is weaker;
//   - changes any other setting, such as a reviewer's instructions, profile or skills, which are reviewer-facing;
//   - drops any input reference it had (adding references is not weakening);
//   - removes a loop rule, sends it to another step, or lowers its max (raising max or adding a rule is not weakening).
//
// A step whose type or version changed is reported once, for that change alone.
// Steps present in both that change relative order are weakening, whatever their kind, and are reported first; then
// removing or changing base's slice range, which next may only add or keep.
// A publish or deliver step of next is weakening, after them, when base has no step of its type: a baseline that
// publishes or delivers nothing, such as the shipped hallmark workflow, has no guarded steps to compare a delivering
// revision with, so it cannot prove one harmless (docs/decisions/report-only-workflows.md).
// Changes to steps that are not guarded, other steps added by next, the name and the description are not weakening.
// It returns nil when next weakens nothing; base and next are not modified.
func Weakenings(base, next *Definition) []Weakening {
	var out []Weakening
	var was, now []string
	for _, s := range base.Steps {
		if next.stepIndex(s.ID) >= 0 {
			was = append(was, s.ID)
		}
	}
	for _, s := range next.Steps {
		if base.stepIndex(s.ID) >= 0 {
			now = append(now, s.ID)
		}
	}
	if !slices.Equal(was, now) {
		out = append(out, Weakening{Reason: fmt.Sprintf("step order changed from %s to %s", strings.Join(was, ", "), strings.Join(now, ", "))})
	}
	// The slice range is what keeps a multi-PR task open until its last slice merges (docs/decisions/multi-pr-tasks.md).
	if b := base.Slices; b != nil && (next.Slices == nil || *next.Slices != *b) {
		out = append(out, Weakening{Reason: fmt.Sprintf("removed or changed the slice range %s..%s", b.From, b.To)})
	}
	for _, n := range next.Steps {
		if (n.Type == "publish" || n.Type == "deliver") && !slices.ContainsFunc(base.Steps, func(b Step) bool { return b.Type == n.Type }) {
			out = append(out, Weakening{n.ID, fmt.Sprintf("added a %s step, which the baseline has none of", n.Type)})
		}
	}
	for _, b := range base.Steps {
		if !Guarded(b.Type) {
			continue
		}
		i := next.stepIndex(b.ID)
		if i < 0 {
			out = append(out, Weakening{b.ID, "removed"})
			continue
		}
		n := next.Steps[i]
		if n.Type != b.Type || n.Version != b.Version {
			out = append(out, Weakening{b.ID, fmt.Sprintf("type changed from %s version %d to %s version %d", b.Type, b.Version, n.Type, n.Version)})
			continue
		}
		for _, w := range stepWeakenings(b, n) {
			out = append(out, Weakening{b.ID, w})
		}
	}
	return out
}

// stepWeakenings compares one guarded step of the same kind in base and next.
func stepWeakenings(b, n Step) []string {
	var out []string
	if ba, na := b.Actor(), n.Actor(); ba != na && actorStrength[na] <= actorStrength[ba] {
		out = append(out, fmt.Sprintf("actor %s -> %s is weaker", ba, na))
	}
	for _, key := range sortedKeys(union(b.Settings, n.Settings)) {
		if key != ActorSetting && !bytes.Equal(b.Settings[key], n.Settings[key]) {
			out = append(out, fmt.Sprintf("%s changed (reviewer-facing)", key))
		}
	}
	for _, port := range sortedKeys(b.Inputs) {
		for _, ref := range b.Inputs[port] {
			if !slices.Contains(n.Inputs[port], ref) {
				out = append(out, fmt.Sprintf("input %s no longer takes %s", port, ref))
			}
		}
	}
	for _, outcome := range sortedKeys(b.On) {
		was := b.On[outcome]
		switch now, ok := n.On[outcome]; {
		case !ok:
			out = append(out, fmt.Sprintf("loop on %s removed", outcome))
		case now.Goto != was.Goto:
			out = append(out, fmt.Sprintf("loop on %s goes to %s instead of %s", outcome, now.Goto, was.Goto))
		case now.Max < was.Max:
			out = append(out, fmt.Sprintf("loop on %s max lowered from %d to %d", outcome, was.Max, now.Max))
		}
	}
	return out
}

func union[V any](a, b map[string]V) map[string]bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	return keys
}

// FloorProblems returns why def, a validated definition, falls below the review floor, or nil when it meets it.
// The floor is one non-author review and one validation before delivery: every deliver step must take,
// among its review inputs, the outcome of a review step, and, as its validation input, the result of a validate step.
// A review step is non-author by construction: its actor is a fresh Artisan session or the Guildmaster, never the build's session.
// A definition with no deliver step delivers nothing and meets the floor.
func FloorProblems(def *Definition) []string {
	var out []string
	for _, s := range def.Steps {
		if s.Type != "deliver" {
			continue
		}
		if !def.takesFrom(s, "review", "review") {
			out = append(out, fmt.Sprintf("deliver step %q takes no review outcome", s.ID))
		}
		if !def.takesFrom(s, "validation", "validate") {
			out = append(out, fmt.Sprintf("deliver step %q takes no validate result", s.ID))
		}
	}
	return out
}

// takesFrom reports whether step s's input port references an output of a step of type typ.
func (d *Definition) takesFrom(s Step, port, typ string) bool {
	for _, ref := range s.Inputs[port] {
		from, _, _ := strings.Cut(ref, ".")
		if i := d.stepIndex(from); i >= 0 && d.Steps[i].Type == typ {
			return true
		}
	}
	return false
}

// Basis says why a new task may bind a workflow revision.
type Basis string

const (
	// ByRelease is a definition this release ships.
	ByRelease Basis = "release"
	// ByGuildmaster is a revision whose exact digest the Guildmaster approved.
	ByGuildmaster Basis = "guildmaster"
	// NonWeakening is a revision that meets the floor and weakens nothing in an authorized baseline.
	NonWeakening Basis = "non-weakening"
)

// Admission is why Admit admitted a revision.
type Admission struct {
	Basis Basis `json:"basis"`
	// From is the authorized baseline a NonWeakening revision was compared with; it is empty otherwise.
	From Revision `json:"from,omitzero"`
}

// Authority is what Admit needs to know about authorized revisions.
// Admit decides policy only; recording and listing approvals belongs to the store.
type Authority struct {
	// Shipped are the definitions this release ships; their revisions are authorized by the release.
	Shipped []*Definition
	// Approved reports whether the Guildmaster approved this exact digest; nil approves nothing.
	Approved func(digest string) bool
}

// ErrNotAuthorized is the error Admit's refusals match with errors.Is.
var ErrNotAuthorized = errors.New("workflow revision is not authorized")

// RefusalError is Admit's refusal: the revision with no Guildmaster approval and why it needs one.
type RefusalError struct {
	Revision Revision
	// Floor lists FloorProblems of the revision.
	Floor []string
	// Against lists, for each authorized baseline the revision was compared with, its weakenings.
	Against []Comparison
}

// Comparison is one authorized baseline and what a refused revision weakens in it.
type Comparison struct {
	From       Revision
	Weakenings []Weakening
}

func (e *RefusalError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s %s is neither shipped nor approved", ErrNotAuthorized, e.Revision.Name, e.Revision.Digest)
	for _, p := range e.Floor {
		fmt.Fprintf(&b, "; below the review floor: %s", p)
	}
	if e.Floor == nil && len(e.Against) == 0 {
		b.WriteString("; no shipped or approved baseline to compare it with")
	}
	for _, c := range e.Against {
		ws := make([]string, len(c.Weakenings))
		for i, w := range c.Weakenings {
			ws[i] = w.String()
		}
		fmt.Fprintf(&b, "; weakens %s %s: %s", c.From.Name, c.From.Digest, strings.Join(ws, ", "))
	}
	return b.String()
}

func (e *RefusalError) Unwrap() error { return ErrNotAuthorized }

// Admit decides whether a new task may bind def, a validated definition, under the workflow-edit authority policy:
//  1. def is admitted ByRelease when its digest is one of a.Shipped's, else ByGuildmaster when a.Approved approves it;
//  2. otherwise it must meet the floor (FloorProblems) and have no Weakenings from at least one authorized baseline,
//     and is admitted NonWeakening from the first such baseline.
//
// The baselines are a.Shipped in order, then candidates, validated definitions whose content the caller holds,
// such as the repository's workflow file before its overrides; a candidate counts only when its own digest is
// shipped or approved, and a baseline is compared once however often it is given.
// Anything else returns a *RefusalError, which matches ErrNotAuthorized: below the floor, it lists the floor
// problems and compares nothing; otherwise it lists every baseline compared with its weakenings.
// Admit reads nothing and records nothing.
func (a Authority) Admit(def *Definition, candidates ...*Definition) (Admission, error) {
	rev := def.Revision()
	switch {
	case a.shipped(rev.Digest):
		return Admission{Basis: ByRelease}, nil
	case a.approved(rev.Digest):
		return Admission{Basis: ByGuildmaster}, nil
	}
	refusal := &RefusalError{Revision: rev, Floor: FloorProblems(def)}
	if refusal.Floor != nil {
		return Admission{}, refusal
	}
	seen := map[string]bool{}
	for _, base := range append(slices.Clone(a.Shipped), candidates...) {
		from := base.Revision()
		if seen[from.Digest] || !a.shipped(from.Digest) && !a.approved(from.Digest) {
			continue
		}
		seen[from.Digest] = true
		ws := Weakenings(base, def)
		if ws == nil {
			return Admission{Basis: NonWeakening, From: from}, nil
		}
		refusal.Against = append(refusal.Against, Comparison{From: from, Weakenings: ws})
	}
	return Admission{}, refusal
}

func (a Authority) shipped(digest string) bool {
	return slices.ContainsFunc(a.Shipped, func(d *Definition) bool { return d.Digest() == digest })
}

func (a Authority) approved(digest string) bool {
	return a.Approved != nil && a.Approved(digest)
}
