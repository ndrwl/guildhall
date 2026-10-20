package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// valid is a small well-formed workflow that the table tests below mutate.
const valid = `{
  "schema": "guildhall.workflow/1",
  "name": "small",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": "plan.spec"},
     "on": {"changes_requested": {"goto": "plan", "max": 2}}},
    {"id": "build", "type": "build", "version": 1, "inputs": {"spec": "accept.spec"}},
    {"id": "review", "type": "automated-review", "version": 1,
     "inputs": {"spec": "accept.spec", "head": "build.head"},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": "build.head", "review": "review.outcome"}}
  ]
}`

func load(t *testing.T, data string) (*Definition, error) {
	t.Helper()
	def, err := Parse([]byte(data))
	if err != nil {
		return nil, err
	}
	return def, Validate(def)
}

func TestValidWorkflowLoads(t *testing.T) {
	def, err := load(t, valid)
	if err != nil {
		t.Fatalf("valid workflow refused: %v", err)
	}
	if def.Name != "small" || len(def.Steps) != 5 {
		t.Fatalf("unexpected definition: %+v", def)
	}
}

func TestShippedWorkflowsValidate(t *testing.T) {
	def, err := Default()
	if err != nil {
		t.Fatalf("default workflow: %v", err)
	}
	if def.Name != "default" {
		t.Fatalf("default workflow name = %q", def.Name)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "examples", "workflows", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no example workflows found: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := load(t, string(data)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestSchemaRefusals(t *testing.T) {
	cases := []struct {
		name  string
		data  string
		newer bool
	}{
		{"newer schema", `{"schema": "guildhall.workflow/2", "name": "x", "steps": []}`, true},
		{"unknown schema", `{"schema": "other/1", "name": "x", "steps": []}`, false},
		{"missing schema", `{"name": "x", "steps": []}`, false},
		{"malformed json", `{"schema": "guildhall.workflow/1", "name": `, false},
		{"unknown field", `{"schema": "guildhall.workflow/1", "name": "x", "steps": [], "run": "rm -rf /"}`, false},
		{"unknown step field", `{"schema": "guildhall.workflow/1", "name": "x", "steps": [{"id": "b", "type": "build", "version": 1, "command": "make"}]}`, false},
		{"duplicate key", `{"schema": "guildhall.workflow/1", "name": "x", "name": "y", "steps": []}`, false},
		{"trailing data", `{"schema": "guildhall.workflow/1", "name": "x", "steps": []} {}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.data))
			if err == nil {
				t.Fatal("accepted")
			}
			if got := errors.Is(err, ErrNewerSchema); got != c.newer {
				t.Fatalf("errors.Is(ErrNewerSchema) = %v, err = %v", got, err)
			}
		})
	}
}

func TestValidationRefusals(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"unknown step type", `"type": "build"`, `"type": "shell"`, `unknown step type "shell"`},
		{"unsupported version", `"id": "build", "type": "build", "version": 1`, `"id": "build", "type": "build", "version": 9`, `does not support version 9`},
		{"duplicate id", `"id": "deliver"`, `"id": "build"`, `duplicate step id "build"`},
		{"invalid id", `"id": "deliver"`, `"id": "Deliver Now"`, `invalid step id`},
		{"missing required input", `"inputs": {"spec": "accept.spec"}}`, `"inputs": {}}`, `required input "spec" is not wired`},
		{"unknown input", `"inputs": {"spec": "accept.spec"}}`, `"inputs": {"spec": "accept.spec", "extra": "plan.spec"}}`, `unknown input "extra"`},
		{"unknown reference step", `"head": "build.head", "review"`, `"head": "built.head", "review"`, `unknown step "built"`},
		{"unknown output", `"head": "build.head", "review"`, `"head": "build.binary", "review"`, `has no output "binary"`},
		{"type mismatch", `"head": "build.head", "review"`, `"head": "plan.spec", "review"`, `wants code-head`},
		{"forward reference", `"inputs": {"spec": "plan.spec"}`, `"inputs": {"spec": "build.head"}`, `must come from an earlier step`},
		{"self reference", `"inputs": {"spec": "accept.spec"}}`, `"inputs": {"spec": "build.head"}}`, `must come from an earlier step`},
		{"bad reference syntax", `"inputs": {"spec": "plan.spec"}`, `"inputs": {"spec": "plan"}`, `want "step.output"`},
		{"unbounded loop", `"goto": "build", "max": 3`, `"goto": "build"`, `max must be between 1 and`},
		{"excessive loop", `"goto": "build", "max": 3`, `"goto": "build", "max": 1000`, `max must be between 1 and`},
		{"forward goto", `"goto": "plan", "max": 2`, `"goto": "deliver", "max": 2`, `can only loop back`},
		{"unknown goto", `"goto": "build", "max": 3`, `"goto": "fix", "max": 3`, `unknown step "fix"`},
		{"outcome without rule", `"on": {"changes_requested": {"goto": "build"`, `"on": {"approved": {"goto": "build"`, `outcome "approved" cannot carry a rule`},
		{"unknown setting", `{"id": "plan", "type": "plan", "version": 1}`, `{"id": "plan", "type": "plan", "version": 1, "settings": {"script": "x"}}`, `unknown setting "script"`},
		{"wrong setting type", `"type": "automated-review", "version": 1,`, `"type": "automated-review", "version": 1, "settings": {"instructions": 5},`, `setting "instructions" must be a string`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(valid, c.old) {
				t.Fatalf("fixture does not contain %q", c.old)
			}
			_, err := load(t, strings.Replace(valid, c.old, c.new, 1))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	data := strings.Replace(valid, `"type": "build"`, `"type": "shell"`, 1)
	data = strings.Replace(data, `"goto": "plan", "max": 2`, `"goto": "plan"`, 1)
	_, err := load(t, data)
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Problems) < 2 {
		t.Fatalf("want at least two problems, got %v", err)
	}
}

func TestRequiredSetting(t *testing.T) {
	data := `{"schema": "guildhall.workflow/1", "name": "wait", "steps": [{"id": "wait", "type": "handoff-wait", "version": 1}]}`
	if _, err := load(t, data); err == nil || !strings.Contains(err.Error(), `required setting "from"`) {
		t.Fatalf("missing required setting accepted: %v", err)
	}
	data = strings.Replace(data, `"version": 1}`, `"version": 1, "settings": {"from": "other-director"}}`, 1)
	if _, err := load(t, data); err != nil {
		t.Fatalf("handoff-wait with from refused: %v", err)
	}
}

func TestDigest(t *testing.T) {
	a, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	reformatted := strings.Join(strings.Fields(valid), " ")
	b, err := load(t, reformatted)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Fatalf("whitespace changed digest: %s != %s", a.Digest(), b.Digest())
	}
	if !strings.HasPrefix(a.Digest(), "sha256:") || len(a.Digest()) != len("sha256:")+64 {
		t.Fatalf("digest %q is not sha256:<hex>", a.Digest())
	}
	c, err := load(t, strings.Replace(valid, `"max": 3`, `"max": 2`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() == c.Digest() {
		t.Fatal("content change kept digest")
	}
	rev := a.Revision()
	if rev.Name != "small" || rev.Digest != a.Digest() {
		t.Fatalf("revision = %+v", rev)
	}
}

func TestReviewOverride(t *testing.T) {
	def, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := ApplyOverrides(def, map[string]Override{"review": {Type: "human-review", Version: 1}})
	if err != nil {
		t.Fatalf("review swap refused: %v", err)
	}
	if err := Validate(swapped); err != nil {
		t.Fatalf("swapped workflow invalid: %v", err)
	}
	if got := swapped.Steps[3].Type; got != "human-review" {
		t.Fatalf("review step type = %q", got)
	}
	if def.Steps[3].Type != "automated-review" {
		t.Fatal("override mutated the original definition")
	}
	if swapped.Digest() == def.Digest() {
		t.Fatal("override kept digest")
	}
	same, err := ApplyOverrides(def, nil)
	if err != nil || same.Digest() != def.Digest() {
		t.Fatalf("empty override changed workflow: %v", err)
	}

	refusals := []struct {
		name string
		ov   map[string]Override
		want string
	}{
		{"unknown step", map[string]Override{"nope": {Type: "human-review", Version: 1}}, `unknown step "nope"`},
		{"non-review step", map[string]Override{"build": {Type: "human-review", Version: 1}}, `only review steps`},
		{"non-review target", map[string]Override{"review": {Type: "deliver", Version: 1}}, `only review steps`},
		{"unknown version", map[string]Override{"review": {Type: "human-review", Version: 7}}, `does not support version 7`},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			_, err := ApplyOverrides(def, c.ov)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

func TestEveryBuiltinKindIsUsable(t *testing.T) {
	for _, k := range Kinds() {
		if k.Type == "" || k.Version < 1 {
			t.Errorf("bad kind %+v", k)
		}
		if _, ok := LookupKind(k.Type, k.Version); !ok {
			t.Errorf("kind %s@%d not found", k.Type, k.Version)
		}
		for _, p := range append(append([]Port{}, k.Inputs...), k.Outputs...) {
			if p.Name == "" || p.Type == "" {
				t.Errorf("%s has an unnamed or untyped port", k.Type)
			}
		}
	}
	if _, ok := LookupKind("deliver", 0); ok {
		t.Error("version 0 resolved")
	}
	for _, name := range []string{"grill", "research", "plan", "guildmaster-approval", "build", "automated-review", "human-review", "validate", "deliver", "handoff-wait"} {
		if _, ok := LookupKind(name, 1); !ok {
			t.Errorf("built-in %s@1 missing", name)
		}
	}
}
