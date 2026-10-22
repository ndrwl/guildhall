package interp_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// registeredOutbox fails its test on any Director message whose type is not in store.MessageTypes, the registry
// the wake line and DirectorUnread count by; every engine env sends through one, so each suite's sends are checked.
type registeredOutbox struct {
	interp.Outbox
	t *testing.T
}

func (o registeredOutbox) Send(to store.Recipient, envelope messaging.Envelope) (store.Receipt, error) {
	if _, ok := store.MessageTypes[envelope.Type]; to.Role == store.RecipientDirector && !ok {
		o.t.Errorf("the engine sent the Director message %s of type %q, which is not in store.MessageTypes", envelope.ID, envelope.Type)
	}
	return o.Outbox.Send(to, envelope)
}
