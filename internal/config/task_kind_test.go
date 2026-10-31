package config

import (
	"slices"
	"testing"
)

// TestTaskKindTable: TaskKinds is TaskKindTable's names in order, every name finds its row, and exactly one row, the
// first, runs the repository's Workflow.
func TestTaskKindTable(t *testing.T) {
	var names []string
	feature := 0
	for i, row := range TaskKindTable {
		names = append(names, row.Name)
		if got, ok := TaskKindRowOf(row.Name); !ok || got != row {
			t.Errorf("TaskKindRowOf(%q) = %+v, %v", row.Name, got, ok)
		}
		if row.FeatureWorkflow {
			feature++
			if i != 0 {
				t.Errorf("row %d (%s) runs Workflow; only the first may", i, row.Name)
			}
		}
	}
	if !slices.Equal(TaskKinds, names) {
		t.Errorf("TaskKinds = %v, want %v", TaskKinds, names)
	}
	if feature != 1 {
		t.Errorf("%d rows run Workflow, want 1", feature)
	}
	if _, ok := TaskKindRowOf("docs"); ok {
		t.Error("TaskKindRowOf found an unknown kind")
	}
}
