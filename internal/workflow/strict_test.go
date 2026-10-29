package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExactJSONFields(t *testing.T) {
	// An old /1 document still reads strictly; its rule-less finding is the /1 shape.
	outcome, err := json.Marshal(ReviewOutcome{Schema: ReviewOutcomeSchemaV1, Reviewer: HumanReviewer, Verdict: VerdictRejected, Findings: []Finding{{ID: "f1", File: "a.go", Line: 1, Summary: "fix"}}, Revisions: revisions})
	if err != nil {
		t.Fatal(err)
	}
	outcome2, err := json.Marshal(ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: HumanReviewer, Verdict: VerdictRejected, Summary: "Not this.",
		Findings: []Finding{{ID: "f1", Rule: "S2", File: "a.go", Line: 1, Summary: "fix"}}, Suggestions: []Suggestion{{ID: "s1", Summary: "maybe"}},
		Dispositions: []Disposition{{Finding: "f1", State: Disputed, Reason: "still"}}, Revisions: revisions})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, data string
		fields     []string
		parse      func([]byte) error
	}{
		{"workflow", strings.Replace(valid, `"name": "small"`, `"description": "test", "name": "small"`, 1), []string{"schema", "name", "description", "steps", "id", "type", "version", "settings", "inputs", "on", "goto", "max"}, func(b []byte) error { _, err := Parse(b); return err }},
		{"outcome", string(outcome), []string{"schema", "reviewer", "verdict", "findings", "revisions", "file", "line", "summary", "subject", "code_head", "spec_digest", "standards_revision"}, func(b []byte) error { _, err := ParseReviewOutcome(b); return err }},
		{"outcome/2", string(outcome2), []string{"schema", "reviewer", "verdict", "summary", "findings", "rule", "suggestions", "dispositions", "finding", "state", "reason", "revisions", "subject"}, func(b []byte) error { _, err := ParseReviewOutcome(b); return err }},
	}
	for _, c := range cases {
		if err := c.parse([]byte(c.data)); err != nil {
			t.Fatal(err)
		}
		for _, field := range c.fields {
			for _, alias := range []string{strings.ToUpper(field), "unknown_" + field} {
				t.Run(c.name+"/"+alias, func(t *testing.T) {
					data := strings.Replace(c.data, `"`+field+`":`, `"`+alias+`":`, 1)
					if err := c.parse([]byte(data)); err == nil {
						t.Fatal("non-exact field accepted")
					}
				})
			}
		}
	}
	shadow := strings.Replace(string(outcome), `"verdict":"rejected"`, `"verdict":"rejected","Verdict":"approved"`, 1)
	if _, err := ParseReviewOutcome([]byte(shadow)); err == nil {
		t.Fatal("shadow verdict accepted")
	}
	for _, c := range cases {
		newer := strings.Replace(c.data, `/1"`, `/2"`, 1)
		prefix := SchemaPrefix
		if strings.HasPrefix(c.name, "outcome") {
			newer, prefix = strings.Replace(strings.Replace(c.data, `/1"`, `/3"`, 1), `/2"`, `/3"`, 1), reviewOutcomePrefix
		}
		newer = strings.TrimSuffix(newer, "}") + `,"Schema":"` + prefix + `1"}`
		if err := c.parse([]byte(newer)); !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("newer schema shadowed: %v", err)
		}
	}
}

func TestInputReferenceArrays(t *testing.T) {
	for _, refs := range []Refs{{"grill.notes"}, {"grill.notes", "research.notes"}} {
		encoded, err := json.Marshal(refs)
		if err != nil {
			t.Fatal(err)
		}
		data := `{"schema":"guildhall.workflow/1","name":"arrays","steps":[{"id":"grill","type":"grill","version":1,"settings":{"actor":"director"}},{"id":"research","type":"research","version":1},{"id":"plan","type":"plan","version":1,"inputs":{"context":` + string(encoded) + `}}]}`
		def, err := load(t, data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(def.Steps[2].Inputs["context"], refs) {
			t.Fatal("references changed")
		}
		scalar := strings.Replace(data, string(encoded), `"grill.notes"`, 1)
		if _, err := Parse([]byte(scalar)); err == nil {
			t.Fatal("scalar reference accepted")
		}
	}
}

func TestMapKeysPreserveCase(t *testing.T) {
	data := `{"schema":"guildhall.workflow/1","name":"maps","steps":[{"id":"plan","type":"plan","version":1,"settings":{"x":{"Name":1,"name":2}},"inputs":{"Context":["grill.notes"],"context":["research.notes"]},"on":{"Changes_requested":{"goto":"plan","max":1}}}]}`
	def, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Steps[0].Inputs) != 2 || def.Steps[0].On["Changes_requested"].Max != 1 {
		t.Fatal("map keys changed")
	}
	if err := Validate(def); err == nil {
		t.Fatal("unknown map keys bypassed validation")
	}
	data = strings.Replace(data, `"settings":`, `"Settings":`, 1)
	if _, err := Parse([]byte(data)); err == nil {
		t.Fatal("settings alias accepted")
	}
}
