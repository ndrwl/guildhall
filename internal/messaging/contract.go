// Package messaging is local, durable Director–Artisan messaging on a Director's store.
//
// A message is a versioned JSON envelope read from a file or standard input,
// never from shell-quoted arguments. Send validates it, commits it to the
// recipient's inbox in the store with a receipt and only then tells an optional
// Notifier; the store, not the notification, is the record of delivery.
// Acceptance means stored, not read: a recipient lists its inbox and acknowledges
// each message it has consumed, and unacknowledged messages stay listed across restarts.
//
// Messages carry information only. Sending, reading or acknowledging one never
// records a task result, answers a decision, approves anything or controls an
// attempt, and the sender is not authenticated: whoever can write the store can send.
//
// Mailbox is the contract consumers depend on; StoreMailbox implements it on
// a store, and messagingtest provides an in-memory fake and the shared contract
// suite every implementation must pass.
// See docs/messaging.md for the envelope and the command line.
package messaging

import (
	"errors"

	"github.com/ndrwl/guildhall/internal/store"
)

// SchemaPrefix and SchemaVersion form the envelope "schema" value, "guildhall.message/1".
const (
	SchemaPrefix  = "guildhall.message/"
	SchemaVersion = 1
)

// MaxEnvelopeBytes bounds an envelope read by Decode.
const MaxEnvelopeBytes = 128 << 10

var (
	// ErrInvalid reports a malformed envelope or recipient, or a recipient that does not match the envelope.
	ErrInvalid = errors.New("invalid message")
	// ErrNewerSchema reports an envelope written for a newer Guildhall; it is refused rather than guessed at.
	ErrNewerSchema = errors.New("message schema is newer than this Guildhall supports")
)

// Envelope is the versioned wire form of one message.
type Envelope struct {
	Schema string `json:"schema"`
	// ID is chosen by the sender and unique in the store; resending the same envelope is safe.
	ID        string              `json:"id"`
	Recipient store.RecipientRole `json:"recipient"`
	// Repository and Task name the task the message concerns.
	Repository string `json:"repository"`
	Task       string `json:"task"`
	// Step and Attempt name the stage attempt the message concerns; an Artisan message is addressed to it.
	Step    string `json:"step,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	// Type is a lowercase id such as "question" or "progress"; Guildhall does not interpret it.
	Type string `json:"type"`
	// Text is concise prose; longer content belongs in a referenced artifact.
	Text      string              `json:"text"`
	Artifacts []store.ArtifactRef `json:"artifacts,omitempty"`
}

// Stored is an envelope as recorded in the store.
type Stored struct {
	Envelope
	// Sequence orders messages in the store; pass the last one seen as List's after.
	Sequence     uint64 `json:"sequence"`
	Acknowledged bool   `json:"acknowledged"`
}

// Page is one bounded page of an inbox.
type Page struct {
	Messages []Stored `json:"messages"`
	// More reports that unacknowledged messages remain after this page.
	More bool `json:"more"`
}

// Notifier is told about a message after the store committed it, for example
// to wake a runtime adapter. It is a hint only: it may be called again for the
// same message when a send is retried, and missing a notification loses nothing
// because the message stays in the inbox until acknowledged.
type Notifier func(store.Message, store.Receipt)

// Decode reads one envelope strictly: unknown, duplicate or case-variant fields,
// trailing data, more than MaxEnvelopeBytes and unknown or newer schemas are refused.

// Mailbox is local Director–Artisan messaging as its consumers see it.
//
// Send stores an envelope addressed to to and returns its receipt only once the
// message is durable. The envelope must name the same recipient as to; the task
// must exist, a named attempt must exist, and an Artisan recipient's attempt must
// be running. Resending the same envelope returns the original receipt and stores
// nothing new; reusing its id for a different envelope fails with
// store.ErrMutationConflict. A receipt means stored, not read.
//
// List returns at most limit (1 to store.MaxInboxLimit) unacknowledged messages
// addressed to to with a sequence greater than after, in send order, and whether
// more remain. Read returns one message, acknowledged or not, without
// acknowledging it. Ack records that to consumed a message addressed to it and
// removes it from the inbox; retrying returns the original receipt.
//
// No method records a task result, answers a decision, approves anything or
// controls an attempt. Errors match ErrInvalid, ErrNewerSchema or the store's
// ErrInvalid, ErrNotFound and ErrMutationConflict under errors.Is.
// Implementations are safe for concurrent use.
type Mailbox interface {
	Send(to store.Recipient, e Envelope) (store.Receipt, error)
	List(to store.Recipient, after uint64, limit int) (Page, error)
	Read(id string) (Stored, error)
	Ack(to store.Recipient, id string) (store.Receipt, error)
}
