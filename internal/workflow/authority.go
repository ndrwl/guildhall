package workflow

import (
	"errors"
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
	return false
}

// Weakenings classifies next as a change from base, both validated definitions, and returns every change
// it cannot prove harmless, in base step order (docs/configuration.md#workflow-edit-authority).
// Steps are matched by id. For each guarded step of base, it is weakening when next:
//   - removes it, or changes its type or version;
//   - changes its actor to a weaker one: the Guildmaster is stronger than an Artisan, and any other change is weaker;
//   - changes any other setting, such as a reviewer's instructions, profile or skills, which are reviewer-facing;
//   - drops any input reference it had (adding references is not weakening);
//   - removes a loop rule, sends it to another step, or lowers its max (raising max or adding a rule is not weakening).
//
// Steps present in both that change relative order are weakening, whatever their kind.
// Changes to steps that are not guarded, steps added by next, the name and the description are not weakening.
// It returns nil when next weakens nothing; base and next are not modified.
func Weakenings(base, next *Definition) []Weakening {
	return nil
}

// FloorProblems returns why def, a validated definition, falls below the review floor, or nil when it meets it.
// The floor is one non-author review and one validation before delivery: every deliver step must take,
// among its review inputs, the outcome of a review step, and, as its validation input, the result of a validate step.
// A review step is non-author by construction: its actor is a fresh Artisan session or the Guildmaster, never the build's session.
// A definition with no deliver step delivers nothing and meets the floor.
func FloorProblems(def *Definition) []string {
	return nil
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
	return ErrNotAuthorized.Error()
}

func (e *RefusalError) Unwrap() error { return ErrNotAuthorized }

// Admit decides whether a new task may bind def, a validated definition, under the workflow-edit authority policy:
//  1. def is admitted ByRelease when its digest is one of a.Shipped's, else ByGuildmaster when a.Approved approves it;
//  2. otherwise it must meet the floor (FloorProblems) and have no Weakenings from at least one authorized baseline,
//     and is admitted NonWeakening from the first such baseline.
//
// The baselines are a.Shipped in order, then candidates, validated definitions whose content the caller holds,
// such as the repository's workflow file before its overrides; a candidate counts only when its own digest is
// shipped or approved. Anything else returns a *RefusalError, which matches ErrNotAuthorized.
// Admit reads nothing and records nothing.
func (a Authority) Admit(def *Definition, candidates ...*Definition) (Admission, error) {
	return Admission{}, nil
}
