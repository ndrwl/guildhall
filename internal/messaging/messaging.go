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
// See docs/messaging.md for the envelope and the command line.
package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
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
// A leading UTF-8 byte order mark, as some Windows tools write, is ignored.
func Decode(r io.Reader) (Envelope, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxEnvelopeBytes+1))
	if err != nil {
		return Envelope{}, fmt.Errorf("messaging: read envelope: %w", err)
	}
	if len(data) > MaxEnvelopeBytes {
		return Envelope{}, invalid("envelope is larger than %d bytes", MaxEnvelopeBytes)
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if err := checkKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	num, ok := strings.CutPrefix(head.Schema, SchemaPrefix)
	version, err := strconv.Atoi(num)
	if !ok || err != nil || version < 1 {
		return Envelope{}, invalid("schema %q is not %s<version>", head.Schema, SchemaPrefix)
	}
	if version > SchemaVersion {
		return Envelope{}, fmt.Errorf("messaging: %w: %q (supported: %s%d)", ErrNewerSchema, head.Schema, SchemaPrefix, SchemaVersion)
	}
	var e Envelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Envelope{}, invalid("unexpected data after the envelope")
	}
	return e, nil
}

// checkKeys refuses an object holding the same key twice, compared without case
// because encoding/json matches field names that way; the value would otherwise
// silently be the last one given.
func checkKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name := strings.ToLower(key.(string))
			if seen[name] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[name] = true
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		for dec.More() {
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
}

// ParseRecipient reads "director" or "artisan:<repository>/<task>/<step>/<attempt>".
func ParseRecipient(s string) (store.Recipient, error) {
	if s == string(store.RecipientDirector) {
		return store.Recipient{Role: store.RecipientDirector}, nil
	}
	rest, ok := strings.CutPrefix(s, string(store.RecipientArtisan)+":")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 4 {
		return store.Recipient{}, invalid(`recipient %q must be "director" or "artisan:<repository>/<task>/<step>/<attempt>"`, s)
	}
	for _, id := range parts[:3] {
		if !workflow.ValidID(id) {
			return store.Recipient{}, invalid("recipient %q: %q is not a valid id", s, id)
		}
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[3] || n > 1<<31-1 {
		return store.Recipient{}, invalid("recipient %q: attempt %q must be a positive number", s, parts[3])
	}
	return store.Recipient{Role: store.RecipientArtisan, Repository: parts[0], TaskID: parts[1], Step: parts[2], Attempt: n}, nil
}

// FormatRecipient is the inverse of ParseRecipient.
func FormatRecipient(r store.Recipient) string {
	if r.Role == store.RecipientArtisan {
		return fmt.Sprintf("%s:%s/%s/%s/%d", r.Role, r.Repository, r.TaskID, r.Step, r.Attempt)
	}
	return string(r.Role)
}

// Send stores an envelope addressed to to and returns its receipt, then calls
// notify, if any, with the stored message. The envelope must name the same
// recipient as to, so a message cannot land in an inbox its sender did not name.
// Resending an envelope returns the original receipt; reusing its id for a
// different envelope returns store.ErrMutationConflict.
func Send(s *store.Store, to store.Recipient, e Envelope, notify Notifier) (store.Receipt, error) {
	if e.Schema != SchemaPrefix+strconv.Itoa(SchemaVersion) {
		return store.Receipt{}, invalid("schema %q is not %s%d", e.Schema, SchemaPrefix, SchemaVersion)
	}
	m := store.Message{ID: e.ID, Recipient: e.Recipient, Repository: e.Repository, TaskID: e.Task, Step: e.Step, Attempt: e.Attempt,
		Type: e.Type, Text: e.Text, Artifacts: e.Artifacts}
	if m.To() != to {
		return store.Receipt{}, invalid("message %q is addressed to %s, not %s", e.ID, FormatRecipient(m.To()), FormatRecipient(to))
	}
	r, err := s.SendMessage(m)
	if err != nil {
		return store.Receipt{}, fmt.Errorf("messaging: %w", err)
	}
	if notify != nil {
		m.Sequence = r.Sequence
		notify(m, r)
	}
	return r, nil
}

// List returns at most limit unacknowledged messages addressed to to, after the given sequence, in send order.
func List(s *store.Store, to store.Recipient, after uint64, limit int) (Page, error) {
	msgs, more, err := s.Inbox(to, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("messaging: %w", err)
	}
	page := Page{Messages: make([]Stored, len(msgs)), More: more}
	for i, m := range msgs {
		page.Messages[i] = stored(m)
	}
	return page, nil
}

// Read returns one message, acknowledged or not. Reading does not acknowledge it.
func Read(s *store.Store, id string) (Stored, error) {
	m, err := s.Message(id)
	if err != nil {
		return Stored{}, fmt.Errorf("messaging: %w", err)
	}
	return stored(m), nil
}

// Ack records that to consumed a message, removing it from the inbox.
// Acknowledgement is not a task result, an approval or an answer to a decision.
func Ack(s *store.Store, to store.Recipient, id string) (store.Receipt, error) {
	r, err := s.AckMessage(to, id)
	if err != nil {
		return store.Receipt{}, fmt.Errorf("messaging: %w", err)
	}
	return r, nil
}

func stored(m store.Message) Stored {
	return Stored{
		Envelope: Envelope{Schema: SchemaPrefix + strconv.Itoa(SchemaVersion), ID: m.ID, Recipient: m.Recipient, Repository: m.Repository, Task: m.TaskID,
			Step: m.Step, Attempt: m.Attempt, Type: m.Type, Text: m.Text, Artifacts: m.Artifacts},
		Sequence:     m.Sequence,
		Acknowledged: m.Acknowledged,
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("messaging: %w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
