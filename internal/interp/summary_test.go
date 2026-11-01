package interp_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
)

// summarisedSpec is threeSlices with a Summary block.
const summarisedSpec = `# Large work

## Summary

- **Goal:** deliver the large work.
- **Size:** about 400 lines, in three pull requests.
- **Slices:** parser, engine, views.
- **Changed since the grill:** nothing; the plan follows the grill's agreed change.
- **Open questions:** one:
  - Q1 Which parser? Recommended: the strict one.

## Goal

Deliver the large work in three pull requests.

## Slices

1. **Parser**: the parser; about 100 lines in 2 files.
2. **Engine**: the engine; about 200 lines in 4 files.
3. **Views**: the views; about 100 lines in 3 files.
`

// TestApprovalCarriesPlanSummary: a plan's approval question carries its spec's Summary block after the first line,
// and a spec without one opens the approval with a line naming the spec instead.
func TestApprovalCarriesPlanSummary(t *testing.T) {
	for _, c := range []struct {
		name, spec, has, lacks string
	}{
		{"summary", summarisedSpec, "attempt 1.\n\nPlan summary:\n- **Goal:** deliver the large work.\n", "No summary in this spec"},
		{"no summary", threeSlices, "attempt 1.\n\nNo summary in this spec; read tasks/app/t7/plan-1/spec.md.\n", "Plan summary:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, b := range backends {
				t.Run(b.name, func(t *testing.T) {
					e, _, _ := newSliceEnv(t, b, true, sliceScript(t, c.spec)...)
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}),
						interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
					q := e.decision(t, "accept-1").Question
					if !strings.Contains(q, c.has) || strings.Contains(q, c.lacks) {
						t.Errorf("question = %q, want %q and no %q", q, c.has, c.lacks)
					}
					if c.spec == summarisedSpec && !strings.Contains(q, "  - Q1 Which parser? Recommended: the strict one.\n\nInputs:\n") {
						t.Errorf("question = %q, want the whole block before the inputs", q)
					}
				})
			}
		})
	}
}
