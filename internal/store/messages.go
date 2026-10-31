package store

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Message bounds; larger content belongs in a referenced artifact.
const (
	MaxMessageTextBytes = 16 << 10
	MaxMessageArtifacts = 32
	// MaxInboxLimit bounds the messages one Inbox call returns.
	MaxInboxLimit = 100
)

// Per-repository message buckets: messages holds every message by id, and
// inbox indexes the unacknowledged ones by recipient and sequence.
var (
	bucketMessages = []byte("messages")
	bucketInbox    = []byte("inbox")
)

// Operations recorded on message receipts.
const (
	opSendMessage = "send-message"
	opAckMessage  = "ack-message"
)

// RecipientRole is the kind of inbox a message is addressed to.
type RecipientRole string

const (
	// RecipientDirector is the inbox of the Director that owns the store.
	RecipientDirector RecipientRole = "director"
	// RecipientArtisan is the inbox of the Artisan running one stage attempt.
	RecipientArtisan RecipientRole = "artisan"
	// RecipientSteward is the inbox of the Steward session that owns the store (docs/steward.md#the-steward-session).
	// Like the Director's it is one per store, and it takes messages only from StewardSenders.
	RecipientSteward RecipientRole = "steward"
)

// SessionInbox reports whether r is the inbox of the session that owns the store, the Director's or the
// Steward's: one per store, spanning every repository, and the inbox a wake types into.
func (r RecipientRole) SessionInbox() bool { return r == RecipientDirector || r == RecipientSteward }

// StewardSenders are the senders the Steward's inbox takes. Only the Guildmaster talks to the Steward: no Director,
// other Director or Artisan reaches it (docs/director-modes.md). This list is where a later path, such as a
// Steward-to-Director or Director-to-Steward one, would add a sender.
var StewardSenders = []string{SenderGuildmaster}

// Recipient names one inbox.
// The Director has one inbox per store, so its other fields are empty.
// There is no Artisan registry yet: an Artisan is identified by the running
// stage attempt it works on (repository, task, step and attempt number).
type Recipient struct {
	Role       RecipientRole `json:"role"`
	Repository string        `json:"repository,omitempty"`
	TaskID     string        `json:"task_id,omitempty"`
	Step       string        `json:"step,omitempty"`
	Attempt    int           `json:"attempt,omitempty"`
}

// ArtifactRef points at a file a message refers to, by relative path and sha256.
type ArtifactRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Message is one durable message between a Director and an Artisan.
// It carries information only: sending, reading or acknowledging a message never
// changes a task, attempt or decision, and its type is not interpreted by the store.
type Message struct {
	// ID is unique in the store; sending records the mutation "message:<id>".
	ID        string        `json:"id"`
	Recipient RecipientRole `json:"recipient"`
	// Repository and TaskID name the task the message concerns.
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	// Step and Attempt name the stage attempt the message concerns. They identify
	// the recipient of an Artisan message and are optional for a Director message.
	Step      string        `json:"step,omitempty"`
	Attempt   int           `json:"attempt,omitempty"`
	Type      string        `json:"type"`
	Text      string        `json:"text"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
	// Sender is where the message was sent from, one of MessageSenders, set by Guildhall and never by the
	// envelope. It is asserted, not authenticated. A record written before senders were recorded has none and
	// reads as SenderUnknown (MessageSender).
	Sender string `json:"sender,omitempty"`
	// Sequence is set by the store to the send receipt's sequence.
	Sequence uint64 `json:"sequence"`
	// Acknowledged is set by AckMessage. It means the recipient consumed the
	// message, not that any work it mentions succeeded.
	Acknowledged bool `json:"acknowledged"`
}

// Message senders: where a message was sent from (docs/messaging.md#senders). Guildhall sets one on every
// message from where it was sent; none is chosen by the sender's envelope.
const (
	// SenderGuildhall is Guildhall in process: the engine's outbox, the launcher, a quality judge and the
	// workspace close sweep.
	SenderGuildhall = "guildhall"
	// SenderArtisan is `messages send` run inside an Artisan attempt, which the launcher marks with GUILDHALL_ATTEMPT.
	SenderArtisan = "artisan"
	// SenderDirector is `messages send` run in a session of the Director whose copy it sends to.
	SenderDirector = "director"
	// SenderOtherDirector is `messages send` run in a session of another copy's Director.
	SenderOtherDirector = "other-director"
	// SenderGuildmaster is `messages send` run outside any Guildhall session or attempt.
	SenderGuildmaster = "guildmaster"
	// SenderUnknown is a sender Guildhall cannot tell, and what a message recorded with no sender reads as.
	SenderUnknown = "unknown"
)

// MessageSenders is the fixed sender vocabulary; no other value is stored, so no free text reaches a sender.
var MessageSenders = []string{SenderGuildhall, SenderArtisan, SenderDirector, SenderOtherDirector, SenderGuildmaster, SenderUnknown}

// MessageSender returns m's sender, SenderUnknown for a message recorded with none.
func MessageSender(m Message) string {
	if m.Sender == "" {
		return SenderUnknown
	}
	return m.Sender
}

// To returns the inbox the message is addressed to.
func (m Message) To() Recipient {
	if m.Recipient.SessionInbox() {
		return Recipient{Role: m.Recipient}
	}
	return Recipient{Role: m.Recipient, Repository: m.Repository, TaskID: m.TaskID, Step: m.Step, Attempt: m.Attempt}
}

// messageIDPattern is the mutation id form without ":", so the derived mutation
// ids "message:<id>" and "message-ack:<id>" fit and cannot collide with each other.
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidateMessageID checks a message id's form.
func ValidateMessageID(id string) error {
	if !messageIDPattern.MatchString(id) {
		return invalid("message id %q must match %s", id, messageIDPattern)
	}
	return nil
}

func sendMutationID(id string) string { return "message:" + id }
func ackMutationID(id string) string  { return "message-ack:" + id }

// SendMessage durably stores a message and its receipt in one transaction and
// adds it to the recipient's inbox. The task must exist, a named attempt must
// exist, and an Artisan message needs a running attempt; no inbox is created for
// anything else. Resending the same message returns the original receipt, and
// reusing its id for a different message returns ErrMutationConflict.
// The receipt means the message is stored, not that the recipient has read it.
// The sender is not part of the request: resending the same message from elsewhere returns the original receipt
// and keeps the first sender, and a message with no sender digests as it did before senders were recorded.
func (s *Store) SendMessage(m Message) (Receipt, error) {
	if err := ValidateMessage(m); err != nil {
		return Receipt{}, err
	}
	req := m
	req.Sender = ""
	return s.mutate(sendMutationID(m.ID), opSendMessage, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = m.Repository, m.TaskID, m.Attempt, "message/"+m.ID
		if _, err := getTask(tx, m.Repository, m.TaskID); err != nil {
			return err
		}
		if m.Attempt > 0 {
			var a StageAttempt
			key := attemptKey(m.TaskID, m.Step, m.Attempt)
			if err := getJSON(repoBucket(tx, m.Repository, bucketAttempts), key, &a); err != nil {
				return fmt.Errorf("attempt %d of %s/%s step %q: %w", m.Attempt, m.Repository, m.TaskID, m.Step, err)
			}
			if m.Recipient == RecipientArtisan && a.Outcome != "" {
				return invalid("attempt %d of step %q already finished: %s", m.Attempt, m.Step, a.Outcome)
			}
		}
		messages, err := createRepoBucket(tx, m.Repository, bucketMessages)
		if err != nil {
			return err
		}
		if messages.Get([]byte(m.ID)) != nil {
			return fmt.Errorf("%w: message %q", ErrExists, m.ID)
		}
		inbox, err := createRepoBucket(tx, m.Repository, bucketInbox)
		if err != nil {
			return err
		}
		m.Sequence = r.Sequence
		if err := inbox.Put(inboxKey(m.To(), m.Sequence), []byte(m.ID)); err != nil {
			return err
		}
		return putJSON(messages, []byte(m.ID), m)
	})
}

// AckMessage records that the recipient consumed a message and removes it from
// the inbox. Only the inbox the message is addressed to can acknowledge it;
// retrying returns the original receipt. It changes nothing but the message.
func (s *Store) AckMessage(to Recipient, id string) (Receipt, error) {
	if err := ValidateRecipient(to); err != nil {
		return Receipt{}, err
	}
	if err := ValidateMessageID(id); err != nil {
		return Receipt{}, err
	}
	req := struct {
		To Recipient `json:"to"`
		ID string    `json:"id"`
	}{to, id}
	return s.mutate(ackMutationID(id), opAckMessage, req, func(tx *bolt.Tx, r *Receipt) error {
		m, err := getMessage(tx, id)
		if err != nil {
			return err
		}
		r.Repository, r.TaskID, r.Attempt, r.Record = m.Repository, m.TaskID, m.Attempt, "message/"+m.ID
		if m.To() != to {
			return invalid("message %q is addressed to %s, not %s", id, describeRecipient(m.To()), describeRecipient(to))
		}
		if m.Acknowledged {
			return invalid("message %q is already acknowledged", id)
		}
		m.Acknowledged = true
		if err := repoBucket(tx, m.Repository, bucketInbox).Delete(inboxKey(to, m.Sequence)); err != nil {
			return err
		}
		return putJSON(repoBucket(tx, m.Repository, bucketMessages), []byte(id), m)
	})
}

// Message returns one message, acknowledged or not.
func (s *Store) Message(id string) (Message, error) {
	if err := ValidateMessageID(id); err != nil {
		return Message{}, err
	}
	var m Message
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		m, err = getMessage(tx, id)
		return err
	})
	if err != nil {
		return Message{}, fmt.Errorf("store: %w", err)
	}
	return m, nil
}

// Inbox returns at most limit unacknowledged messages addressed to to, with a
// sequence greater than after, in send order, and whether more remain.
// The Director's inbox spans every repository in the store.
func (s *Store) Inbox(to Recipient, after uint64, limit int) ([]Message, bool, error) {
	if err := ValidateRecipient(to); err != nil {
		return nil, false, err
	}
	if limit < 1 || limit > MaxInboxLimit {
		return nil, false, invalid("inbox limit %d must be between 1 and %d", limit, MaxInboxLimit)
	}
	if after == math.MaxUint64 {
		return nil, false, nil
	}
	type entry struct {
		seq  uint64
		repo []byte
		id   []byte
	}
	var out []Message
	more := false
	err := s.view(func(tx *bolt.Tx) error {
		var repos [][]byte
		if to.Role.SessionInbox() {
			_ = tx.Bucket(bucketRepos).ForEachBucket(func(k []byte) error { repos = append(repos, k); return nil })
		} else {
			repos = [][]byte{[]byte(to.Repository)}
		}
		// Each repository contributes at most limit+1 entries, so the work is bounded by the limit.
		prefix := inboxPrefix(to)
		var entries []entry
		for _, repo := range repos {
			inbox := repoBucket(tx, string(repo), bucketInbox)
			if inbox == nil {
				continue
			}
			c := inbox.Cursor()
			n := 0
			for k, v := c.Seek(inboxKey(to, after+1)); k != nil && bytes.HasPrefix(k, prefix) && n <= limit; k, v = c.Next() {
				seq, err := strconv.ParseUint(string(k[len(prefix):]), 10, 64)
				if err != nil {
					return fmt.Errorf("%w: inbox key %q", ErrNotStore, k)
				}
				entries = append(entries, entry{seq, repo, v})
				n++
			}
		}
		slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.seq, b.seq) })
		if len(entries) > limit {
			entries, more = entries[:limit], true
		}
		for _, e := range entries {
			var m Message
			if err := getJSON(repoBucket(tx, string(e.repo), bucketMessages), e.id, &m); err != nil {
				return fmt.Errorf("inbox message %q: %w", e.id, err)
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: inbox of %s: %w", describeRecipient(to), err)
	}
	return out, more, nil
}

// getMessage finds a message through its send receipt, which names its repository.
func getMessage(tx *bolt.Tx, id string) (Message, error) {
	var r Receipt
	if err := getJSON(tx.Bucket(bucketReceipts), []byte(sendMutationID(id)), &r); err != nil || r.Operation != opSendMessage {
		return Message{}, fmt.Errorf("%w: message %q", ErrNotFound, id)
	}
	var m Message
	if err := getJSON(repoBucket(tx, r.Repository, bucketMessages), []byte(id), &m); err != nil {
		return Message{}, fmt.Errorf("message %q: %w", id, err)
	}
	return m, nil
}

// inboxPrefix is "director/", "steward/" or "artisan/<task>/<step>/<attempt>/"; ids contain
// no "/", so a prefix scan sees only that recipient's entries.
func inboxPrefix(to Recipient) []byte {
	if to.Role.SessionInbox() {
		return recordKey(string(to.Role), "")
	}
	return recordKey(string(to.Role), to.TaskID, to.Step, fmt.Sprintf("%010d", to.Attempt), "")
}

// inboxKey zero-pads the sequence so entries sort in send order.
func inboxKey(to Recipient, seq uint64) []byte {
	return append(inboxPrefix(to), fmt.Sprintf("%020d", seq)...)
}

func describeRecipient(to Recipient) string {
	if to.Role == RecipientArtisan {
		return fmt.Sprintf("the artisan of %s/%s step %q attempt %d", to.Repository, to.TaskID, to.Step, to.Attempt)
	}
	return string(to.Role)
}

// ValidateRecipient checks a recipient's form without reading the store.
func ValidateRecipient(to Recipient) error {
	switch to.Role {
	case RecipientDirector, RecipientSteward:
		if to != (Recipient{Role: to.Role}) {
			return invalid("the %s recipient takes no repository, task, step or attempt", to.Role)
		}
	case RecipientArtisan:
		if err := validateIDs(to.Repository, to.TaskID, to.Step); err != nil {
			return err
		}
		if to.Attempt < 1 {
			return invalid("an artisan recipient needs a positive attempt number, not %d", to.Attempt)
		}
	default:
		return invalid("recipient %q must be %q, %q or %q", to.Role, RecipientDirector, RecipientSteward, RecipientArtisan)
	}
	return nil
}

// ValidateMessage checks a new message's fields without reading the store.
func ValidateMessage(m Message) error {
	if err := ValidateMessageID(m.ID); err != nil {
		return err
	}
	if !m.Recipient.SessionInbox() && m.Recipient != RecipientArtisan {
		return invalid("recipient %q must be %q, %q or %q", m.Recipient, RecipientDirector, RecipientSteward, RecipientArtisan)
	}
	if m.Recipient == RecipientSteward && !slices.Contains(StewardSenders, m.Sender) {
		return invalid("the steward's inbox takes messages from %s only, not from %s", strings.Join(StewardSenders, ", "), MessageSender(m))
	}
	if err := validateIDs(m.Repository, m.TaskID, m.Type); err != nil {
		return err
	}
	switch {
	case m.Recipient == RecipientArtisan && m.Attempt == 0 && m.Step == "":
		return invalid("an artisan message needs the step and attempt it is addressed to")
	case (m.Step == "") != (m.Attempt == 0):
		return invalid("step %q and attempt %d must be given together", m.Step, m.Attempt)
	case m.Attempt < 0:
		return invalid("attempt number %d must be positive", m.Attempt)
	}
	if m.Step != "" {
		if err := validateIDs(m.Step); err != nil {
			return err
		}
	}
	if err := validateText("message text", m.Text, MaxMessageTextBytes); err != nil {
		return err
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
	if m.Sender != "" && !slices.Contains(MessageSenders, m.Sender) {
		return invalid("message sender %q must be one of %s", m.Sender, strings.Join(MessageSenders, ", "))
	}
	if m.Sequence != 0 || m.Acknowledged {
		return invalid("a new message has no sequence and is not acknowledged")
	}
	return nil
}
