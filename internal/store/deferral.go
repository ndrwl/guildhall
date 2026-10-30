package store

import (
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var bucketDecisionDeferrals = []byte("decision-deferrals")

// DecisionDeferral is the Director's record that an open decision is held out of needs_me until named tasks merge
// (docs/decisions/0096-hold-until-merged.md). The decision itself never changes, so it stays open and other tasks'
// dependency states do not shift. Undeferring keeps the record with Deferred false, so Cycle, the number of
// undefers so far, survives and the next deferral's mutation id differs from the last one's, as a park's does.
type DecisionDeferral struct {
	Repository  string    `json:"repository"`
	TaskID      string    `json:"task_id"`
	Decision    string    `json:"decision"`
	Reason      string    `json:"reason"`
	UntilMerged []TaskRef `json:"until_merged"`
	Deferred    bool      `json:"deferred"`
	Cycle       int       `json:"cycle"`
}

// DecisionDeferRecord and decisionUndeferRecord are the receipt record names of a deferral and an undefer of
// decision id.
func DecisionDeferRecord(id string) string { return "decision-defer/" + id }

func decisionUndeferRecord(id string) string { return "decision-undefer/" + id }

// DeferDecision defers open decision id of a task, receipt record decision-defer/<id>, with the Director's reason,
// until the tasks untilMerged names merge. cycle is the deferral record's cycle the caller read, 0 when there was
// none. In the same transaction it refuses with ErrInvalid a blank or oversized reason, an answered decision and a
// hold checkUntilMerged refuses; an unknown decision is ErrNotFound. A stored cycle other than cycle, or a decision already
// deferred under another mutation id, is ErrExists.
func (s *Store) DeferDecision(mutationID, repository, taskID, id, reason string, untilMerged []TaskRef, cycle int) (Receipt, error) {
	if err := validateIDs(repository, taskID, id); err != nil {
		return Receipt{}, err
	}
	if cycle < 0 {
		return Receipt{}, invalid("deferral cycle %d must not be negative", cycle)
	}
	if err := validateText("reason", reason, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	if err := validateUntilMerged(untilMerged); err != nil {
		return Receipt{}, err
	}
	d := DecisionDeferral{Repository: repository, TaskID: taskID, Decision: id, Reason: reason, UntilMerged: untilMerged, Deferred: true, Cycle: cycle}
	return s.mutate(mutationID, "defer-decision", d, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, DecisionDeferRecord(id)
		var dec Decision
		if err := getJSON(repoBucket(tx, repository, bucketDecisions), recordKey(taskID, id), &dec); err != nil {
			return fmt.Errorf("decision %q of %s/%s: %w", id, repository, taskID, err)
		}
		if dec.Answered {
			return invalid("decision %q of %s/%s is answered; only an open decision is deferred", id, repository, taskID)
		}
		if err := checkUntilMerged(tx, untilMerged, TaskRef{Repository: repository, ID: taskID}); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketDecisionDeferrals)
		if err != nil {
			return err
		}
		prior, err := readDeferral(b, taskID, id)
		if err != nil {
			return err
		}
		switch {
		case prior.Cycle != cycle:
			return fmt.Errorf("%w: deferral of decision %q of %s/%s is at cycle %d, not %d", ErrExists, id, repository, taskID, prior.Cycle, cycle)
		case prior.Deferred:
			return fmt.Errorf("%w: decision %q of %s/%s is already deferred", ErrExists, id, repository, taskID)
		}
		return putJSON(b, recordKey(taskID, id), d)
	})
}

// UndeferDecision lifts the deferral of decision id, receipt record decision-undefer/<id>: the record stays with
// Deferred false and its cycle one more than cycle, the cycle the caller read. A decision that is not deferred is
// ErrInvalid and a stored cycle other than cycle is ErrExists. It does not look at the decision, so a stale
// deferral can always be cleared.
func (s *Store) UndeferDecision(mutationID, repository, taskID, id string, cycle int) (Receipt, error) {
	if err := validateIDs(repository, taskID, id); err != nil {
		return Receipt{}, err
	}
	if cycle < 0 {
		return Receipt{}, invalid("deferral cycle %d must not be negative", cycle)
	}
	req := DecisionDeferral{Repository: repository, TaskID: taskID, Decision: id, Cycle: cycle}
	return s.mutate(mutationID, "undefer-decision", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, decisionUndeferRecord(id)
		b := repoBucket(tx, repository, bucketDecisionDeferrals)
		d, err := readDeferral(b, taskID, id)
		if err != nil {
			return err
		}
		switch {
		case !d.Deferred:
			return invalid("decision %q of %s/%s is not deferred", id, repository, taskID)
		case d.Cycle != cycle:
			return fmt.Errorf("%w: deferral of decision %q of %s/%s is at cycle %d, not %d", ErrExists, id, repository, taskID, d.Cycle, cycle)
		}
		d.Deferred, d.Cycle = false, cycle+1
		return putJSON(b, recordKey(taskID, id), d)
	})
}

// DecisionDeferral returns the deferral record of decision id, deferred or not, or ErrNotFound when it has none.
func (s *Store) DecisionDeferral(repository, taskID, id string) (DecisionDeferral, error) {
	var d DecisionDeferral
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketDecisionDeferrals), recordKey(taskID, id), &d)
	})
	if err != nil {
		return DecisionDeferral{}, fmt.Errorf("store: deferral of decision %q of %s/%s: %w", id, repository, taskID, err)
	}
	return d, nil
}

// readDeferral reads a deferral record from b, the zero record when there is none.
func readDeferral(b *bolt.Bucket, taskID, id string) (DecisionDeferral, error) {
	var d DecisionDeferral
	if err := getJSON(b, recordKey(taskID, id), &d); err != nil && !errors.Is(err, ErrNotFound) {
		return DecisionDeferral{}, err
	}
	return d, nil
}
