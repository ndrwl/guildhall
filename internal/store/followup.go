package store

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketFollowUps is the nested bucket of the director bucket holding the Director's follow-ups, keyed by id.
var bucketFollowUps = []byte("follow-ups")

// FollowUp is something the Director means to do once named tasks merge, with no attempt or decision to hold yet
// (docs/decisions/hold-until-merged.md). It is a hold: status lists it as held while it waits and as a
// follow-up item in needs_me once released, until it is done. A done follow-up stays, with Done set.
type FollowUp struct {
	// ID is "f<k>", k from the follow-ups bucket's own sequence.
	ID string `json:"id"`
	// Text is one line of at most MaxNoteBytes, as a note's is.
	Text        string    `json:"text"`
	UntilMerged []TaskRef `json:"until_merged"`
	// Created is when it was added, in UTC.
	Created time.Time `json:"created"`
	Done    bool      `json:"done,omitempty"`
}

func followUpRecord(id string) string { return "director/follow-up/" + id }

// AddFollowUp adds a follow-up with text, held until the tasks untilMerged names merge, written by session n, under
// the mutation id "follow-up-add:<n>:<sha256(text|tasks)>": replaying it returns the follow-up it added. A blank,
// multi-line or oversized text, a hold checkUntilMerged refuses and a session that is not the newest or is stopped are
// ErrInvalid; an unknown task or session is ErrNotFound.
func (s *Store) AddFollowUp(n int, text string, untilMerged []TaskRef) (FollowUp, Receipt, error) {
	if err := validateNoteText("follow-up text", text, MaxNoteBytes); err != nil {
		return FollowUp{}, Receipt{}, err
	}
	if err := validateUntilMerged(untilMerged); err != nil {
		return FollowUp{}, Receipt{}, err
	}
	if n < 1 {
		return FollowUp{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	req := struct {
		Session     int       `json:"session"`
		Text        string    `json:"text"`
		UntilMerged []TaskRef `json:"until_merged"`
	}{n, text, untilMerged}
	id := fmt.Sprintf("follow-up-add:%d:%s", n, hexSum(text+"|"+TaskRefList(untilMerged)))
	r, err := s.mutate(id, "add-follow-up", req, func(tx *bolt.Tx, r *Receipt) error {
		if err := checkWriterSession(tx, n); err != nil {
			return err
		}
		if err := checkUntilMerged(tx, untilMerged, TaskRef{}); err != nil {
			return err
		}
		b, err := createDirectorBucket(tx, bucketFollowUps)
		if err != nil {
			return err
		}
		k, err := b.NextSequence()
		if err != nil {
			return err
		}
		f := FollowUp{ID: "f" + strconv.FormatUint(k, 10), Text: text, UntilMerged: untilMerged, Created: s.now().UTC()}
		r.Record = followUpRecord(f.ID)
		return putJSON(b, []byte(f.ID), f)
	})
	if err != nil {
		return FollowUp{}, Receipt{}, err
	}
	return s.followUpOf(r)
}

// DoneFollowUp marks follow-up id done, written by session n, under the mutation id "follow-up-done:<id>": the
// follow-up stays with Done set, and replaying returns the receipt. A follow-up already done or a session that is not
// the newest or is stopped is ErrInvalid; an unknown follow-up is ErrNotFound.
func (s *Store) DoneFollowUp(n int, id string) (FollowUp, Receipt, error) {
	if n < 1 {
		return FollowUp{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	req := struct {
		ID string `json:"id"`
	}{id}
	r, err := s.mutate("follow-up-done:"+id, "done-follow-up", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = followUpRecord(id)
		if err := checkWriterSession(tx, n); err != nil {
			return err
		}
		f, err := readFollowUp(tx, id)
		switch {
		case err != nil:
			return err
		case f.Done:
			return invalid("follow-up %s is already done", id)
		}
		f.Done = true
		return putJSON(directorBucket(tx, bucketFollowUps), []byte(id), f)
	})
	if err != nil {
		return FollowUp{}, Receipt{}, err
	}
	return s.followUpOf(r)
}

// FollowUps returns the follow-ups, done ones too when all is set, in id order.
func (s *Store) FollowUps(all bool) ([]FollowUp, error) {
	var out []FollowUp
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		out, err = followUps(tx, all)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: follow-ups: %w", err)
	}
	return out, nil
}

func followUps(tx *bolt.Tx, all bool) ([]FollowUp, error) {
	out := []FollowUp{}
	b := directorBucket(tx, bucketFollowUps)
	if b == nil {
		return out, nil
	}
	err := b.ForEach(func(k, v []byte) error {
		var f FollowUp
		if err := decode(k, v, &f); err != nil {
			return err
		}
		if all || !f.Done {
			out = append(out, f)
		}
		return nil
	})
	slices.SortFunc(out, func(a, b FollowUp) int { return cmp.Compare(followUpNumber(a.ID), followUpNumber(b.ID)) })
	return out, err
}

// followUpOf reads the follow-up a follow-up receipt names.
func (s *Store) followUpOf(r Receipt) (FollowUp, Receipt, error) {
	id := strings.TrimPrefix(r.Record, followUpRecord(""))
	var f FollowUp
	if err := s.view(func(tx *bolt.Tx) error {
		var err error
		f, err = readFollowUp(tx, id)
		return err
	}); err != nil {
		return FollowUp{}, Receipt{}, fmt.Errorf("store: %w", err)
	}
	return f, r, nil
}

func readFollowUp(tx *bolt.Tx, id string) (FollowUp, error) {
	var f FollowUp
	if err := getJSON(directorBucket(tx, bucketFollowUps), []byte(id), &f); err != nil {
		return FollowUp{}, fmt.Errorf("follow-up %s: %w", id, err)
	}
	return f, nil
}

// followUpNumber is k of follow-up id "f<k>", 0 for any other id.
func followUpNumber(id string) uint64 {
	k, _ := strconv.ParseUint(strings.TrimPrefix(id, "f"), 10, 64)
	return k
}
