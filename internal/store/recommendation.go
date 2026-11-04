package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketDecisionRecommendations is each repository's bucket of the Director's recommendations, one per decision,
// keyed <task>/<decision> (docs/store.md#decision-recommendations). It is created by the first write, so stores
// without one need no schema change.
var bucketDecisionRecommendations = []byte("decision-recommendations")

// Recommendation is the Director's one recommended answer to an open decision, written by decide open --recommend,
// decide explain --recommend and task say --recommend, each in its own transaction. Every write replaces the record
// and bumps Revision: the last committed write is the latest. Option is one of the decision's options; Words, from
// task say --words-file, pre-fill the page's Your words box; SubjectDigest is what the Director read when it
// recommended (SubjectDigestOf), and the record is stale once it differs from the decision's. Number is the
// conversation entry number of the task say recommendation that wrote it, which the page's Confirm names, and 0 for
// the other two writers. At is the commit time.
type Recommendation struct {
	Repository    string    `json:"repository"`
	TaskID        string    `json:"task_id"`
	Decision      string    `json:"decision"`
	Option        string    `json:"option"`
	Words         string    `json:"words,omitempty"`
	SubjectDigest string    `json:"subject_digest"`
	At            time.Time `json:"at"`
	Revision      int       `json:"revision"`
	Number        int       `json:"number,omitempty"`
}

// Current says the recommendation was made against d as it stands, the rule DecisionExplanation.Current keeps.
func (r Recommendation) Current(d Decision) bool {
	return r.SubjectDigest == SubjectDigestOf(d)
}

// validateRecommendation checks what a recommendation can be checked for outside its transaction: ids, a one-line
// option, a blank, spaced or oversized digest and words over MaxTextBytes.
func validateRecommendation(r Recommendation) error {
	if err := validateIDs(r.Repository, r.TaskID, r.Decision); err != nil {
		return err
	}
	if err := validateSayingOption(r.Option); err != nil {
		return err
	}
	if err := validateSubjectDigest(r.SubjectDigest); err != nil {
		return err
	}
	if r.Words != "" {
		if err := validateText("recommendation words", r.Words, MaxTextBytes); err != nil {
			return err
		}
	}
	if r.Number < 0 {
		return invalid("recommendation entry number %d must not be negative", r.Number)
	}
	return nil
}

// validateSubjectDigest refuses a blank, oversized or spaced subject digest.
func validateSubjectDigest(digest string) error {
	if err := validateText("subject digest", digest, MaxSubjectDigestBytes); err != nil {
		return err
	}
	if strings.ContainsAny(digest, " \t\r\n") {
		return invalid("subject digest %q must have no spaces", digest)
	}
	return nil
}

// putRecommendation writes r as its decision's one recommendation inside a writer's transaction, replacing any
// earlier one and bumping its revision, at rc's commit time. It refuses with ErrInvalid a decision that is answered
// or void and an option the decision does not offer; an unknown decision is ErrNotFound. Every refusal fails the
// whole transaction, so the writer's own records are not written either.
func (s *Store) putRecommendation(tx *bolt.Tx, rc *Receipt, r Recommendation) error {
	if err := validateRecommendation(r); err != nil {
		return err
	}
	repository, taskID, id := r.Repository, r.TaskID, r.Decision
	var dec Decision
	if err := getJSON(repoBucket(tx, repository, bucketDecisions), recordKey(taskID, id), &dec); err != nil {
		return fmt.Errorf("decision %q of %s/%s: %w", id, repository, taskID, err)
	}
	switch {
	case dec.Answered:
		return invalid("decision %q of %s/%s is answered; only an open decision takes a recommendation", id, repository, taskID)
	case dec.Void:
		return invalid("decision %q of %s/%s is void: %s", id, repository, taskID, dec.VoidReason)
	case len(dec.Options) == 0:
		return invalid("decision %q of %s/%s offers no options, so none can be recommended", id, repository, taskID)
	case !slices.Contains(dec.Options, r.Option):
		return invalid("decision %q of %s/%s offers %s; %q is not one of them", id, repository, taskID, strings.Join(dec.Options, ", "), r.Option)
	}
	b, err := createRepoBucket(tx, repository, bucketDecisionRecommendations)
	if err != nil {
		return err
	}
	var prior Recommendation
	if err := getJSON(b, recordKey(taskID, id), &prior); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if rc.CommittedAt.IsZero() {
		rc.CommittedAt = s.now().UTC()
	}
	r.Revision, r.At = prior.Revision+1, rc.CommittedAt
	return putJSON(b, recordKey(taskID, id), r)
}

// DecisionRecommendation returns the recommendation of decision id, or ErrNotFound when it has none, as in a store
// written before recommendations were recorded.
func (s *Store) DecisionRecommendation(repository, taskID, id string) (Recommendation, error) {
	var r Recommendation
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketDecisionRecommendations), recordKey(taskID, id), &r)
	})
	if err != nil {
		return Recommendation{}, fmt.Errorf("store: recommendation of decision %q of %s/%s: %w", id, repository, taskID, err)
	}
	return r, nil
}

// SayRecommendation appends say, a Director recommendation on the open decision its tag names, to an open task's
// conversation and writes the decision's recommendation record from it, with that entry's number and subjectDigest,
// what the Director read, in one transaction, receipt record conversation/<n>. A digest that is not the decision's
// current one is recorded, so the page shows the recommendation as stale. It refuses with ErrInvalid a saying that
// is not a Director recommendation or does not validate, a tag that names no decision, a decision that is answered
// or void, offers no options or does not offer the option, and a blank or oversized digest; a closed task is
// ErrInvalid and an unknown one ErrNotFound. Every refusal writes nothing.
func (s *Store) SayRecommendation(mutationID, repository, taskID string, say Saying, subjectDigest string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if err := validateSaying(say); err != nil {
		return Receipt{}, err
	}
	if say.Kind != ConversationRecommendation {
		return Receipt{}, invalid("a %s entry is not a recommendation", say.Kind)
	}
	rec := Recommendation{Repository: repository, TaskID: taskID, Decision: say.Tag, Option: say.Option, Words: say.Words, SubjectDigest: subjectDigest}
	if err := validateRecommendation(rec); err != nil {
		return Receipt{}, err
	}
	req := struct {
		ConversationEntry
		SubjectDigest string `json:"subject_digest"`
	}{ConversationEntry{Repository: repository, TaskID: taskID, Saying: say}, subjectDigest}
	return s.mutate(mutationID, "say-recommendation", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		if b := repoBucket(tx, repository, bucketDecisions); b == nil || b.Get(recordKey(taskID, say.Tag)) == nil {
			return invalid("task %s/%s has no decision %q to recommend an answer to", repository, taskID, say.Tag)
		}
		rc.CommittedAt = s.now().UTC()
		n, err := appendConversation(tx, repository, taskID, say, rc.CommittedAt)
		if err != nil {
			return err
		}
		rc.Record = ConversationRecord(n)
		rec.Number = n
		return s.putRecommendation(tx, rc, rec)
	})
}
