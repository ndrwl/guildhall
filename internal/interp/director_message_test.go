package interp

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

type sentOutbox []messaging.Envelope

func (o *sentOutbox) Send(_ store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	*o = append(*o, e)
	return store.Receipt{}, nil
}

// TestNotifySendsOnlyRegisteredTypes shows the engine's one send of a Director message refuses a type not in
// store.MessageTypes, naming it, and sends nothing, while a registered type is sent.
func TestNotifySendsOnlyRegisteredTypes(t *testing.T) {
	var sent sentOutbox
	r := &run{e: &Engine{Outbox: &sent}, task: store.Task{Repository: "app", ID: "t7"}}
	err := r.notify("made-up.1", "made-up", "a type no sender registered")
	if err == nil || !strings.Contains(err.Error(), `"made-up"`) || !strings.Contains(err.Error(), "store.MessageTypes") {
		t.Fatalf("notify of an unregistered type = %v, want an error naming the type", err)
	}
	if len(sent) != 0 {
		t.Fatalf("notify of an unregistered type sent %+v", sent)
	}
	if err := r.notify("merged.d1", store.MessageMerged, "merged"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Type != store.MessageMerged || sent[0].ID != "app.t7.merged.d1" {
		t.Fatalf("notify of a registered type sent %+v", sent)
	}
}
