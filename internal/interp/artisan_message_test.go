package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/messaging/messagingtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestArtisanMessageTypesRegistry: the registry of Guildhall-sent Artisan message types starts with nudge, and
// every entry says what it tells the Artisan.
func TestArtisanMessageTypesRegistry(t *testing.T) {
	if store.MessageNudge != "nudge" {
		t.Errorf("MessageNudge = %q, want nudge", store.MessageNudge)
	}
	if _, ok := store.ArtisanMessageTypes[store.MessageNudge]; !ok {
		t.Errorf("ArtisanMessageTypes lacks %q", store.MessageNudge)
	}
	for typ, what := range store.ArtisanMessageTypes {
		if typ == "" || strings.TrimSpace(what) == "" {
			t.Errorf("ArtisanMessageTypes[%q] = %q, want a type and what it tells the Artisan", typ, what)
		}
	}
}

// TestGuildhallOutboxSendsOnlyRegisteredArtisanTypes: Guildhall's own send to an Artisan's inbox takes a type in
// store.ArtisanMessageTypes, recorded with sender guildhall, and refuses any other type, sending nothing; a
// message to the Director is not checked against that registry.
func TestGuildhallOutboxSendsOnlyRegisteredArtisanTypes(t *testing.T) {
	box := messagingtest.NewFake()
	box.AddTask("app", "t1")
	n := box.StartAttempt("app", "t1", "build")
	to := store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t1", Step: "build", Attempt: n}
	envelope := func(id, typ string) messaging.Envelope {
		return messaging.Envelope{
			Schema: "guildhall.message/1", ID: id, Recipient: store.RecipientArtisan,
			Repository: "app", Task: "t1", Step: "build", Attempt: n, Type: typ, Text: "submit your result before you exit",
		}
	}
	out := interp.GuildhallOutbox{Mailbox: box}

	_, err := out.Send(to, envelope("t1.note", store.MessageNote))
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), `"note"`) || !strings.Contains(err.Error(), "store.ArtisanMessageTypes") {
		t.Fatalf("Send of an unregistered artisan type = %v, want ErrInvalid naming the type and the registry", err)
	}
	if page, err := box.List(to, 0, store.MaxInboxLimit); err != nil || len(page.Messages) != 0 {
		t.Fatalf("after a refused send the inbox holds %+v (%v), want nothing", page.Messages, err)
	}

	if _, err := out.Send(to, envelope("t1.nudge", store.MessageNudge)); err != nil {
		t.Fatalf("Send of nudge: %v", err)
	}
	page, err := box.List(to, 0, store.MaxInboxLimit)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("inbox = %+v (%v), want the nudge", page.Messages, err)
	}
	if m := page.Messages[0]; m.Type != store.MessageNudge || m.Sender != store.SenderGuildhall {
		t.Errorf("nudge stored as type %q from %q, want nudge from guildhall", m.Type, m.Sender)
	}

	director := messaging.Envelope{Schema: "guildhall.message/1", ID: "t1.other", Recipient: store.RecipientDirector, Repository: "app", Task: "t1", Type: "made-up", Text: "x"}
	if _, err := out.Send(store.Recipient{Role: store.RecipientDirector}, director); err != nil {
		t.Errorf("Send to the Director of a type outside ArtisanMessageTypes = %v, want it sent", err)
	}
}
