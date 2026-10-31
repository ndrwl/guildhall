package workflow

import (
	"strings"
	"testing"
)

func TestSliceStepRoundTrips(t *testing.T) {
	for _, c := range []struct {
		step string
		k    int
		id   string
	}{{"build", 1, "build"}, {"build", 2, "build--s2"}, {"deliver", 12, "deliver--s12"}} {
		if got := SliceStep(c.step, c.k); got != c.id {
			t.Errorf("SliceStep(%q, %d) = %q, want %q", c.step, c.k, got, c.id)
		}
		if step, k := ParseSliceStep(c.id); step != c.step || k != c.k {
			t.Errorf("ParseSliceStep(%q) = %q, %d, want %q, %d", c.id, step, k, c.step, c.k)
		}
		if !ValidID(c.id) {
			t.Errorf("slice id %q is not a valid id, so the store would refuse its records", c.id)
		}
	}
	for _, id := range []string{"build", "build--s1", "build--s02", "build--sx", "--s2", "a-s2"} {
		if step, k := ParseSliceStep(id); step != id || k != 1 {
			t.Errorf("ParseSliceStep(%q) = %q, %d, want it unqualified", id, step, k)
		}
	}
}

func TestDefaultDeclaresSlices(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if def.Slices == nil || def.Slices.From != "build" || def.Slices.To != "deliver" {
		t.Fatalf("default slices = %+v, want build..deliver", def.Slices)
	}
	for _, f := range []func() (*Definition, error){Bug, Hallmark} {
		d, err := f()
		if err != nil {
			t.Fatal(err)
		}
		if d.Slices != nil {
			t.Errorf("%s declares slices %+v; only the default does", d.Name, d.Slices)
		}
	}
	retired, err := Retired("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range retired {
		if d.Slices != nil {
			t.Errorf("retired default %s declares slices", d.Digest())
		}
	}
}

func TestSliceRangeValidation(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(d *Definition)
		wants string
	}{
		{"unknown from", func(d *Definition) { d.Slices.From = "nope" }, `slices: from "nope" is not a step`},
		{"unknown to", func(d *Definition) { d.Slices.To = "nope" }, `slices: to "nope" is not a step`},
		{"backwards", func(d *Definition) { d.Slices.From, d.Slices.To = "deliver", "build" }, `slices: from "deliver" comes after to "build"`},
		{"not ending at deliver", func(d *Definition) { d.Slices.To = "validate" }, `the range ends at "validate", a validate step, not a deliver step`},
		{"starting at accept", func(d *Definition) { d.Slices.From = "accept" }, `the range starts at "accept", which does not come after the step that accepts the spec`},
		{"no acceptance", func(d *Definition) {
			remove(d, "accept")
			for _, id := range []string{"build", "review", "publish"} {
				step(d, id).Inputs["spec"] = Refs{"plan.spec"}
			}
		}, "does not come after the step that accepts the spec"},
		{"a step id with the slice mark", func(d *Definition) {
			d.Steps[1].ID = "re--search"
			step(d, "plan").Inputs["context"] = Refs{"grill.notes"}
		}, `step "re--search": a workflow with a slice range has no step id containing "--"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			def, err := Default()
			if err != nil {
				t.Fatal(err)
			}
			r := *def.Slices
			def.Slices = &r
			c.edit(def)
			err = Validate(def)
			if err == nil || !strings.Contains(err.Error(), c.wants) {
				t.Errorf("Validate = %v, want it to say %q", err, c.wants)
			}
		})
	}
}

func TestSliceView(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	before := def.Digest()
	if v := SliceView(def, 1); v != def {
		t.Error("slice 1's view is not the definition itself")
	}
	v := SliceView(def, 3)
	if def.Digest() != before {
		t.Fatal("SliceView modified its definition")
	}
	var ids []string
	for _, s := range v.Steps {
		ids = append(ids, s.ID)
	}
	if got, want := strings.Join(ids, " "), "grill research plan accept build--s3 review--s3 publish--s3 validate--s3 deliver--s3"; got != want {
		t.Errorf("view steps = %s, want %s", got, want)
	}
	review := v.Steps[5]
	if got := review.Inputs["head"][0]; got != "build--s3.head" {
		t.Errorf("review head = %s, want build--s3.head", got)
	}
	if got := review.Inputs["spec"][0]; got != "accept.spec" {
		t.Errorf("review spec = %s, want accept.spec, outside the range", got)
	}
	if got := review.On["changes_requested"].Goto; got != "build--s3" {
		t.Errorf("review loops to %s, want build--s3", got)
	}
	if got := v.Steps[3].On["changes_requested"].Goto; got != "plan" {
		t.Errorf("accept loops to %s, want plan", got)
	}
	bug, err := Bug()
	if err != nil {
		t.Fatal(err)
	}
	if SliceView(bug, 2) != bug {
		t.Error("a workflow with no range has a slice view")
	}
}

// TestSliceRangeWeakenings: removing or changing the default's slice range weakens it, so a retired revision without
// one is never admitted for a new task; adding one to a workflow that had none does not.
func TestSliceRangeWeakenings(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	none := *def
	none.Slices = nil
	changed := *def
	changed.Slices = &SliceRange{From: "review", To: "deliver"}
	for _, c := range []struct {
		name       string
		base, next *Definition
		weakens    bool
	}{{"removed", def, &none, true}, {"changed", def, &changed, true}, {"kept", def, def, false}, {"added", &none, def, false}} {
		got := Weakenings(c.base, c.next)
		if c.weakens != (len(got) == 1 && strings.Contains(got[0].Reason, "slice range build..deliver")) || !c.weakens && len(got) > 0 {
			t.Errorf("%s: weakenings = %v", c.name, got)
		}
	}
}
