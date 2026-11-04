package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// External messages are messages from outside this copy's own processes, such as a Steward message
// (docs/messaging.md#external-messages). They are kept in the store's director bucket, outside any repository, keyed
// by their sender and id, so one needs no task in this store. Like every message they inform only: storing, reading
// or acknowledging one never changes another record.
var (
	// bucketExternal holds every external message by "<sender>/<id>".
	bucketExternal = []byte("external-messages")
	// bucketExternalInbox indexes the unacknowledged ones by zero-padded sequence, to "<sender>/<id>".
	bucketExternalInbox = []byte("external-inbox")
	// bucketInboxStates holds each external sender's inbox state by sender.
	bucketInboxStates = []byte("inbox-states")
	// bucketOutbox holds a sender's outbox entries by message id.
	bucketOutbox = []byte("outbox")
	// bucketCopyReach holds, by "<machine>/<director>", when a Steward first found an enrolled copy unreachable.
	bucketCopyReach = []byte("copy-reach")
	// bucketCopyConditions holds, by "<machine>/<director> <kind>", each Director condition a Steward has found on an
	// enrolled copy and not yet seen clear.
	bucketCopyConditions = []byte("copy-conditions")
)

// Operations recorded on external message, inbox state and outbox receipts.
const (
	opReceiveExternal = "receive-external-message"
	opAckExternal     = "ack-external-message"
	opInboxState      = "inbox-state"
	opRecordOutbox    = "record-outbox"
	opOutboxAttempt   = "outbox-attempt"
	opCopyReach       = "copy-reach"
	opCopyCondition   = "copy-condition"
)

// SenderSteward is the sender of a Steward message: one `messages receive` took in from the drop file a Steward copied
// to the copy (docs/messaging.md#steward-messages). It is recorded only by that command, never by `messages send`.
const SenderSteward = "steward"

// ValidateExternalSender refuses a sender that is neither SenderSteward nor a peer name. A peer is another Director,
// named as the receiving copy's [[peer]] config lists it (docs/messaging.md#director-to-director-messages): a Director
// name, a lowercase id, that is never one of the reserved sender words, so it cannot pass for the Steward, the
// Guildmaster or a local sender. The receiving command checks a peer against its config; the store checks the form.
func ValidateExternalSender(sender string) error {
	if sender == SenderSteward {
		return nil
	}
	if !workflow.ValidID(sender) || slices.Contains(reservedSenderNames, sender) {
		return invalid("external sender %q must be %s or a peer Director's name, a lowercase id that is not one of %s",
			sender, SenderSteward, strings.Join(reservedSenderNames, ", "))
	}
	return nil
}

// reservedSenderNames are the words no peer may be named: the message senders and the wake sender types.
var reservedSenderNames = []string{SenderGuildhall, SenderArtisan, SenderDirector, SenderOtherDirector, SenderGuildmaster,
	SenderSteward, SenderUnknown, "local", "page"}

// ReservedSenderNames returns the names no peer may have.
func ReservedSenderNames() []string { return slices.Clone(reservedSenderNames) }

// MessageStewardReport is the type of a Steward message: a report of an issue the Steward found, with a kind.
const MessageStewardReport = "steward-report"

// The types of a Director message, one a peer sends (docs/messaging.md#message-types). Only an answer has in_reply_to.
const (
	// MessageHandoffOffer offers the receiver work, with a handoff bundle named in its artifacts.
	MessageHandoffOffer = "handoff-offer"
	// MessageDependencyNotice tells the receiver the sender's work depends on, or changes, something it uses.
	MessageDependencyNotice = "dependency-notice"
	// MessageAnswer replies to one question the receiver sent, named by in_reply_to. A peer's question is
	// MessageQuestion, the local question type.
	MessageAnswer = "answer"
)

// DirectorMessageTypes are the types a peer's message may have; further types are added by a decision.
var DirectorMessageTypes = []string{MessageHandoffOffer, MessageDependencyNotice, MessageQuestion, MessageAnswer}

// The kinds of a steward-report (docs/messaging.md#steward-messages).
const (
	// ReportRolloutFailed: the newest rollout to the copy failed, from the Steward's rollout receipts.
	ReportRolloutFailed = "rollout-failed"
	// ReportUnreachableRecovered: the copy was unreachable for at least the configured span and is reachable again.
	ReportUnreachableRecovered = "unreachable-recovered"
	// ReportDirectorStuck: the Director took no turn for the configured span while messages waited for it.
	ReportDirectorStuck = "director-stuck"
	// ReportDirectorUnhealthy: the session's supervisor is gone while its session is not stopped, or the session
	// has stayed recovering for the configured span.
	ReportDirectorUnhealthy = "director-unhealthy"
	// ReportReleaseBehind: the session runs a release older than the copy's pin, and has for the configured span.
	ReportReleaseBehind = "release-behind"
)

// ReportKinds lists the kinds a steward-report may have.
var ReportKinds = []string{ReportRolloutFailed, ReportUnreachableRecovered, ReportDirectorStuck, ReportDirectorUnhealthy, ReportReleaseBehind}

// Bounds of an external message's fields beyond a local message's.
const (
	// MaxSubjectBytes bounds the one-line subject a summary shows.
	MaxSubjectBytes = 200
	// MaxExternalNameBytes bounds the from and to names.
	MaxExternalNameBytes = 100
	// DefaultMailboxMax is how many unacknowledged external messages a copy holds before it refuses a new one.
	DefaultMailboxMax = 200
)

var (
	// ErrInboxClosed reports a message refused because its sender's inbox is closed.
	ErrInboxClosed = errors.New("closed")
	// ErrMailboxFull reports a message refused because the copy holds mailbox_max unacknowledged external messages.
	ErrMailboxFull = errors.New("mailbox full")
)

// About names the sender's repository and task an external message concerns, which the receiver does not look up.
type About struct {
	Repository string `json:"repository"`
	Task       string `json:"task"`
}

// ExternalMessage is one external message as the receiving store records it.
type ExternalMessage struct {
	// Sender is who sent it, one of ExternalSenders, set by the receiving command and never by the envelope.
	Sender string `json:"sender"`
	// ID is chosen by the sender and unique among its messages; with Sender it is the message's identity.
	ID string `json:"id"`
	// From and To are the envelope's names: the sender's own, which must match Sender's identity, and the receiving
	// Director's.
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
	// Kind is what a steward-report reports, one of ReportKinds; other types have none.
	Kind    string `json:"kind,omitempty"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	About   *About `json:"about,omitempty"`
	// InReplyTo is an answer's question: the id of the question the receiver sent the answer's sender. Only an
	// answer has one, and it must.
	InReplyTo string        `json:"in_reply_to,omitempty"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
	// Sequence is set by the store to the receive receipt's sequence, and ReceivedAt to its time.
	Sequence   uint64    `json:"sequence"`
	ReceivedAt time.Time `json:"received_at"`
	// Acknowledged is set by AckExternal: the Director dealt with it, not that anything it mentions was done.
	Acknowledged bool `json:"acknowledged"`
}

func externalKey(sender, id string) []byte { return []byte(sender + "/" + id) }

// ExternalMutationID is the mutation an external message's receipt records, "dmessage:<sender>:<id>".
func ExternalMutationID(sender, id string) string { return "dmessage:" + sender + ":" + id }

func externalAckMutationID(sender, id string) string { return "dmessage-ack:" + sender + ":" + id }

// ValidateExternalMessage checks a new external message's fields without reading the store.
func ValidateExternalMessage(m ExternalMessage) error {
	if err := ValidateExternalSender(m.Sender); err != nil {
		return err
	}
	if err := ValidateMessageID(m.ID); err != nil {
		return err
	}
	for name, v := range map[string]string{"from": m.From, "to": m.To} {
		if err := validateExternalName(name, v); err != nil {
			return err
		}
	}
	if m.From != m.Sender {
		return invalid("a message from %s is from %q, not %q", m.Sender, m.Sender, m.From)
	}
	if err := validateIDs(m.Type); err != nil {
		return err
	}
	switch {
	case m.Sender == SenderSteward && m.Type != MessageStewardReport:
		return invalid("a steward message is a %s, not %q", MessageStewardReport, m.Type)
	case m.Sender != SenderSteward && !slices.Contains(DirectorMessageTypes, m.Type):
		return invalid("a Director message's type must be one of %s, not %q", strings.Join(DirectorMessageTypes, ", "), m.Type)
	case m.Type == MessageStewardReport && !slices.Contains(ReportKinds, m.Kind):
		return invalid("a %s kind must be one of %s, not %q", MessageStewardReport, strings.Join(ReportKinds, ", "), m.Kind)
	case m.Type != MessageStewardReport && m.Kind != "":
		return invalid("only a %s has a kind", MessageStewardReport)
	case m.Type == MessageAnswer && m.InReplyTo == "":
		return invalid("an %s needs in_reply_to, the id of the question it answers", MessageAnswer)
	case m.Type != MessageAnswer && m.InReplyTo != "":
		return invalid("only an %s has in_reply_to", MessageAnswer)
	}
	if m.InReplyTo != "" {
		if err := ValidateMessageID(m.InReplyTo); err != nil {
			return err
		}
	}
	if err := validateText("subject", m.Subject, MaxSubjectBytes); err != nil {
		return err
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return invalid("subject must be one line")
	}
	if err := validateText("message text", m.Text, MaxMessageTextBytes); err != nil {
		return err
	}
	if m.About != nil {
		if err := validateIDs(m.About.Repository, m.About.Task); err != nil {
			return err
		}
	}
	if len(m.Artifacts) > MaxMessageArtifacts {
		return invalid("a message has %d artifacts; the limit is %d", len(m.Artifacts), MaxMessageArtifacts)
	}
	for _, a := range m.Artifacts {
		if !validDocumentPath(a.Path) {
			return invalid("artifact path %q must be a clean relative slash-separated path", a.Path)
		}
		if _, err := digest.ParseHex(a.SHA256); err != nil {
			return invalid("artifact sha256 %q must be 64 lowercase hex characters", a.SHA256)
		}
	}
	if m.Sequence != 0 || m.Acknowledged || !m.ReceivedAt.IsZero() {
		return invalid("a new message has no sequence or receipt time and is not acknowledged")
	}
	return nil
}

// validateExternalName checks a from or to name: the message id form, which a copy's name and "steward" both fit.
func validateExternalName(name, v string) error {
	if !messageIDPattern.MatchString(v) {
		return invalid("%s %q must be 1 to %d letters, digits, '.', '_' and '-'", name, v, MaxExternalNameBytes)
	}
	return nil
}

// ReceiveExternal stores an external message and its receipt in one transaction and adds it to the Director's
// external inbox. It resolves the mutation ExternalMutationID first: an identical message received before returns its
// original receipt and stores nothing new, even when the sender's inbox is now closed or the mailbox full, and a
// different message under that identity returns ErrMutationConflict. Only a new message is then admitted: refused
// with ErrInboxClosed while its sender's inbox is closed, and with ErrMailboxFull when mailboxMax unacknowledged
// external messages are held; either refusal stores nothing. The receipt means stored, not read.
func (s *Store) ReceiveExternal(m ExternalMessage, mailboxMax int) (Receipt, error) {
	if err := ValidateExternalMessage(m); err != nil {
		return Receipt{}, err
	}
	if mailboxMax < 1 {
		return Receipt{}, invalid("mailbox_max %d must be positive", mailboxMax)
	}
	return s.mutate(ExternalMutationID(m.Sender, m.ID), opReceiveExternal, m, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = "external-message/" + m.Sender + "/" + m.ID
		if st, err := inboxState(tx, m.Sender); err != nil {
			return err
		} else if st.State == InboxClosed {
			return fmt.Errorf("%w: the inbox is closed to %s: %s", ErrInboxClosed, m.Sender, st.Reason)
		}
		inbox, err := createDirectorBucket(tx, bucketExternalInbox)
		if err != nil {
			return err
		}
		// The count stops at the cap, so admission's work is bounded by mailbox_max, not by the store.
		n, c := 0, inbox.Cursor()
		for k, _ := c.First(); k != nil && n < mailboxMax; k, _ = c.Next() {
			n++
		}
		if n >= mailboxMax {
			return fmt.Errorf("%w: %d unacknowledged external messages, mailbox_max %d", ErrMailboxFull, n, mailboxMax)
		}
		msgs, err := createDirectorBucket(tx, bucketExternal)
		if err != nil {
			return err
		}
		if msgs.Get(externalKey(m.Sender, m.ID)) != nil {
			return fmt.Errorf("%w: external message %s/%s", ErrExists, m.Sender, m.ID)
		}
		m.Sequence, m.ReceivedAt = r.Sequence, s.now().UTC()
		r.CommittedAt = m.ReceivedAt
		if err := inbox.Put(wakeKey(m.Sequence), externalKey(m.Sender, m.ID)); err != nil {
			return err
		}
		return putJSON(msgs, externalKey(m.Sender, m.ID), m)
	})
}

// AckExternal records that the Director dealt with an external message and removes it from the external inbox, with
// the mutation "dmessage-ack:<sender>:<id>"; retrying returns the original receipt. It changes nothing but the message.
func (s *Store) AckExternal(sender, id string) (Receipt, error) {
	if err := validateExternalRef(sender, id); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Sender string `json:"sender"`
		ID     string `json:"id"`
	}{sender, id}
	return s.mutate(externalAckMutationID(sender, id), opAckExternal, req, func(tx *bolt.Tx, r *Receipt) error {
		m, err := getExternal(tx, sender, id)
		if err != nil {
			return err
		}
		r.Record = "external-message/" + sender + "/" + id
		if m.Acknowledged {
			return invalid("external message %s/%s is already acknowledged", sender, id)
		}
		m.Acknowledged = true
		if err := directorBucket(tx, bucketExternalInbox).Delete(wakeKey(m.Sequence)); err != nil {
			return err
		}
		return putJSON(directorBucket(tx, bucketExternal), externalKey(sender, id), m)
	})
}

// External returns one external message, acknowledged or not.
func (s *Store) External(sender, id string) (ExternalMessage, error) {
	if err := validateExternalRef(sender, id); err != nil {
		return ExternalMessage{}, err
	}
	var m ExternalMessage
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		m, err = getExternal(tx, sender, id)
		return err
	})
	if err != nil {
		return ExternalMessage{}, fmt.Errorf("store: %w", err)
	}
	return m, nil
}

// ExternalInbox returns at most limit unacknowledged external messages, from sender or from every sender when it is
// empty, with a sequence greater than after, in receive order, and whether more remain.
func (s *Store) ExternalInbox(sender string, after uint64, limit int) ([]ExternalMessage, bool, error) {
	if sender != "" {
		if err := ValidateExternalSender(sender); err != nil {
			return nil, false, err
		}
	}
	if limit < 1 || limit > MaxInboxLimit {
		return nil, false, invalid("inbox limit %d must be between 1 and %d", limit, MaxInboxLimit)
	}
	var out []ExternalMessage
	more := false
	err := s.view(func(tx *bolt.Tx) error {
		inbox := directorBucket(tx, bucketExternalInbox)
		if inbox == nil || after == math.MaxUint64 {
			return nil
		}
		c := inbox.Cursor()
		for k, v := c.Seek(wakeKey(after + 1)); k != nil; k, v = c.Next() {
			if sender != "" && !bytes.HasPrefix(v, []byte(sender+"/")) {
				continue
			}
			if len(out) == limit {
				more = true
				return nil
			}
			var m ExternalMessage
			if err := getJSON(directorBucket(tx, bucketExternal), v, &m); err != nil {
				return fmt.Errorf("external inbox message %q: %w", v, err)
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: external inbox: %w", err)
	}
	return out, more, nil
}

// externalUnread adds the unacknowledged external messages to counts, keyed UnreadFrom(type, sender) with the type
// one of MessageTypes or "other", so no key carries free text.
func externalUnread(tx *bolt.Tx, counts map[string]int) error {
	inbox := directorBucket(tx, bucketExternalInbox)
	if inbox == nil {
		return nil
	}
	return inbox.ForEach(func(_, v []byte) error {
		var m ExternalMessage
		if err := getJSON(directorBucket(tx, bucketExternal), v, &m); err != nil {
			return fmt.Errorf("external inbox message %q: %w", v, err)
		}
		t := m.Type
		if _, ok := MessageTypes[t]; !ok {
			t = "other"
		}
		sender := m.Sender
		if ValidateExternalSender(sender) != nil {
			sender = SenderUnknown
		}
		counts[UnreadFrom(t, sender)]++
		return nil
	})
}

func getExternal(tx *bolt.Tx, sender, id string) (ExternalMessage, error) {
	var m ExternalMessage
	if err := getJSON(directorBucket(tx, bucketExternal), externalKey(sender, id), &m); err != nil {
		return m, fmt.Errorf("%w: external message %s/%s", ErrNotFound, sender, id)
	}
	return m, nil
}

func validateExternalRef(sender, id string) error {
	if err := ValidateExternalSender(sender); err != nil {
		return err
	}
	return ValidateMessageID(id)
}

// Inbox states of one external sender.
const (
	InboxOpen   = "open"
	InboxClosed = "closed"
)

// InboxState is whether the Director's inbox takes new messages from one external sender
// (docs/messaging.md#closing-the-inbox-to-a-sender). Every sender's inbox is open until the Director closes it; only
// the Guildmaster's answer reopen to its alert lets it open again.
type InboxState struct {
	Sender string `json:"sender"`
	State  string `json:"state"`
	// Reason is the Director's reason for closing it, kept while it is closed.
	Reason string `json:"reason,omitempty"`
	// Closes counts how often it was closed; the n-th close's alert decision is InboxAlertID(sender, n).
	Closes int `json:"closes"`
	// Decision is the alert decision of the newest close.
	Decision string `json:"decision,omitempty"`
	// Sequence and Time are set by the store from the change's receipt.
	Sequence uint64    `json:"sequence,omitempty"`
	Time     time.Time `json:"time,omitzero"`
}

// InboxAlertID is the id of the alert decision the n-th close of sender's inbox opens: "inbox-<sender>-<n>".
func InboxAlertID(sender string, n int) string { return fmt.Sprintf("inbox-%s-%d", sender, n) }

// InboxStateOf returns sender's inbox state: open, with no closes, when it was never closed.
func (s *Store) InboxStateOf(sender string) (InboxState, error) {
	if err := ValidateExternalSender(sender); err != nil {
		return InboxState{}, err
	}
	var st InboxState
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		st, err = inboxState(tx, sender)
		return err
	})
	return st, err
}

func inboxState(tx *bolt.Tx, sender string) (InboxState, error) {
	b := directorBucket(tx, bucketInboxStates)
	if b == nil || b.Get([]byte(sender)) == nil {
		return InboxState{Sender: sender, State: InboxOpen}, nil
	}
	var st InboxState
	err := getJSON(b, []byte(sender), &st)
	return st, err
}

// CloseInbox closes sender's inbox with the Director's reason, as its n-th close, naming the alert decision
// InboxAlertID(sender, n) the caller opened for it, with the mutation "inbox-close:<sender>:<n>". n must be one more
// than the closes recorded and the inbox open; replaying the same close returns its original receipt.
func (s *Store) CloseInbox(sender, reason string, n int) (Receipt, error) {
	if err := ValidateExternalSender(sender); err != nil {
		return Receipt{}, err
	}
	if err := validateText("close reason", reason, MaxSubjectBytes); err != nil {
		return Receipt{}, err
	}
	if strings.ContainsAny(reason, "\r\n") {
		return Receipt{}, invalid("close reason must be one line")
	}
	req := struct {
		Sender string `json:"sender"`
		Reason string `json:"reason"`
		N      int    `json:"n"`
	}{sender, reason, n}
	return s.mutate(fmt.Sprintf("inbox-close:%s:%d", sender, n), opInboxState, req, func(tx *bolt.Tx, r *Receipt) error {
		st, err := inboxState(tx, sender)
		switch {
		case err != nil:
			return err
		case st.State == InboxClosed:
			return invalid("the inbox is already closed to %s: %s", sender, st.Reason)
		case n != st.Closes+1:
			return invalid("close %d of the inbox to %s is not the next one, %d", n, sender, st.Closes+1)
		}
		r.Record = "inbox-state/" + sender
		st = InboxState{Sender: sender, State: InboxClosed, Reason: reason, Closes: n, Decision: InboxAlertID(sender, n),
			Sequence: r.Sequence, Time: s.now().UTC()}
		r.CommittedAt = st.Time
		return putInboxState(tx, st)
	})
}

// ReopenInbox opens sender's inbox after its n-th close, with the mutation "inbox-open:<sender>:<n>". The caller
// checks first that the Guildmaster answered that close's alert reopen. Replaying it returns its original receipt.
func (s *Store) ReopenInbox(sender string, n int) (Receipt, error) {
	if err := ValidateExternalSender(sender); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Sender string `json:"sender"`
		N      int    `json:"n"`
	}{sender, n}
	return s.mutate(fmt.Sprintf("inbox-open:%s:%d", sender, n), opInboxState, req, func(tx *bolt.Tx, r *Receipt) error {
		st, err := inboxState(tx, sender)
		switch {
		case err != nil:
			return err
		case st.State != InboxClosed || st.Closes != n:
			return invalid("the inbox to %s is not closed by close %d", sender, n)
		}
		r.Record = "inbox-state/" + sender
		st = InboxState{Sender: sender, State: InboxOpen, Closes: n, Decision: st.Decision, Sequence: r.Sequence, Time: s.now().UTC()}
		r.CommittedAt = st.Time
		return putInboxState(tx, st)
	})
}

func putInboxState(tx *bolt.Tx, st InboxState) error {
	b, err := createDirectorBucket(tx, bucketInboxStates)
	if err != nil {
		return err
	}
	return putJSON(b, []byte(st.Sender), st)
}

// Outbox states.
const (
	OutboxPending   = "pending"
	OutboxDelivered = "delivered"
	OutboxRefused   = "refused"
)

// OutboxEntry is one message in a sender's outbox (docs/messaging.md#the-outbox): recorded pending before its first
// delivery, then delivered with the receiver's receipt or refused with the receiver's reason, each final. A pending
// entry is retried; a refused one never is.
type OutboxEntry struct {
	// ID is the message's id, To where it goes, such as "<machine>/<director>", and Envelope the exact bytes sent.
	ID       string          `json:"id"`
	To       string          `json:"to"`
	Envelope json.RawMessage `json:"envelope"`
	State    string          `json:"state"`
	// Receipt is the receiver's receipt of a delivered entry, as it printed it.
	Receipt json.RawMessage `json:"receipt,omitempty"`
	// Reason is the receiver's refusal of a refused entry.
	Reason string `json:"reason,omitempty"`
	// Attempts counts the deliveries tried, and LastError is why the newest that failed did, kept while pending.
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaxOutboxEnvelopeBytes bounds an outbox entry's envelope.
const MaxOutboxEnvelopeBytes = 128 << 10

// RecordOutbox records a new pending outbox entry with the mutation "outbox:<id>"; recording the same id, destination
// and envelope again returns its original receipt and changes nothing, so a report is queued once however often it is
// found, and anything else under that id returns ErrMutationConflict.
func (s *Store) RecordOutbox(id, to string, envelope []byte) (Receipt, error) {
	if err := ValidateMessageID(id); err != nil {
		return Receipt{}, err
	}
	if err := validateText("outbox destination", to, 256); err != nil {
		return Receipt{}, err
	}
	if len(envelope) == 0 || len(envelope) > MaxOutboxEnvelopeBytes || !json.Valid(envelope) {
		return Receipt{}, invalid("an outbox envelope is one JSON document of 1 to %d bytes", MaxOutboxEnvelopeBytes)
	}
	req := struct {
		ID       string `json:"id"`
		To       string `json:"to"`
		Envelope string `json:"envelope"`
	}{id, to, string(digest.Of(envelope))}
	return s.mutate("outbox:"+id, opRecordOutbox, req, func(tx *bolt.Tx, r *Receipt) error {
		b, err := createDirectorBucket(tx, bucketOutbox)
		if err != nil {
			return err
		}
		if b.Get([]byte(id)) != nil {
			return fmt.Errorf("%w: outbox entry %q", ErrExists, id)
		}
		r.Record = "outbox/" + id
		now := s.now().UTC()
		r.CommittedAt = now
		return putJSON(b, []byte(id), OutboxEntry{ID: id, To: to, Envelope: envelope, State: OutboxPending, CreatedAt: now, UpdatedAt: now})
	})
}

// OutboxAttempt records the outcome of delivery attempt number n of a pending entry, with the mutation
// "outbox-attempt:<id>:<n>": a receipt makes it delivered, a reason refused, and neither, with failure set, keeps it
// pending with that error. n must be one more than the attempts recorded; replaying it returns its original receipt.
func (s *Store) OutboxAttempt(id string, n int, receipt []byte, reason, failure string) (Receipt, error) {
	if err := ValidateMessageID(id); err != nil {
		return Receipt{}, err
	}
	switch {
	case len(receipt) > 0 && (reason != "" || failure != ""), reason != "" && failure != "":
		return Receipt{}, invalid("an outbox attempt is delivered, refused or failed, one of them")
	case len(receipt) == 0 && reason == "" && failure == "":
		return Receipt{}, invalid("an outbox attempt needs a receipt, a refusal or a failure")
	case len(receipt) > MaxOutboxEnvelopeBytes || len(receipt) > 0 && !json.Valid(receipt):
		return Receipt{}, invalid("an outbox receipt is one JSON document of at most %d bytes", MaxOutboxEnvelopeBytes)
	}
	for name, v := range map[string]string{"refusal": reason, "failure": failure} {
		if v != "" {
			if err := validateText("outbox "+name, v, MaxMessageTextBytes); err != nil {
				return Receipt{}, err
			}
		}
	}
	req := struct {
		ID      string `json:"id"`
		N       int    `json:"n"`
		Receipt string `json:"receipt,omitempty"`
		Reason  string `json:"reason,omitempty"`
		Failure string `json:"failure,omitempty"`
	}{id, n, string(receipt), reason, failure}
	return s.mutate(fmt.Sprintf("outbox-attempt:%s:%d", id, n), opOutboxAttempt, req, func(tx *bolt.Tx, r *Receipt) error {
		b := directorBucket(tx, bucketOutbox)
		var e OutboxEntry
		if err := getJSON(b, []byte(id), &e); err != nil {
			return fmt.Errorf("%w: outbox entry %q", ErrNotFound, id)
		}
		switch {
		case e.State != OutboxPending:
			return invalid("outbox entry %q is %s, not pending", id, e.State)
		case n != e.Attempts+1:
			return invalid("attempt %d of outbox entry %q is not the next one, %d", n, id, e.Attempts+1)
		}
		r.Record = "outbox/" + id
		e.Attempts, e.UpdatedAt = n, s.now().UTC()
		r.CommittedAt = e.UpdatedAt
		switch {
		case len(receipt) > 0:
			e.State, e.Receipt, e.LastError = OutboxDelivered, receipt, ""
		case reason != "":
			e.State, e.Reason, e.LastError = OutboxRefused, reason, ""
		default:
			e.LastError = failure
		}
		return putJSON(b, []byte(id), e)
	})
}

// Outbox returns the outbox entries in id order: every one with all set, otherwise only those not delivered.
func (s *Store) Outbox(all bool) ([]OutboxEntry, error) {
	out := []OutboxEntry{}
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketOutbox)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var e OutboxEntry
			if err := decode(k, v, &e); err != nil {
				return err
			}
			if all || e.State != OutboxDelivered {
				out = append(out, e)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: outbox: %w", err)
	}
	return out, nil
}

// OutboxEntryOf returns the outbox entry with the message id id, whatever its state.
func (s *Store) OutboxEntryOf(id string) (OutboxEntry, error) {
	if err := ValidateMessageID(id); err != nil {
		return OutboxEntry{}, err
	}
	var e OutboxEntry
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketOutbox)
		if b == nil || b.Get([]byte(id)) == nil {
			return fmt.Errorf("%w: outbox entry %q", ErrNotFound, id)
		}
		return getJSON(b, []byte(id), &e)
	})
	if err != nil {
		return OutboxEntry{}, fmt.Errorf("store: %w", err)
	}
	return e, nil
}

// CopyReach is when a Steward first found an enrolled copy unreachable, kept until it reaches the copy again.
type CopyReach struct {
	Copy  string    `json:"copy"`
	Since time.Time `json:"since"`
}

// MarkCopyReach records whether the Steward reached the copy named name ("<machine>/<director>") at the time at: an
// unreachable copy keeps the first time it was found so, and a reachable one has its record removed. It returns the
// time the copy had been unreachable since, zero when it was not.
func (s *Store) MarkCopyReach(name string, reached bool, at time.Time) (time.Time, error) {
	if err := validateText("copy name", name, 256); err != nil {
		return time.Time{}, err
	}
	var since time.Time
	req := struct {
		Copy    string    `json:"copy"`
		Reached bool      `json:"reached"`
		At      time.Time `json:"at"`
	}{name, reached, at.UTC()}
	_, err := s.mutate("copy-reach:"+digest.Of([]byte(fmt.Sprintf("%s\n%t\n%s", name, reached, at.UTC().Format(time.RFC3339Nano)))).Hex()[:32],
		opCopyReach, req, func(tx *bolt.Tx, r *Receipt) error {
			b, err := createDirectorBucket(tx, bucketCopyReach)
			if err != nil {
				return err
			}
			var prior CopyReach
			if b.Get([]byte(name)) != nil {
				if err := getJSON(b, []byte(name), &prior); err != nil {
					return err
				}
				since = prior.Since
			}
			r.Record = "copy-reach/" + name
			if reached {
				return b.Delete([]byte(name))
			}
			if since.IsZero() {
				return putJSON(b, []byte(name), CopyReach{Copy: name, Since: at.UTC()})
			}
			return nil
		})
	return since, err
}

// CopyCondition is a Director condition a Steward found on an enrolled copy (docs/steward.md#director-health): its
// copy, its report kind, Mark, which names what holds, such as the release and the pin, and Since, when the Steward
// first found it with that mark. It is kept until the Steward sees it clear.
type CopyCondition struct {
	Copy  string    `json:"copy"`
	Kind  string    `json:"kind"`
	Mark  string    `json:"mark"`
	Since time.Time `json:"since"`
}

// MarkCopyCondition records whether condition kind holds on the copy named name ("<machine>/<director>") at the
// time at, with mark naming what holds: a condition found with the mark it already has keeps its Since, one found
// with another mark or not yet recorded starts again at at, and one that does not hold has its record removed. It
// returns the condition's Since, zero when it does not hold, so one condition is one report until it clears.
func (s *Store) MarkCopyCondition(name, kind, mark string, holds bool, at time.Time) (time.Time, error) {
	if err := validateText("copy name", name, 256); err != nil {
		return time.Time{}, err
	}
	if !slices.Contains(ReportKinds, kind) {
		return time.Time{}, invalid("a copy condition kind must be one of %s, not %q", strings.Join(ReportKinds, ", "), kind)
	}
	if holds {
		if err := validateText("copy condition mark", mark, 512); err != nil {
			return time.Time{}, err
		}
	}
	key := name + " " + kind
	var since time.Time
	req := struct {
		Copy  string    `json:"copy"`
		Kind  string    `json:"kind"`
		Mark  string    `json:"mark"`
		Holds bool      `json:"holds"`
		At    time.Time `json:"at"`
	}{name, kind, mark, holds, at.UTC()}
	id := digest.Of([]byte(fmt.Sprintf("%s\n%s\n%t\n%s", key, mark, holds, at.UTC().Format(time.RFC3339Nano)))).Hex()[:32]
	_, err := s.mutate("copy-condition:"+id, opCopyCondition, req, func(tx *bolt.Tx, r *Receipt) error {
		b, err := createDirectorBucket(tx, bucketCopyConditions)
		if err != nil {
			return err
		}
		r.Record = "copy-condition/" + key
		if !holds {
			return b.Delete([]byte(key))
		}
		if b.Get([]byte(key)) != nil {
			var prior CopyCondition
			if err := getJSON(b, []byte(key), &prior); err != nil {
				return err
			}
			if prior.Mark == mark {
				since = prior.Since
				return nil
			}
		}
		since = at.UTC()
		return putJSON(b, []byte(key), CopyCondition{Copy: name, Kind: kind, Mark: mark, Since: since})
	})
	if err != nil || !holds || !since.IsZero() {
		return since, err
	}
	// A replay of the same mutation runs nothing, so the condition's Since is read from its record.
	err = s.view(func(tx *bolt.Tx) error {
		var c CopyCondition
		if err := getJSON(directorBucket(tx, bucketCopyConditions), []byte(key), &c); err != nil {
			return err
		}
		since = c.Since
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("store: copy condition %s: %w", key, err)
	}
	return since, nil
}
