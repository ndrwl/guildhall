package store

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// bucketDrafts is each repository's bucket of the Guildmaster's saved drafts, keyed <task>/<%010d number>: the words
// and marks of a grill round's questions the task page saved before the round was sent (docs/store.md#drafts). It is
// created by the first draft, so stores without one need no schema change.
var bucketDrafts = []byte("task_drafts")

// The marks of a draft: whether the Guildmaster marked its question answered, and how.
const (
	// DraftUnmarked is words saved that are not an answer yet.
	DraftUnmarked = ""
	// DraftAccept accepts the question's recommendation.
	DraftAccept = "accept"
	// DraftWords answers the question with the draft's words.
	DraftWords = "words"
)

// DraftMarks lists the marks in the order the docs name them.
var DraftMarks = []string{DraftUnmarked, DraftAccept, DraftWords}

// Draft is one saved draft of one question: the attempt or decision it is under (Tag), the conversation entry of the
// round it answers (Round), the question's number from 1, its mark and the words in its box, which may be empty. A
// draft is not an answer: it never goes through AnswerDecision, and only sending its round makes it one.
type Draft struct {
	Tag      string `json:"tag"`
	Round    int    `json:"round"`
	Question int    `json:"question"`
	Mark     string `json:"mark,omitempty"`
	Text     string `json:"text,omitempty"`
}

// DraftEntry is one saved draft of a task. Entries are numbered per task from 1 and append-only; for each tag, round
// and question the latest entry wins. A task's drafts are dropped when it closes.
type DraftEntry struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Number     int    `json:"number"`
	Draft
	// At is the commit time of the mutation that saved the draft.
	At time.Time `json:"at"`
}

// validateDraft checks a draft: a tag of id form, a round and question from 1, a mark of DraftMarks, a draft marked
// words that has words, and words of valid UTF-8 at most MaxTextBytes.
func validateDraft(d Draft) error {
	if err := validateIDs(d.Tag); err != nil {
		return err
	}
	switch {
	case d.Round < 1 || d.Question < 1:
		return invalid("a draft names its round and question, numbered from 1, not %d and %d", d.Round, d.Question)
	case !slices.Contains(DraftMarks, d.Mark):
		return invalid("draft mark %q is not one of %q", d.Mark, DraftMarks)
	case len(d.Text) > MaxTextBytes:
		return invalid("draft words are %d bytes; the limit is %d", len(d.Text), MaxTextBytes)
	case !utf8.ValidString(d.Text):
		return invalid("draft words are not valid UTF-8")
	case d.Mark == DraftWords && isBlank(d.Text):
		return invalid("a draft answered with words needs words")
	}
	return nil
}

func isBlank(s string) bool { return len(bytes.TrimSpace([]byte(s))) == 0 }

// SaveDrafts appends each of drafts that differs from its question's latest draft to an open task's drafts, in one
// transaction, receipt record drafts/<number of the last entry written>, or drafts/0 when none differs. A closed task
// is ErrInvalid, as is a draft that does not validate; an unknown task is ErrNotFound. The caller chooses the mutation
// id, so a retry replays the receipt.
func (s *Store) SaveDrafts(mutationID, repository, taskID string, drafts []Draft) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if len(drafts) == 0 {
		return Receipt{}, invalid("no drafts to save")
	}
	for _, d := range drafts {
		if err := validateDraft(d); err != nil {
			return Receipt{}, err
		}
	}
	req := struct {
		Repository string  `json:"repository"`
		TaskID     string  `json:"task_id"`
		Drafts     []Draft `json:"drafts"`
	}{repository, taskID, drafts}
	return s.mutate(mutationID, "save-drafts", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketDrafts)
		if err != nil {
			return err
		}
		var all []DraftEntry
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		latest := latestDrafts(all)
		rc.CommittedAt = s.now().UTC()
		n, last := len(all), 0
		for _, d := range drafts {
			if old, ok := latest[draftKeyOf(d)]; ok && old.Draft == d {
				continue
			}
			if _, ok := latest[draftKeyOf(d)]; !ok && d.Mark == DraftUnmarked && d.Text == "" {
				// An empty box never saved before is no draft.
				continue
			}
			n++
			e := DraftEntry{Repository: repository, TaskID: taskID, Number: n, Draft: d, At: rc.CommittedAt}
			if err := putJSON(b, draftKey(taskID, n), e); err != nil {
				return err
			}
			latest[draftKeyOf(d)] = e
			last = n
		}
		rc.Record = "drafts/" + strconv.Itoa(last)
		return nil
	})
}

// Drafts lists a task's latest draft of each tag, round and question, ordered by tag, round and question; empty for
// a task with none, and for a closed task, whose drafts were dropped.
func (s *Store) Drafts(repository, taskID string) ([]DraftEntry, error) {
	all, err := list[DraftEntry](s, repository, bucketDrafts, recordKey(taskID, ""))
	if err != nil {
		return nil, err
	}
	return sortedDrafts(latestDrafts(all)), nil
}

type draftID struct {
	tag             string
	round, question int
}

func draftKeyOf(d Draft) draftID { return draftID{d.Tag, d.Round, d.Question} }

// latestDrafts keeps the latest of entries, which are in number order, for each tag, round and question.
func latestDrafts(entries []DraftEntry) map[draftID]DraftEntry {
	out := map[draftID]DraftEntry{}
	for _, e := range entries {
		out[draftKeyOf(e.Draft)] = e
	}
	return out
}

func sortedDrafts(m map[draftID]DraftEntry) []DraftEntry {
	out := make([]DraftEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b DraftEntry) int {
		return cmp.Or(cmp.Compare(a.Tag, b.Tag), cmp.Compare(a.Round, b.Round), cmp.Compare(a.Question, b.Question))
	})
	return out
}

// dropDrafts deletes every draft of a task that is closing, inside its close's transaction: a round never sent is
// never answered, and its saved words go with it.
func dropDrafts(tx *bolt.Tx, repository, taskID string) error {
	b := repoBucket(tx, repository, bucketDrafts)
	if b == nil {
		return nil
	}
	prefix := recordKey(taskID, "")
	var keys [][]byte
	c := b.Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		keys = append(keys, bytes.Clone(k))
	}
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// draftKey zero-pads the number so drafts sort numerically.
func draftKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
