package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketDecisionExplanations = []byte("decision-explanations")

// MaxSubjectDigestBytes bounds an explanation's subject digest: a "sha256:<64 hex>" document digest or a 40 or 64 hex
// head is well within it.
const MaxSubjectDigestBytes = 128

// DecisionExplanation is the Director's plain-English explanation of an open decision, written once when the ask opens
// and shown on the task page above the ask's summary (docs/store.md#decision-explanations). The decision itself never
// changes. SubjectDigest is what the Director read when it wrote the text (SubjectDigestOf); the page hides an
// explanation whose digest no longer matches. Recommend is the option the Director recommends, "" for none. Explaining
// again replaces the record and bumps Revision, the number of explanations so far, which the mutation id carries as a
// deferral's cycle.
type DecisionExplanation struct {
	Repository    string    `json:"repository"`
	TaskID        string    `json:"task_id"`
	Decision      string    `json:"decision"`
	Text          string    `json:"text"`
	SubjectDigest string    `json:"subject_digest"`
	Recommend     string    `json:"recommend,omitempty"`
	At            time.Time `json:"at"`
	Revision      int       `json:"revision"`
}

// DecisionExplainRecord is the receipt record name of an explanation of decision id.
func DecisionExplainRecord(id string) string { return "decision-explain/" + id }

// SubjectDigestOf is the digest an explanation of d is written against: its subject's digest, or, for a decision with
// no subject, such as a question or a grant, "sha256:" and the hex sha256 of its question text.
func SubjectDigestOf(d Decision) string {
	if d.Subject != nil {
		return d.Subject.Digest
	}
	sum := sha256.Sum256([]byte(d.Question))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Current says the explanation was written against d as it stands, so the page may show it.
func (e DecisionExplanation) Current(d Decision) bool {
	return e.SubjectDigest == SubjectDigestOf(d)
}

// ExplainDecision records text, the Director's Markdown explanation of open decision id, written against
// subjectDigest, with recommend the option it recommends or "", receipt record decision-explain/<id>. revision is the
// explanation record's revision the caller read, 0 when there was none; the new record's is one more. In the same
// transaction it refuses with ErrInvalid blank, oversized or non-UTF-8 text, a blank or oversized digest, an
// answered or void decision and a recommend that is not one of the decision's options; an unknown decision is
// ErrNotFound and a stored revision other than revision is ErrExists. The record's time is the mutation's. A
// recommend that is not "" also replaces the decision's recommendation record (Recommendation), written against
// subjectDigest in the same transaction; with "" the record is left as it is.
func (s *Store) ExplainDecision(mutationID, repository, taskID, id, text, subjectDigest, recommend string, revision int) (Receipt, error) {
	if err := validateIDs(repository, taskID, id); err != nil {
		return Receipt{}, err
	}
	if revision < 0 {
		return Receipt{}, invalid("explanation revision %d must not be negative", revision)
	}
	if err := validateText("explanation", text, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	if err := validateText("subject digest", subjectDigest, MaxSubjectDigestBytes); err != nil {
		return Receipt{}, err
	}
	if strings.ContainsAny(subjectDigest, " \t\r\n") {
		return Receipt{}, invalid("subject digest %q must have no spaces", subjectDigest)
	}
	e := DecisionExplanation{Repository: repository, TaskID: taskID, Decision: id, Text: text, SubjectDigest: subjectDigest, Recommend: recommend,
		Revision: revision + 1}
	return s.mutate(mutationID, "explain-decision", e, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, DecisionExplainRecord(id)
		var dec Decision
		if err := getJSON(repoBucket(tx, repository, bucketDecisions), recordKey(taskID, id), &dec); err != nil {
			return fmt.Errorf("decision %q of %s/%s: %w", id, repository, taskID, err)
		}
		switch {
		case dec.Answered:
			return invalid("decision %q of %s/%s is answered; only an open decision is explained", id, repository, taskID)
		case dec.Void:
			return invalid("decision %q of %s/%s is void: %s", id, repository, taskID, dec.VoidReason)
		case recommend != "" && !slices.Contains(dec.Options, recommend):
			if len(dec.Options) == 0 {
				return invalid("decision %q of %s/%s offers no options, so none can be recommended", id, repository, taskID)
			}
			return invalid("decision %q of %s/%s offers %s; %q is not one of them", id, repository, taskID, strings.Join(dec.Options, ", "), recommend)
		}
		b, err := createRepoBucket(tx, repository, bucketDecisionExplanations)
		if err != nil {
			return err
		}
		prior, err := readExplanation(b, taskID, id)
		if err != nil {
			return err
		}
		if prior.Revision != revision {
			return fmt.Errorf("%w: explanation of decision %q of %s/%s is at revision %d, not %d", ErrExists, id, repository, taskID, prior.Revision, revision)
		}
		rc.CommittedAt = s.now().UTC()
		e.At = rc.CommittedAt
		if err := putJSON(b, recordKey(taskID, id), e); err != nil {
			return err
		}
		if recommend == "" {
			return nil
		}
		// The recommendation is the decision's one record, written with the explanation.
		return s.putRecommendation(tx, rc, Recommendation{Repository: repository, TaskID: taskID, Decision: id, Option: recommend, SubjectDigest: subjectDigest})
	})
}

// DecisionExplanation returns the explanation of decision id, or ErrNotFound when it has none.
func (s *Store) DecisionExplanation(repository, taskID, id string) (DecisionExplanation, error) {
	var e DecisionExplanation
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketDecisionExplanations), recordKey(taskID, id), &e)
	})
	if err != nil {
		return DecisionExplanation{}, fmt.Errorf("store: explanation of decision %q of %s/%s: %w", id, repository, taskID, err)
	}
	return e, nil
}

// readExplanation reads an explanation record from b, the zero record when there is none.
func readExplanation(b *bolt.Bucket, taskID, id string) (DecisionExplanation, error) {
	var e DecisionExplanation
	if err := getJSON(b, recordKey(taskID, id), &e); err != nil && !errors.Is(err, ErrNotFound) {
		return DecisionExplanation{}, err
	}
	return e, nil
}
