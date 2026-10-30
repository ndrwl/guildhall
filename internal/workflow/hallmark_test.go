package workflow

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestHallmarkWorkflow: the shipped hallmark workflow validates and is one hallmark step, run by an Artisan under the
// researcher profile with the guildhall-hallmark skill, with no deliver step and nothing wired to previous.
func TestHallmarkWorkflow(t *testing.T) {
	def, err := Hallmark()
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "hallmark" || len(def.Steps) != 1 {
		t.Fatalf("hallmark = %s with %d steps", def.Name, len(def.Steps))
	}
	s := def.Steps[0]
	if s.ID != "hallmark" || s.Type != "hallmark" || s.Version != 1 || s.Actor() != ArtisanActor {
		t.Fatalf("step = %+v", s)
	}
	if s.Profile() != "researcher" || EffectiveProfile(s) != "researcher" || !slices.Equal(s.Skills(), []string{"guildhall-hallmark"}) {
		t.Fatalf("profile %q, skills %q", s.Profile(), s.Skills())
	}
	for _, want := range []string{"read-only", "do not edit, commit, stage or reset tracked files", "untracked", "first recorded hallmark"} {
		if !strings.Contains(s.Instructions(), want) {
			t.Errorf("instructions = %q, want them to mention %q", s.Instructions(), want)
		}
	}
	if len(s.Inputs) != 0 {
		t.Fatalf("inputs = %v, want none wired", s.Inputs)
	}
}

// TestHallmarkKind: the hallmark step kind takes an optional single previous document and writes a report document,
// accepts profile, skills and instructions, is Artisan-only, and validates as a one-step workflow.
func TestHallmarkKind(t *testing.T) {
	k, ok := LookupKind("hallmark", 1)
	if !ok {
		t.Fatal("hallmark v1 is not registered")
	}
	if !reflect.DeepEqual(k.Inputs, []Port{{Name: "previous", Type: Document, Optional: true}}) {
		t.Fatalf("inputs = %+v", k.Inputs)
	}
	if !reflect.DeepEqual(k.Outputs, []Port{{Name: "report", Type: Document}}) {
		t.Fatalf("outputs = %+v", k.Outputs)
	}
	if !slices.Equal(k.Actors, []Actor{ArtisanActor}) {
		t.Fatalf("actors = %v", k.Actors)
	}
	for _, tc := range []struct{ settings, want string }{
		{`{}`, ""},
		{`{"profile":"researcher","skills":["guildhall-hallmark"],"instructions":"Read only."}`, ""},
		{`{"actor":"guildmaster"}`, `hallmark always runs as artisan`},
		{`{"from":"x"}`, `unknown setting "from"`},
	} {
		def, err := Parse([]byte(`{"schema":"guildhall.workflow/1","name":"h","steps":[{"id":"h","type":"hallmark","version":1,"settings":` + tc.settings + `}]}`))
		if err != nil {
			t.Fatal(err)
		}
		err = Validate(def)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: Validate = %v, want %q", tc.settings, err, tc.want)
		}
	}
}
