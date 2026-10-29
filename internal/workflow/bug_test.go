package workflow

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

// TestBugIsTheDefaultWithInstructions: the shipped bug workflow validates, and differs from the shipped default only
// in its name, description and the instructions on research, build and review.
func TestBugIsTheDefaultWithInstructions(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	bug, err := Bug()
	if err != nil {
		t.Fatal(err)
	}
	if bug.Name != "bug" || bug.Digest() == def.Digest() || len(bug.Steps) != len(def.Steps) {
		t.Fatalf("bug = %s %s with %d steps", bug.Name, bug.Digest(), len(bug.Steps))
	}
	instructed := map[string]string{"research": "Reproduce the defect", "build": "regression test", "review": "regression test"}
	for i, s := range bug.Steps {
		want := def.Steps[i]
		if prefix, ok := instructed[s.ID]; ok {
			if !strings.Contains(s.Instructions(), prefix) {
				t.Errorf("step %s instructions = %q, want them to mention %q", s.ID, s.Instructions(), prefix)
			}
			s.Settings = maps.Clone(s.Settings)
			delete(s.Settings, "instructions")
			if len(s.Settings) == 0 {
				s.Settings = nil
			}
		}
		got, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		other, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(other) {
			t.Errorf("step %d differs from the default beyond its instructions:\n%s\n%s", i, got, other)
		}
	}
}

func TestBuildAcceptsInstructions(t *testing.T) {
	for settings, want := range map[string]string{
		`{"instructions":"Write the regression test first."}`:                       "",
		`{"profile":"builder","skills":["go-tdd"],"instructions":"Test it first."}`: "",
		`{"instructions":["no"]}`:                                                   `setting "instructions" must be a string`,
	} {
		def, err := Parse([]byte(withSettings("build", settings)))
		if err != nil {
			t.Fatal(err)
		}
		err = Validate(def)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", settings, err, want)
		}
	}
}
