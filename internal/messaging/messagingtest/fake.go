package messagingtest

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// Fake is an in-memory messaging.Mailbox that passes RunMailboxContract, for
// testing a consumer without a store file. It applies the store's field
// validation but holds its state only for its own lifetime: it is not durable
// and is not shared between processes. Receipts carry no request digest or commit time.
type Fake struct {
	mu       sync.Mutex
	tasks    map[[2]string]bool
	attempts map[fakeAttempt]bool // value: still running
	started  map[[3]string]int
	messages map[string]store.Message
	sent     map[string]store.Receipt
	acks     map[string]fakeAck
	seq      uint64
}

type fakeAttempt struct {
	repository, task, step string
	number                 int
}

type fakeAck struct {
	to      store.Recipient
	receipt store.Receipt
}

var _ messaging.Mailbox = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		tasks: map[[2]string]bool{}, attempts: map[fakeAttempt]bool{}, started: map[[3]string]int{},
		messages: map[string]store.Message{}, sent: map[string]store.Receipt{}, acks: map[string]fakeAck{},
	}
}

// AddTask records an open task messages may concern.
func (f *Fake) AddTask(repository, task string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[[2]string{repository, task}] = true
}

// StartAttempt starts the next attempt of step and returns its number.
func (f *Fake) StartAttempt(repository, task, step string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [3]string{repository, task, step}
	f.started[k]++
	f.attempts[fakeAttempt{repository, task, step, f.started[k]}] = true
	return f.started[k]
}

// FinishAttempt marks an attempt finished.
func (f *Fake) FinishAttempt(repository, task, step string, number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[fakeAttempt{repository, task, step, number}] = false
}

// Send implements messaging.Mailbox.
func (f *Fake) Send(to store.Recipient, sender string, e messaging.Envelope) (store.Receipt, error) {
	m, err := messaging.Message(to, e)
	if err != nil {
		return store.Receipt{}, err
	}
	m.Sender = sender
	if err := store.ValidateMessage(m); err != nil {
		return store.Receipt{}, err
	}
	m.Artifacts = append([]store.ArtifactRef(nil), m.Artifacts...)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.sent[m.ID]; ok {
		// As in the store, the sender is not part of the request: a resend keeps the first.
		prior := f.messages[m.ID]
		prior.Sequence, prior.Acknowledged, prior.Sender = 0, false, m.Sender
		if !reflect.DeepEqual(prior, m) {
			return store.Receipt{}, fmt.Errorf("messagingtest: %w: message %q", store.ErrMutationConflict, m.ID)
		}
		return r, nil
	}
	if !f.tasks[[2]string{m.Repository, m.TaskID}] {
		return store.Receipt{}, fmt.Errorf("messagingtest: %w: task %s/%s", store.ErrNotFound, m.Repository, m.TaskID)
	}
	if m.Attempt > 0 {
		running, ok := f.attempts[fakeAttempt{m.Repository, m.TaskID, m.Step, m.Attempt}]
		switch {
		case !ok:
			return store.Receipt{}, fmt.Errorf("messagingtest: %w: attempt %d of step %q", store.ErrNotFound, m.Attempt, m.Step)
		case m.Recipient == store.RecipientArtisan && !running:
			return store.Receipt{}, fmt.Errorf("messagingtest: %w: attempt %d of step %q already finished", store.ErrInvalid, m.Attempt, m.Step)
		}
	}
	f.seq++
	m.Sequence = f.seq
	r := store.Receipt{MutationID: "message:" + m.ID, Operation: "send-message", Repository: m.Repository, TaskID: m.TaskID, Attempt: m.Attempt, Sequence: f.seq}
	f.messages[m.ID], f.sent[m.ID] = m, r
	return r, nil
}

// List implements messaging.Mailbox.
func (f *Fake) List(to store.Recipient, after uint64, limit int) (messaging.Page, error) {
	if err := store.ValidateRecipient(to); err != nil {
		return messaging.Page{}, err
	}
	if limit < 1 || limit > store.MaxInboxLimit {
		return messaging.Page{}, fmt.Errorf("messagingtest: %w: inbox limit %d", store.ErrInvalid, limit)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var msgs []store.Message
	for _, m := range f.messages {
		if !m.Acknowledged && m.Sequence > after && m.To() == to {
			msgs = append(msgs, m)
		}
	}
	slices.SortFunc(msgs, func(a, b store.Message) int { return cmp.Compare(a.Sequence, b.Sequence) })
	page := messaging.Page{}
	if len(msgs) > limit {
		msgs, page.More = msgs[:limit], true
	}
	for _, m := range msgs {
		m.Artifacts = slices.Clone(m.Artifacts)
		page.Messages = append(page.Messages, messaging.StoredMessage(m))
	}
	return page, nil
}

// Read implements messaging.Mailbox.
func (f *Fake) Read(id string) (messaging.Stored, error) {
	if err := store.ValidateMessageID(id); err != nil {
		return messaging.Stored{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok {
		return messaging.Stored{}, fmt.Errorf("messagingtest: %w: message %q", store.ErrNotFound, id)
	}
	m.Artifacts = slices.Clone(m.Artifacts)
	return messaging.StoredMessage(m), nil
}

// Ack implements messaging.Mailbox.
func (f *Fake) Ack(to store.Recipient, id string) (store.Receipt, error) {
	if err := store.ValidateRecipient(to); err != nil {
		return store.Receipt{}, err
	}
	if err := store.ValidateMessageID(id); err != nil {
		return store.Receipt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.acks[id]; ok {
		if a.to != to {
			return store.Receipt{}, fmt.Errorf("messagingtest: %w: acknowledgement of %q", store.ErrMutationConflict, id)
		}
		return a.receipt, nil
	}
	m, ok := f.messages[id]
	switch {
	case !ok:
		return store.Receipt{}, fmt.Errorf("messagingtest: %w: message %q", store.ErrNotFound, id)
	case m.To() != to:
		return store.Receipt{}, fmt.Errorf("messagingtest: %w: message %q is addressed to %s", store.ErrInvalid, id, messaging.FormatRecipient(m.To()))
	}
	f.seq++
	m.Acknowledged = true
	r := store.Receipt{MutationID: "message-ack:" + id, Operation: "ack-message", Repository: m.Repository, TaskID: m.TaskID, Attempt: m.Attempt, Sequence: f.seq}
	f.messages[id], f.acks[id] = m, fakeAck{to, r}
	return r, nil
}
