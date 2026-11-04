package interp

import (
	"fmt"
	"slices"

	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// StrictSections is the one owner of which strict sections intake refuses for step under def: the prompt prints
// exactly this list (render.Stage.Strict) and checkOutputs checks exactly it. A plan always has its `## Slices`, and
// its `## Summary` and `## Open questions` too when a guildmaster-approval step of def shows its spec
// (approvalShows); the synthetic designer step has its `## Open questions`; every other step has none. It is a pure
// function of def and step.
func StrictSections(def *workflow.Definition, step workflow.Step) []spec.Example {
	switch step.Type {
	case "plan":
		if approvalShows(def, step.ID+".spec") {
			return []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample}
		}
		return []spec.Example{spec.SlicesExample}
	case designKindType:
		return []spec.Example{spec.OpenQuestionsExample}
	}
	return nil
}

// approvalShows reports whether a guildmaster-approval step of def takes ref, "step.output", as an input, so its ask
// shows that document's Summary and open questions. A bug workflow's plan has none.
func approvalShows(def *workflow.Definition, ref string) bool {
	if def == nil {
		return false
	}
	return slices.ContainsFunc(def.Steps, func(s workflow.Step) bool {
		if s.Type != "guildmaster-approval" {
			return false
		}
		for _, refs := range s.Inputs {
			if slices.Contains(refs, ref) {
				return true
			}
		}
		return false
	})
}

// strictChecker is intake's check of one strict section of step's document, data; each error keeps its wording.
type strictChecker func(r *run, step workflow.Step, data []byte) error

// strictCheck is intake's checker for the strict section whose spec.Example.Heading is heading.
type strictCheck struct {
	heading string
	check   strictChecker
}

// strictCheckers holds intake's checker for each heading StrictSections can return, keyed by spec.Example.Heading, in
// the order intake runs them, so a spec that breaks several sections gets the refusal it got before: Slices first.
var strictCheckers = []strictCheck{
	{spec.SlicesHeading, func(r *run, _ workflow.Step, data []byte) error { return r.checkSlices(data) }},
	{spec.SummaryHeading, func(_ *run, _ workflow.Step, data []byte) error {
		if err := spec.CheckSummary(data); err != nil {
			return rejectOutput(fmt.Errorf("%w; the approval ask shows it, so write one", err))
		}
		return nil
	}},
	{spec.OpenQuestionsHeading, func(_ *run, step workflow.Step, data []byte) error {
		if step.Type == designKindType {
			return checkDesignQuestions(data)
		}
		if _, err := spec.SpecQuestions(data); err != nil {
			return rejectOutput(fmt.Errorf("%w; the approval ask copies each open question in full, so write \"None\" or **Qn. title** questions, each with Options: and Recommended: lines", err))
		}
		return nil
	}},
}

// checkStrict runs intake's checker for each section StrictSections returns for s, in strictCheckers' order, and
// returns the first refusal. A heading with no checker is a programming error, which TestStrictCheckersCoverSections
// catches; it is refused here too rather than silently unchecked.
func (r *run) checkStrict(s workflow.Step, data []byte) error {
	sections := StrictSections(r.base, s)
	for _, ex := range sections {
		if !slices.ContainsFunc(strictCheckers, func(c strictCheck) bool { return c.heading == ex.Heading }) {
			return fmt.Errorf("%w: no intake check for strict section %s", ErrUnsupported, ex.Heading)
		}
	}
	for _, c := range strictCheckers {
		if !slices.ContainsFunc(sections, func(ex spec.Example) bool { return ex.Heading == c.heading }) {
			continue
		}
		if err := c.check(r, s, data); err != nil {
			return err
		}
	}
	return nil
}
