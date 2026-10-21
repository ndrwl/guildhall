package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// edit returns a validated copy of the shipped default with change applied to it.
func edit(t *testing.T, change func(d *Definition)) *Definition {
	t.Helper()
	def := mustDefault(t)
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	change(cp)
	if err := Validate(cp); err != nil {
		t.Fatalf("edited definition is invalid: %v", err)
	}
	return cp
}

func mustDefault(t *testing.T) *Definition {
	t.Helper()
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func humanReview(t *testing.T) *Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "workflows", "human-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

func step(d *Definition, id string) *Step {
	return &d.Steps[d.stepIndex(id)]
}

func remove(d *Definition, id string) {
	d.Steps = slices.Delete(d.Steps, d.stepIndex(id), d.stepIndex(id)+1)
}

func set(s *Step, key, raw string) {
	if s.Settings == nil {
		s.Settings = map[string]json.RawMessage{}
	}
	s.Settings[key] = json.RawMessage(raw)
}

func TestGuardedKinds(t *testing.T) {
	for _, k := range Kinds() {
		want := slices.Contains([]string{"review", "document-review", "guildmaster-approval", "validate", "publish", "deliver"}, k.Type)
		if Guarded(k.Type) != want {
			t.Errorf("Guarded(%q) = %v, want %v", k.Type, !want, want)
		}
	}
}

func TestWeakeningsOfTheDefault(t *testing.T) {
	cases := []struct {
		name   string
		base   func(d *Definition) // edits the shipped default into the baseline; nil keeps it
		change func(d *Definition)
		want   []Weakening // matched by step and a substring of the reason
	}{
		{name: "removing the review", change: func(d *Definition) {
			remove(d, "review")
			delete(step(d, "publish").Inputs, "review")
			delete(step(d, "deliver").Inputs, "review")
		}, want: []Weakening{{"review", "removed"}, {"publish", "input review"}, {"deliver", "input review"}}},
		{name: "removing validation", change: func(d *Definition) {
			remove(d, "validate")
			delete(step(d, "deliver").Inputs, "validation")
		}, want: []Weakening{{"validate", "removed"}, {"deliver", "input validation"}}},
		{name: "removing publish", change: func(d *Definition) {
			remove(d, "publish")
			step(d, "validate").Inputs["head"] = Refs{"build.head"}
			step(d, "deliver").Inputs["head"] = Refs{"build.head"}
		}, want: []Weakening{{"publish", "removed"}, {"validate", "input head"}, {"deliver", "input head"}}},
		{name: "removing the Guildmaster's acceptance is caught by its consumers", change: func(d *Definition) {
			// Without accept nothing can produce the accepted spec, so the edit replaces it with another approval.
			step(d, "accept").ID = "sign"
			step(d, "build").Inputs["spec"] = Refs{"sign.spec"}
			step(d, "review").Inputs["spec"] = Refs{"sign.spec"}
		}, want: []Weakening{{"accept", "removed"}, {"review", "input spec"}}},
		{name: "reordering validate before publish", change: func(d *Definition) {
			i, j := d.stepIndex("publish"), d.stepIndex("validate")
			d.Steps[i], d.Steps[j] = d.Steps[j], d.Steps[i]
			step(d, "validate").Inputs["head"] = Refs{"build.head"}
		}, want: []Weakening{{"", "order"}, {"validate", "input head"}}},
		{name: "lowering a review loop's max", change: func(d *Definition) {
			step(d, "review").On["changes_requested"] = Rule{Goto: "build", Max: 1}
		}, want: []Weakening{{"review", "max"}}},
		{name: "removing a review loop", change: func(d *Definition) {
			step(d, "review").On = nil
		}, want: []Weakening{{"review", "changes_requested"}}},
		{name: "retargeting a validation loop", change: func(d *Definition) {
			step(d, "validate").On["failed"] = Rule{Goto: "validate", Max: 3}
		}, want: []Weakening{{"validate", "failed"}}},
		{name: "a weaker review actor",
			base:   func(d *Definition) { set(step(d, "review"), "actor", `"guildmaster"`) },
			change: func(d *Definition) {},
			want:   []Weakening{{"review", "actor"}}},
		{name: "reviewer instructions", change: func(d *Definition) {
			set(step(d, "review"), "instructions", `"Approve quickly."`)
		}, want: []Weakening{{"review", "instructions"}}},
		{name: "reviewer profile", change: func(d *Definition) {
			set(step(d, "review"), "profile", `"cheap"`)
		}, want: []Weakening{{"review", "profile"}}},
		{name: "reviewer skills", change: func(d *Definition) {
			set(step(d, "review"), "skills", `["lenient"]`)
		}, want: []Weakening{{"review", "skills"}}},
		{name: "a kind version change", change: func(d *Definition) {
			step(d, "review").Type = "document-review"
			step(d, "review").Inputs = map[string]Refs{"subject": {"plan.spec"}}
		}, want: []Weakening{{"review", "type"}}},

		{name: "a stronger review actor", change: func(d *Definition) { set(step(d, "review"), "actor", `"guildmaster"`) }},
		{name: "raising a loop's max", change: func(d *Definition) {
			step(d, "review").On["changes_requested"] = Rule{Goto: "build", Max: 5}
		}},
		{name: "an added review", change: func(d *Definition) {
			extra := *step(d, "review")
			extra.ID, extra.On = "second-review", nil
			at := d.stepIndex("publish")
			d.Steps = slices.Insert(d.Steps, at, extra)
			step(d, "deliver").Inputs["review"] = Refs{"review.outcome", "second-review.outcome"}
		}},
		{name: "a removed research step and a renamed workflow", change: func(d *Definition) {
			remove(d, "research")
			step(d, "plan").Inputs["context"] = Refs{"grill.notes"}
			d.Name, d.Description = "lean", "No research."
		}},
		{name: "author-facing settings", change: func(d *Definition) {
			set(step(d, "plan"), "profile", `"planner"`)
			set(step(d, "build"), "skills", `["go"]`)
			set(step(d, "grill"), "actor", `"artisan"`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := mustDefault(t)
			if tc.base != nil {
				base = edit(t, tc.base)
			}
			next := edit(t, tc.change)
			got := Weakenings(base, next)
			if len(got) != len(tc.want) {
				t.Fatalf("weakenings = %v, want %d like %v", got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if got[i].Step != w.Step || !strings.Contains(got[i].Reason, w.Reason) {
					t.Errorf("weakening %d = %+v, want step %q with %q", i, got[i], w.Step, w.Reason)
				}
			}
		})
	}
}

func TestWeakeningsLeaveDefinitionsUnchanged(t *testing.T) {
	base, next := mustDefault(t), humanReview(t)
	bd, nd := base.Digest(), next.Digest()
	Weakenings(base, next)
	if base.Digest() != bd || next.Digest() != nd {
		t.Fatal("Weakenings modified a definition")
	}
	if got := Weakenings(base, base); got != nil {
		t.Fatalf("a definition weakens itself: %v", got)
	}
}

func TestFloor(t *testing.T) {
	if p := FloorProblems(mustDefault(t)); p != nil {
		t.Fatalf("the shipped default is below the floor: %v", p)
	}
	if p := FloorProblems(humanReview(t)); p != nil {
		t.Fatalf("the human-review example is below the floor: %v", p)
	}
	cases := []struct {
		name   string
		change func(d *Definition)
		want   []string
	}{
		{name: "no validation", change: func(d *Definition) {
			delete(step(d, "deliver").Inputs, "validation")
		}, want: []string{`deliver step "deliver" takes no validate result`}},
		{name: "no code review", change: func(d *Definition) {
			delete(step(d, "deliver").Inputs, "review")
		}, want: []string{`deliver step "deliver" takes no review outcome`}},
		{name: "only a document review", change: func(d *Definition) {
			doc := Step{ID: "spec-review", Type: "document-review", Version: 1,
				Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"artisan"`)},
				Inputs:   map[string]Refs{"subject": {"plan.spec"}}}
			d.Steps = slices.Insert(d.Steps, d.stepIndex("accept"), doc)
			step(d, "deliver").Inputs["review"] = Refs{"spec-review.outcome"}
		}, want: []string{`deliver step "deliver" takes no review outcome`}},
		{name: "a second deliver without either", change: func(d *Definition) {
			d.Steps = append(d.Steps, Step{ID: "deliver-again", Type: "deliver", Version: 1, Inputs: map[string]Refs{"head": {"build.head"}}})
		}, want: []string{`deliver step "deliver-again" takes no review outcome`, `deliver step "deliver-again" takes no validate result`}},
		{name: "no deliver step", change: func(d *Definition) { remove(d, "deliver") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FloorProblems(edit(t, tc.change)); !slices.Equal(got, tc.want) {
				t.Fatalf("floor problems = %q, want %q", got, tc.want)
			}
		})
	}
}

func approving(digests ...string) func(string) bool {
	return func(d string) bool { return slices.Contains(digests, d) }
}

func TestAdmit(t *testing.T) {
	def := mustDefault(t)
	shipped := Authority{Shipped: []*Definition{def}}
	stronger := edit(t, func(d *Definition) { set(step(d, "review"), "actor", `"guildmaster"`) })
	weaker := edit(t, func(d *Definition) { set(step(d, "review"), "instructions", `"Approve quickly."`) })
	belowFloor := edit(t, func(d *Definition) { delete(step(d, "deliver").Inputs, "validation") })
	file := humanReview(t)
	overridden, err := ApplyOverrides(file, map[string]Override{"review": {Actor: ArtisanActor}})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		a          Authority
		def        *Definition
		candidates []*Definition
		want       Admission
	}{
		{"shipped", shipped, def, nil, Admission{Basis: ByRelease}},
		{"shipped and approved", Authority{Shipped: shipped.Shipped, Approved: approving(def.Digest())}, def, nil, Admission{Basis: ByRelease}},
		{"approved", Authority{Approved: approving(weaker.Digest())}, weaker, nil, Admission{Basis: ByGuildmaster}},
		{"approved below the floor", Authority{Approved: approving(belowFloor.Digest())}, belowFloor, nil, Admission{Basis: ByGuildmaster}},
		{"non-weakening from the shipped default", shipped, stronger, nil, Admission{Basis: NonWeakening, From: def.Revision()}},
		{"non-weakening from an approved candidate", Authority{Approved: approving(file.Digest())}, edit(t, func(d *Definition) {
			*d = *file
			d.Description = "Reworded."
		}), []*Definition{file}, Admission{Basis: NonWeakening, From: file.Revision()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.a.Admit(tc.def, tc.candidates...)
			if err != nil || got != tc.want {
				t.Fatalf("Admit = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}

	refusals := []struct {
		name       string
		a          Authority
		def        *Definition
		candidates []*Definition
		floor      []string
		against    []Revision
	}{
		{"weakening the shipped default", shipped, weaker, nil, nil, []Revision{def.Revision()}},
		{"nothing authorized", Authority{}, stronger, nil, nil, nil},
		{"below the floor, though non-weakening", Authority{Approved: approving(belowFloor.Digest())}, edit(t, func(d *Definition) {
			*d = *belowFloor
			d.Description = "Reworded."
		}), []*Definition{belowFloor}, []string{`deliver step "deliver" takes no validate result`}, nil},
		{"an unapproved candidate is no baseline", shipped, overridden, []*Definition{file}, nil, []Revision{def.Revision()}},
		{"an approved candidate whose override weakens it", Authority{Shipped: shipped.Shipped, Approved: approving(file.Digest())}, overridden, []*Definition{file},
			nil, []Revision{def.Revision(), file.Revision()}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.a.Admit(tc.def, tc.candidates...)
			var refusal *RefusalError
			if !errors.Is(err, ErrNotAuthorized) || !errors.As(err, &refusal) {
				t.Fatalf("Admit = %+v, %v; want a refusal", got, err)
			}
			if got != (Admission{}) || refusal.Revision != tc.def.Revision() || !slices.Equal(refusal.Floor, tc.floor) {
				t.Fatalf("refusal = %+v, admission %+v", refusal, got)
			}
			var against []Revision
			for _, c := range refusal.Against {
				if len(c.Weakenings) == 0 {
					t.Errorf("baseline %s compared without weakenings", c.From.Digest)
				}
				against = append(against, c.From)
			}
			if !slices.Equal(against, tc.against) {
				t.Fatalf("compared with %v, want %v", against, tc.against)
			}
			msg := err.Error()
			for _, want := range append([]string{tc.def.Digest()}, tc.floor...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not name %q", msg, want)
				}
			}
		})
	}
}
