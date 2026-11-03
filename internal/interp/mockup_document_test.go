package interp_test

import (
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestMockupDocument: a designer attempt's design names its mock-up under the named mockup part, else the legacy
// design-2 name, by which the task records; any other subject names none, whatever it records.
func TestMockupDocument(t *testing.T) {
	plain := &workflow.Definition{Steps: []workflow.Step{{ID: "plan"}}}
	renamed := &workflow.Definition{Steps: []workflow.Step{{ID: "designer"}}}
	for _, c := range []struct {
		name     string
		def      *workflow.Definition
		subject  string
		recorded []string
		want     string
	}{
		{"named", plain, "designer-1-design", []string{"designer-1-design", "designer-1-mockup"}, "designer-1-mockup"},
		{"legacy", plain, "designer-1-design", []string{"designer-1-design", "designer-1-design-2"}, "designer-1-design-2"},
		{"both, named wins", plain, "designer-3-design", []string{"designer-3-design-2", "designer-3-mockup"}, "designer-3-mockup"},
		{"neither", plain, "designer-1-design", []string{"designer-1-design", "designer-2-mockup", "designer-2-design-2"}, ""},
		{"plan spec", plain, "plan-1-spec", []string{"plan-1-spec"}, ""},
		{"plan spec with a -2", plain, "plan-1-spec", []string{"plan-1-spec", "plan-1-spec-2", "plan-1-mockup"}, ""},
		{"exit", plain, "designer-x1-design", []string{"designer-x1-mockup", "designer-x1-design-2"}, ""},
		{"round 0", plain, "designer-0-design", []string{"designer-0-mockup", "designer-0-design-2"}, ""},
		{"index 2", plain, "designer-1-design-2", []string{"designer-1-mockup", "designer-1-design-2-2"}, ""},
		{"renamed designer", renamed, "round-designer-2-design", []string{"round-designer-2-mockup"}, "round-designer-2-mockup"},
		{"renamed designer, legacy", renamed, "round-designer-2-design", []string{"round-designer-2-design-2"}, "round-designer-2-design-2"},
		{"renamed designer, workflow step", renamed, "designer-1-design", []string{"designer-1-mockup", "designer-1-design-2"}, ""},
		{"nil def", nil, "designer-1-design", []string{"designer-1-mockup"}, "designer-1-mockup"},
		{"nil def, legacy", nil, "designer-1-design", []string{"designer-1-design-2"}, "designer-1-design-2"},
		{"empty", plain, "", []string{"designer-1-mockup"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			name, ok := interp.MockupDocument(c.def, c.subject, func(n string) bool { return slices.Contains(c.recorded, n) })
			if name != c.want || ok != (c.want != "") {
				t.Errorf("MockupDocument(%q) = %q, %v; want %q", c.subject, name, ok, c.want)
			}
		})
	}
}
