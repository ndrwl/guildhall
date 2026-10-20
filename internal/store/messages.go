package store

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"

	bolt "go.etcd.io/bbolt"
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
)

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
	// Sequence is set by the store to the send receipt's sequence.
	Sequence uint64 `json:"sequence"`
	// Acknowledged is set by AckMessage. It means the recipient consumed the
	// message, not that any work it mentions succeeded.
	Acknowledged bool `json:"acknowledged"`
}

// To returns the inbox the message is addressed to.
func (m Message) To() Recipient {
	if m.Recipient == RecipientDirector {
		return Recipient{Role: RecipientDirector}
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
func (s *Store) SendMessage(m Message) (Receipt, error) {
	if err := ValidateMessage(m); err != nil {
		return Receipt{}, err
	}
	return s.mutate(sendMutationID(m.ID), opSendMessage, m, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt = m.Repository, m.TaskID, m.Attempt
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
		r.Repository, r.TaskID, r.Attempt = m.Repository, m.TaskID, m.Attempt
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
		if to.Role == RecipientDirector {
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

// inboxPrefix is "director/" or "artisan/<task>/<step>/<attempt>/"; ids contain
// no "/", so a prefix scan sees only that recipient's entries.
func inboxPrefix(to Recipient) []byte {
	if to.Role == RecipientDirector {
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
	case RecipientDirector:
		if to != (Recipient{Role: RecipientDirector}) {
			return invalid("the director recipient takes no repository, task, step or attempt")
		}
	case RecipientArtisan:
		if err := validateIDs(to.Repository, to.TaskID, to.Step); err != nil {
			return err
		}
		if to.Attempt < 1 {
			return invalid("an artisan recipient needs a positive attempt number, not %d", to.Attempt)
		}
	default:
		return invalid("recipient %q must be %q or %q", to.Role, RecipientDirector, RecipientArtisan)
	}
	return nil
}

// ValidateMessage checks a new message's fields without reading the store.
func ValidateMessage(m Message) error {
	if err := ValidateMessageID(m.ID); err != nil {
		return err
	}
	if m.Recipient != RecipientDirector && m.Recipient != RecipientArtisan {
		return invalid("recipient %q must be %q or %q", m.Recipient, RecipientDirector, RecipientArtisan)
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
		if !isLowerHex(a.SHA256, 64) {
			return invalid("artifact sha256 %q must be 64 lowercase hex characters", a.SHA256)
		}
	}
	if m.Sequence != 0 || m.Acknowledged {
		return invalid("a new message has no sequence and is not acknowledged")
	}
	return nil
}
