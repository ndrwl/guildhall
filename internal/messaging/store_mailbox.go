package messaging

import (
	"fmt"
	"strconv"

	"github.com/ndrwl/guildhall/internal/store"
)

// StoreMailbox is the Mailbox kept in a Director's store.
type StoreMailbox struct {
	Store *store.Store
}

var _ Mailbox = (*StoreMailbox)(nil)

// Send implements Mailbox.Send.
func (b *StoreMailbox) Send(to store.Recipient, e Envelope) (store.Receipt, error) {
	m, err := Message(to, e)
	if err != nil {
		return store.Receipt{}, err
	}
	r, err := b.Store.SendMessage(m)
	if err != nil {
		return store.Receipt{}, fmt.Errorf("messaging: %w", err)
	}
	return r, nil
}

// List implements Mailbox.List.
func (b *StoreMailbox) List(to store.Recipient, after uint64, limit int) (Page, error) {
	msgs, more, err := b.Store.Inbox(to, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("messaging: %w", err)
	}
	page := Page{Messages: make([]Stored, len(msgs)), More: more}
	for i, m := range msgs {
		page.Messages[i] = StoredMessage(m)
	}
	return page, nil
}

// Read implements Mailbox.Read.
func (b *StoreMailbox) Read(id string) (Stored, error) {
	m, err := b.Store.Message(id)
	if err != nil {
		return Stored{}, fmt.Errorf("messaging: %w", err)
	}
	return StoredMessage(m), nil
}

// Ack implements Mailbox.Ack.
func (b *StoreMailbox) Ack(to store.Recipient, id string) (store.Receipt, error) {
	r, err := b.Store.AckMessage(to, id)
	if err != nil {
		return store.Receipt{}, fmt.Errorf("messaging: %w", err)
	}
	return r, nil
}

// Message converts an envelope sent to to into the store record. It refuses an
// unsupported schema and an envelope that names a different recipient than to,
// so a message cannot land in an inbox its sender did not name; the store checks the rest.
func Message(to store.Recipient, e Envelope) (store.Message, error) {
	if e.Schema != SchemaPrefix+strconv.Itoa(SchemaVersion) {
		return store.Message{}, invalid("schema %q is not %s%d", e.Schema, SchemaPrefix, SchemaVersion)
	}
	m := store.Message{ID: e.ID, Recipient: e.Recipient, Repository: e.Repository, TaskID: e.Task, Step: e.Step, Attempt: e.Attempt,
		Type: e.Type, Text: e.Text, Artifacts: e.Artifacts}
	if m.To() != to {
		return store.Message{}, invalid("message %q is addressed to %s, not %s", e.ID, FormatRecipient(m.To()), FormatRecipient(to))
	}
	return m, nil
}

// StoredMessage converts a store record into its envelope view.
func StoredMessage(m store.Message) Stored {
	return Stored{
		Envelope: Envelope{Schema: SchemaPrefix + strconv.Itoa(SchemaVersion), ID: m.ID, Recipient: m.Recipient, Repository: m.Repository, Task: m.TaskID,
			Step: m.Step, Attempt: m.Attempt, Type: m.Type, Text: m.Text, Artifacts: m.Artifacts},
		Sequence:     m.Sequence,
		Acknowledged: m.Acknowledged,
	}
}
