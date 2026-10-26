package workflow_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

const refuteWorkflow = `{"schema":"guildhall.workflow/1","name":"refute-trial","steps":[
 {"id":"research","type":"research","version":1},
 {"id":"plan","type":"plan","version":1},
 {"id":"attack","type":"refute","version":1,"settings":{"profile":"critic","skills":["go-tdd"]},"inputs":{"spec":["plan.spec"],"context":["research.notes","plan.spec"]}},
 {"id":"accept","type":"guildmaster-approval","version":1,"inputs":{"spec":["plan.spec"],"findings":["attack.findings"]},"on":{"changes_requested":{"goto":"plan","max":1}}}
]}`

func TestRefutePortsAndWiring(t *testing.T) {
	k, ok := workflow.LookupKind("refute", 1)
	if !ok {
		t.Fatal("missing refute v1")
	}
	if !reflect.DeepEqual(k.Inputs, []workflow.Port{{Name: "spec", Type: workflow.Document}, {Name: "context", Type: workflow.Document, Optional: true, Many: true}}) || !reflect.DeepEqual(k.Outputs, []workflow.Port{{Name: "findings", Type: workflow.Document}}) {
		t.Fatalf("ports = %+v -> %+v", k.Inputs, k.Outputs)
	}
	if !reflect.DeepEqual(k.Actors, []workflow.Actor{workflow.ArtisanActor}) || len(k.Loops) != 0 {
		t.Fatalf("unexpected actor/loop contract: %+v", k)
	}
	for _, tc := range []struct {
		name, old, replacement string
		valid                  bool
	}{
		{name: "complete", valid: true},
		{"optional context", `,"context":["research.notes","plan.spec"]`, ``, true},
		{"required spec", `"spec":["plan.spec"],`, ``, false},
		{"wrong input type", `"spec":["plan.spec"],"findings"`, `"spec":["accept.spec"],"findings"`, false},
		{"wrong findings type", `"attack.findings"`, `"accept.spec"`, false},
		{"actor forbidden", `"profile":"critic"`, `"actor":"guildmaster"`, false},
		{"unknown setting", `"profile":"critic"`, `"instructions":"attack"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := refuteWorkflow
			if tc.old != "" {
				data = strings.Replace(data, tc.old, tc.replacement, 1)
			}
			d, err := workflow.Parse([]byte(data))
			if err == nil {
				err = workflow.Validate(d)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestRefuteTrialExample(t *testing.T) {
	data, err := os.ReadFile("../../examples/workflows/refute.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := workflow.Parse(data)
	if err == nil {
		err = workflow.Validate(d)
	}
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, s := range d.Steps {
		positions[s.ID] = i
	}
	attack := d.Steps[positions["refute"]]
	accept := d.Steps[positions["accept"]]
	if attack.Type != "refute" || positions["plan"] >= positions["refute"] || positions["refute"] >= positions["accept"] || !reflect.DeepEqual(accept.Inputs["findings"], workflow.Refs{"refute.findings"}) {
		t.Fatalf("bad trial wiring: %+v", d)
	}
	defaultDef, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range defaultDef.Steps {
		if s.Type == "refute" {
			t.Fatal("default must not enable trial")
		}
	}
}
