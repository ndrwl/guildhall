package store

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketConversation is each repository's bucket of task conversation entries, keyed <task>/<%010d number>: the
// talk between the Guildmaster and the Director about a task, which its task page shows (docs/store.md#task-conversation).
// It is created by the first entry, so stores without one need no schema change.
var bucketConversation = []byte("task_conversation")

// The authors of a conversation entry, a fixed vocabulary that is never free text.
const (
	// ConversationGuildmasterPage is the Guildmaster on the task page, such as a words-only note.
	ConversationGuildmasterPage = "guildmaster-page"
	// ConversationGuildmasterChat is the Guildmaster in chat, recorded by the Director or by decide answer.
	ConversationGuildmasterChat = "guildmaster-chat"
	// ConversationDirector is the Director.
	ConversationDirector = "director"
)

// ConversationAuthors lists the authors in the order the docs name them.
var ConversationAuthors = []string{ConversationGuildmasterPage, ConversationGuildmasterChat, ConversationDirector}

// The kinds of a conversation entry.
const (
	// ConversationSay is words said, answering nothing.
	ConversationSay = "say"
	// ConversationRound is a grill round the Director put on the page.
	ConversationRound = "round"
	// ConversationRoundAnswers is the Guildmaster's answers to a round, sent.
	ConversationRoundAnswers = "round-answers"
	// ConversationRecommendation is the Director's reading of the Guildmaster's words and its recommended answer.
	ConversationRecommendation = "recommendation"
	// ConversationConfirmed records that the Guildmaster confirmed a recommendation.
	ConversationConfirmed = "confirmed"
)

// ConversationKinds lists the kinds in the order the docs name them.
var ConversationKinds = []string{ConversationSay, ConversationRound, ConversationRoundAnswers, ConversationRecommendation, ConversationConfirmed}

// Saying is what one conversation entry says: who, about which attempt or decision of the task (its tag, such as
// grill-2 or accept-1), what kind of entry it is, and its Markdown text.
type Saying struct {
	Author string `json:"author"`
	Tag    string `json:"tag"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
}

// ConversationEntry is one entry of a task's conversation. Entries are numbered per task from 1 and append-only:
// none is ever edited or deleted.
type ConversationEntry struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Number     int    `json:"number"`
	Saying
	// At is the commit time of the mutation that wrote the entry.
	At time.Time `json:"at"`
}

// validateSaying checks a saying's author, kind, tag and text: an author and kind of the fixed vocabularies, a tag of
// id form and Markdown text that is not blank, is valid UTF-8 and is at most MaxTextBytes.
func validateSaying(say Saying) error {
	switch {
	case !slices.Contains(ConversationAuthors, say.Author):
		return invalid("conversation author %q is not one of %v", say.Author, ConversationAuthors)
	case !slices.Contains(ConversationKinds, say.Kind):
		return invalid("conversation kind %q is not one of %v", say.Kind, ConversationKinds)
	}
	if err := validateIDs(say.Tag); err != nil {
		return err
	}
	return validateText("conversation text", say.Text, MaxTextBytes)
}

// AddConversation appends say to the conversation of an open task, receipt record conversation/<n>. A closed (done
// or cancelled) task's conversation stays readable but takes no new entry: ErrInvalid, as is a saying that does not
// validate; an unknown task is ErrNotFound. The caller chooses the mutation id, so a retry replays the receipt.
func (s *Store) AddConversation(mutationID, repository, taskID string, say Saying) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if err := validateSaying(say); err != nil {
		return Receipt{}, err
	}
	req := ConversationEntry{Repository: repository, TaskID: taskID, Saying: say}
	return s.mutate(mutationID, "add-conversation", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		// The entry's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		n, err := appendConversation(tx, repository, taskID, say, rc.CommittedAt)
		rc.Record = ConversationRecord(n)
		return err
	})
}

// mirrorConversation appends say to the conversation of repository/taskID inside another mutation's transaction, so
// the entry and the record it mirrors are written together or not at all. It writes nothing for a nil say or a task
// that is not open, whose record is still written: a done task's audit is answered as any other decision.
// rc.CommittedAt is set, so the entry's time is the mutation's.
func (s *Store) mirrorConversation(tx *bolt.Tx, rc *Receipt, repository, taskID string, say *Saying) error {
	if say == nil {
		return nil
	}
	if t, err := getTask(tx, repository, taskID); err != nil || t.State != TaskOpen {
		return nil
	}
	if rc.CommittedAt.IsZero() {
		rc.CommittedAt = s.now().UTC()
	}
	_, err := appendConversation(tx, repository, taskID, *say, rc.CommittedAt)
	return err
}

// appendConversation writes say as the task's next conversation entry at time at and returns its number.
func appendConversation(tx *bolt.Tx, repository, taskID string, say Saying, at time.Time) (int, error) {
	b, err := createRepoBucket(tx, repository, bucketConversation)
	if err != nil {
		return 0, err
	}
	n := 1
	c := b.Cursor()
	prefix := recordKey(taskID, "")
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		n++
	}
	e := ConversationEntry{Repository: repository, TaskID: taskID, Number: n, Saying: say, At: at}
	return n, putJSON(b, conversationKey(taskID, n), e)
}

// Conversation lists a task's conversation entries by number, empty for a task with none.
func (s *Store) Conversation(repository, taskID string) ([]ConversationEntry, error) {
	out, err := list[ConversationEntry](s, repository, bucketConversation, recordKey(taskID, ""))
	return append([]ConversationEntry{}, out...), err
}

// ConversationRecord is the receipt record name of conversation entry number.
func ConversationRecord(number int) string { return "conversation/" + strconv.Itoa(number) }

// conversationKey zero-pads the number so entries sort numerically.
func conversationKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
