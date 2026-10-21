package interp

import (
	"encoding/json"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestOutputOrdersExitByOriginatingRound(t *testing.T) {
	for _, port := range []string{"spec", "outcome"} {
		t.Run(port, func(t *testing.T) {
			r := run{
				def: &workflow.Definition{Steps: []workflow.Step{{ID: "review", Type: "review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}}}},
				decisions: []store.Decision{
					{ID: "review-2", Answered: true, Choice: changesRequested},
					{ID: "review-10", Answered: true, Choice: changesRequested},
				},
				documents: []store.DocumentRef{{Name: "review-x2-" + port}, {Name: "review-10-" + port}},
			}
			doc, source, err := r.output("review", port)
			if err != nil || doc.Name != "review-x2-"+port || source != "review-x2" {
				t.Fatalf("exit output = %+v, %s, %v", doc, source, err)
			}
			r.documents = append(r.documents, store.DocumentRef{Name: "review-11-" + port})
			doc, source, err = r.output("review", port)
			if err != nil || doc.Name != "review-11-"+port || source != "review-11" {
				t.Fatalf("later output = %+v, %s, %v", doc, source, err)
			}
		})
	}
}
