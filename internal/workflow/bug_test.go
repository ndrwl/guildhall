package workflow

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
)

// TestBugIsTheDefaultWithoutAcceptance: the shipped bug workflow validates and has the shipped default's steps, in the
// same order, without grill and accept. Plan's context is research's notes only, build and review take plan's spec,
// research, plan, build and review carry instructions, and every other step equals the default's.
func TestBugIsTheDefaultWithoutAcceptance(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	bug, err := Bug()
	if err != nil {
		t.Fatal(err)
	}
	if bug.Name != "bug" || bug.Digest() == def.Digest() {
		t.Fatalf("bug = %s %s", bug.Name, bug.Digest())
	}
	var defaults []Step
	for _, s := range def.Steps {
		if s.ID != "grill" && s.ID != "accept" {
			defaults = append(defaults, s)
		}
	}
	if len(bug.Steps) != len(defaults) {
		t.Fatalf("bug has %d steps, want %d", len(bug.Steps), len(defaults))
	}
	instructed := map[string]string{"research": "Reproduce the defect", "plan": "no Guildmaster acceptance", "build": "regression test", "review": "regression test"}
	inputs := map[string]map[string]Refs{
		"plan":   {"context": {"research.notes"}},
		"build":  {"spec": {"plan.spec"}},
		"review": {"spec": {"plan.spec"}, "head": {"build.head"}},
	}
	for i, s := range bug.Steps {
		want := defaults[i]
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
		if in, ok := inputs[s.ID]; ok {
			if !reflect.DeepEqual(s.Inputs, in) {
				t.Errorf("step %s inputs = %v, want %v", s.ID, s.Inputs, in)
			}
			s.Inputs, want.Inputs = nil, nil
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
			t.Errorf("step %d differs from the default beyond its instructions and inputs:\n%s\n%s", i, got, other)
		}
	}
}

func TestPlanAcceptsInstructions(t *testing.T) {
	for settings, want := range map[string]string{
		`{"instructions":"Proceed on the recommended answer."}`:               "",
		`{"profile":"planner","skills":["go-tdd"],"instructions":"Proceed."}`: "",
		`{"instructions":["no"]}`:                                             `setting "instructions" must be a string`,
	} {
		def, err := Parse([]byte(withSettings("plan", settings)))
		if err != nil {
			t.Fatal(err)
		}
		err = Validate(def)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", settings, err, want)
		}
	}
}

// TestSpecPortTakesADocumentOrAnAcceptedSpec: the spec input of build, review and rebase accepts a plan's document as
// well as an accepted spec, and nothing else; other ports keep their single type.
func TestSpecPortTakesADocumentOrAnAcceptedSpec(t *testing.T) {
	for _, typ := range []string{"build", "review", "rebase"} {
		k, _ := LookupKind(typ, 1)
		spec, ok := k.input("spec")
		if !ok || !spec.Accepts(AcceptedSpec) || !spec.Accepts(Document) || spec.Accepts(CodeHead) || spec.Accepts(ReviewResult) {
			t.Errorf("%s spec port = %+v", typ, spec)
		}
	}
	k, _ := LookupKind("review", 1)
	if head, _ := k.input("head"); head.Accepts(Document) || head.Accepts(AcceptedSpec) || !head.Accepts(CodeHead) {
		t.Errorf("review head port = %+v", head)
	}
	k, _ = LookupKind("refute", 1)
	if spec, _ := k.input("spec"); spec.Accepts(AcceptedSpec) || !spec.Accepts(Document) {
		t.Errorf("refute spec port = %+v", spec)
	}
	for _, c := range []struct{ name, old, new, want string }{
		{"build from accept", "", "", ""},
		{"build and review from plan", `"spec": ["accept.spec"]`, `"spec": ["plan.spec"]`, ""},
		{"spec from a code head", `"spec": ["accept.spec"], "head"`, `"spec": ["build.head"], "head"`, `input "spec" wants accepted-spec but build.head is code-head`},
		{"head from a document", `"head": ["build.head"]}`, `"head": ["plan.spec"]}`, `input "head" wants code-head but plan.spec is document`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(t, strings.ReplaceAll(valid, c.old, c.new))
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
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
