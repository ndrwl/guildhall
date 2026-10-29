package store

import (
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var bucketRulings = []byte("rulings")

// RulingMove is what the Director rules a review finding to be (docs/tasks.md#director-rulings).
type RulingMove string

const (
	// RulingOutOfScope: the finding is outside the accepted spec; the reason cites the spec section.
	RulingOutOfScope RulingMove = "out-of-scope"
	// RulingDuplicate: the finding repeats another one.
	RulingDuplicate RulingMove = "duplicate"
	// RulingDefer: the item moves to a follow-up task; only a suggestion, or a /1 finding that cites no rule,
	// may be deferred.
	RulingDefer RulingMove = "defer"
)

// Ruling is the Director's ruling on one finding, or suggestion, of one review outcome document.
// It is recorded once, shown to the step's next review attempt, and contested at most once, by that attempt.
// A ruling never resolves a finding: only the reviewer or the Guildmaster does. The one exception is the
// nothing_open merge requirement, for which a disputed finding with an uncontested ruling no longer counts.
type Ruling struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	// Document is the name of the review outcome document holding the finding.
	Document string     `json:"document"`
	Finding  string     `json:"finding"`
	Move     RulingMove `json:"move"`
	Reason   string     `json:"reason"`
	// ContestedBy is the attempt, <step>-<n>, that contested the ruling; empty while it stands.
	ContestedBy string `json:"contested_by,omitempty"`
}

// RuleFinding records a Director ruling, receipt record ruling/<document>/<finding>, on an open task.
// The document must be recorded with a Review projection listing the finding, or a suggestion, under that id
// (ErrNotFound otherwise); an unknown move, a blank reason, a set ContestedBy and deferring a finding that cites a rule are refused
// with ErrInvalid, and a second ruling on the same finding of the same document with ErrExists.
func (s *Store) RuleFinding(mutationID string, r Ruling) (Receipt, error) {
	if err := validateIDs(r.Repository, r.TaskID, r.Document, r.Finding); err != nil {
		return Receipt{}, err
	}
	if err := validateText("reason", r.Reason, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	switch {
	case r.Move != RulingOutOfScope && r.Move != RulingDuplicate && r.Move != RulingDefer:
		return Receipt{}, invalid("ruling move %q must be %q, %q or %q", r.Move, RulingOutOfScope, RulingDuplicate, RulingDefer)
	case r.ContestedBy != "":
		return Receipt{}, invalid("a new ruling is not contested")
	}
	return s.mutate(mutationID, "rule-finding", r, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = r.Repository, r.TaskID, rulingRecord(r.Document, r.Finding)
		if _, err := openTask(tx, r.Repository, r.TaskID); err != nil {
			return err
		}
		var d DocumentRef
		if err := getJSON(repoBucket(tx, r.Repository, bucketDocuments), recordKey(r.TaskID, r.Document), &d); err != nil {
			return fmt.Errorf("document %q: %w", r.Document, err)
		}
		found, rule := false, ""
		if d.Review != nil {
			if i := slices.IndexFunc(d.Review.Findings, func(f workflow.Finding) bool { return f.ID == r.Finding }); i >= 0 {
				found, rule = true, d.Review.Findings[i].Rule
			} else {
				found = slices.ContainsFunc(d.Review.Suggestions, func(s workflow.Suggestion) bool { return s.ID == r.Finding })
			}
		}
		switch {
		case !found:
			return fmt.Errorf("finding %q of document %q: %w", r.Finding, r.Document, ErrNotFound)
		case r.Move == RulingDefer && rule != "":
			return invalid("finding %q cites rule %s; only a suggestion or a finding that cites no rule may be deferred", r.Finding, rule)
		}
		rulings, err := createRepoBucket(tx, r.Repository, bucketRulings)
		if err != nil {
			return err
		}
		key := recordKey(r.TaskID, r.Document, r.Finding)
		if rulings.Get(key) != nil {
			return fmt.Errorf("%w: ruling on finding %q of %q", ErrExists, r.Finding, r.Document)
		}
		return putJSON(rulings, key, r)
	})
}

// ContestRuling records that attempt by, <step>-<n>, contested a ruling. A ruling is contested once:
// contesting it again, by any attempt, is refused with ErrInvalid; an unknown ruling is ErrNotFound.
func (s *Store) ContestRuling(mutationID, repository, taskID, document, finding, by string) (Receipt, error) {
	if err := validateIDs(repository, taskID, document, finding, by); err != nil {
		return Receipt{}, err
	}
	req := Ruling{Repository: repository, TaskID: taskID, Document: document, Finding: finding, ContestedBy: by}
	return s.mutate(mutationID, "contest-ruling", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, rulingRecord(document, finding)
		key := recordKey(taskID, document, finding)
		var r Ruling
		if err := getJSON(repoBucket(tx, repository, bucketRulings), key, &r); err != nil {
			return fmt.Errorf("ruling on finding %q of %q: %w", finding, document, err)
		}
		if r.ContestedBy != "" {
			return invalid("the ruling on finding %q of %q was already contested by %s", finding, document, r.ContestedBy)
		}
		r.ContestedBy = by
		return putJSON(repoBucket(tx, repository, bucketRulings), key, r)
	})
}

func rulingRecord(document, finding string) string { return "ruling/" + document + "/" + finding }

// Rulings lists a task's rulings by document, then finding.
func (s *Store) Rulings(repository, taskID string) ([]Ruling, error) {
	return list[Ruling](s, repository, bucketRulings, recordKey(taskID, ""))
}
