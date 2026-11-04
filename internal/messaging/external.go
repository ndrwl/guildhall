package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// DirectorSchemaPrefix and DirectorSchemaVersion form an external message's envelope "schema" value,
// "guildhall.director-message/1" (docs/messaging.md#external-messages). The local "guildhall.message/1" is unchanged.
const (
	DirectorSchemaPrefix  = "guildhall.director-message/"
	DirectorSchemaVersion = 1
)

// SenderSteward is the wake sender type of a Steward message, which `messages receive` records.
const SenderSteward = "steward"

// SenderPeer is the wake sender type of a peer Director's message, which `messages receive --from` records; the
// message's sender is the peer's name.
const SenderPeer = "director"

// DirectorEnvelope is the versioned wire form of one external message, such as a Steward message. It names no task
// of the receiving store, which may have none for it.
type DirectorEnvelope struct {
	Schema string `json:"schema"`
	// ID is chosen by the sender and unique among its messages; resending the same envelope is safe.
	ID string `json:"id"`
	// From is the sender's name, which must match the identity the receiving command records; To the receiving
	// Director's name.
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
	// Kind is what a steward-report reports; other types have none.
	Kind string `json:"kind,omitempty"`
	// Subject is the one-line header a summary shows before the message is read.
	Subject string       `json:"subject"`
	Text    string       `json:"text"`
	About   *store.About `json:"about,omitempty"`
	// InReplyTo is an answer's question: the id of the question the answer's receiver sent. Only an answer has one.
	InReplyTo string              `json:"in_reply_to,omitempty"`
	Artifacts []store.ArtifactRef `json:"artifacts,omitempty"`
}

// DecodeDirector reads one external message envelope with Decode's strict rules: unknown, duplicate or case-variant
// fields, trailing data, more than MaxEnvelopeBytes and unknown or newer schemas are refused, a leading UTF-8 byte
// order mark and CRLF are accepted, and UTF-16 is refused with a hint.
func DecodeDirector(r io.Reader) (DirectorEnvelope, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxEnvelopeBytes+1))
	if err != nil {
		return DirectorEnvelope{}, fmt.Errorf("messaging: read envelope: %w", err)
	}
	var e DirectorEnvelope
	err = strictjson.Decode(data, &e, strictjson.Options{Schema: DirectorSchemaPrefix, Version: DirectorSchemaVersion, MaxBytes: MaxEnvelopeBytes})
	if errors.Is(err, ErrNewerSchema) {
		return DirectorEnvelope{}, fmt.Errorf("messaging: %w", err)
	}
	if err != nil {
		return DirectorEnvelope{}, invalid("envelope: %v", err)
	}
	return e, nil
}

// ExternalMessage converts an envelope that sender sent to the receiving Director named to into the store record.
// It refuses an unsupported schema, an envelope whose from is not sender's identity and one addressed to another
// Director; the store checks the rest, such as which types and which in_reply_to each sender may send.
func ExternalMessage(sender, to string, e DirectorEnvelope) (store.ExternalMessage, error) {
	switch {
	case e.Schema != DirectorSchemaPrefix+strconv.Itoa(DirectorSchemaVersion):
		return store.ExternalMessage{}, invalid("schema %q is not %s%d", e.Schema, DirectorSchemaPrefix, DirectorSchemaVersion)
	case e.From != sender:
		return store.ExternalMessage{}, invalid("message %q is from %q, but was received from %s", e.ID, e.From, sender)
	case to != "" && e.To != to:
		return store.ExternalMessage{}, invalid("message %q is addressed to %q, not this Director, %q", e.ID, e.To, to)
	}
	return store.ExternalMessage{Sender: sender, ID: e.ID, From: e.From, To: e.To, Type: e.Type, Kind: e.Kind, Subject: e.Subject,
		Text: e.Text, About: e.About, InReplyTo: e.InReplyTo, Artifacts: e.Artifacts}, nil
}

// ExternalWakeStore is the store as WakeExternal reads and writes it.
type ExternalWakeStore interface {
	WakeStore
	ExternalWakeFor(sender, id string) (store.Wake, error)
	External(sender, id string) (store.ExternalMessage, error)
}

// WakeExternal is Wake for an external message: after the message sender sent with id is stored, it wakes the
// Director with the same counts-only line, through the same coalescing, and records the decision as the message's
// wake, keyed by sender and id. A message already woken for, such as a retried receive, returns its recorded wake and
// wakes nothing new.
func WakeExternal(ctx context.Context, st ExternalWakeStore, pane DirectorPane, sender, id, senderType string) (store.Wake, error) {
	if w, err := st.ExternalWakeFor(sender, id); !errors.Is(err, store.ErrNotFound) {
		return w, err
	}
	if _, err := st.External(sender, id); err != nil {
		return store.Wake{}, err
	}
	w := store.Wake{MessageID: id, ExternalSender: sender, SenderType: senderType, Decision: store.WakeQueued}
	var dues []store.HandoffDue
	var err error
	if w.Reason, dues, err = typeWake(ctx, st, pane, 0); err != nil {
		return store.Wake{}, err
	}
	if w.Reason == "" {
		w.Decision = store.WakeWoke
	}
	// A crossing names the message id only; its wake record names the sender too.
	if err := recordDues(st, dues, id); err != nil {
		return store.Wake{}, err
	}
	if _, err := st.RecordWake(w); err != nil && !errors.Is(err, store.ErrMutationConflict) {
		return store.Wake{}, err
	}
	return st.ExternalWakeFor(sender, id)
}
