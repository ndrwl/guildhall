package store

import (
	"bytes"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var bucketAttemptParks = []byte("attempt-parks")

// AttemptPark is the Director's record that a failed or stopped attempt is deliberately not retried yet
// (docs/decisions/0092-park-attempt.md). Parking never changes the attempt; status leaves a counting park's attempt
// out of needs_me. Unparking keeps the record with Parked false, so Cycle, the number of unparks so far, survives
// and the next park's mutation id differs from the last one's.
type AttemptPark struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	Reason     string `json:"reason"`
	Parked     bool   `json:"parked"`
	Cycle      int    `json:"cycle"`
}

// AttemptParkRecord and attemptUnparkRecord are the receipt record names of a park and an unpark of attempt number
// of step; status times a park by its receipt.
func AttemptParkRecord(step string, number int) string {
	return fmt.Sprintf("attempt-park/%s/%d", step, number)
}

func attemptUnparkRecord(step string, number int) string {
	return fmt.Sprintf("attempt-unpark/%s/%d", step, number)
}

// ParkAttempt parks attempt number of step, receipt record attempt-park/<step>/<number>, with the Director's reason.
// cycle is the park record's cycle the caller read, 0 when there was none. In the same transaction it refuses with
// ErrInvalid a blank or oversized reason, a closed task, the judge step, an attempt that is not the step's latest
// and one whose outcome is not failed or stopped; an unknown task or attempt is ErrNotFound. A stored cycle other
// than cycle, or an attempt already parked under another mutation id, is ErrExists.
func (s *Store) ParkAttempt(mutationID, repository, taskID, step string, number int, reason string, cycle int) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	switch {
	case number < 1:
		return Receipt{}, invalid("attempt number %d must be positive", number)
	case cycle < 0:
		return Receipt{}, invalid("park cycle %d must not be negative", cycle)
	case step == workflow.JudgeStep:
		return Receipt{}, invalid("a %s attempt is not parked", workflow.JudgeStep)
	}
	if err := validateText("reason", reason, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	p := AttemptPark{Repository: repository, TaskID: taskID, Step: step, Attempt: number, Reason: reason, Parked: true, Cycle: cycle}
	return s.mutate(mutationID, "park-attempt", p, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, AttemptParkRecord(step, number)
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		latest, err := latestAttempt(tx, repository, taskID, step)
		if err != nil {
			return err
		}
		switch {
		case number > latest.Number:
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, ErrNotFound)
		case number < latest.Number:
			return invalid("attempt %d of step %q is not the step's latest; attempt %d is", number, step, latest.Number)
		case latest.Outcome != AttemptFailed && latest.Outcome != AttemptStopped:
			return invalid("attempt %d of step %q is %s; only a failed or stopped attempt is parked", number, step, outcomeName(latest.Outcome))
		}
		b, err := createRepoBucket(tx, repository, bucketAttemptParks)
		if err != nil {
			return err
		}
		prior, err := readPark(b, taskID, step, number)
		if err != nil {
			return err
		}
		switch {
		case prior.Cycle != cycle:
			return fmt.Errorf("%w: park of attempt %d of %s/%s step %q is at cycle %d, not %d", ErrExists, number, repository, taskID, step, prior.Cycle, cycle)
		case prior.Parked:
			return fmt.Errorf("%w: attempt %d of %s/%s step %q is already parked", ErrExists, number, repository, taskID, step)
		}
		return putJSON(b, attemptKey(taskID, step, number), p)
	})
}

// UnparkAttempt unparks attempt number of step, receipt record attempt-unpark/<step>/<number>: the record stays with
// Parked false and its cycle one more than cycle, the cycle the caller read. An attempt that is not parked is
// ErrInvalid and a stored cycle other than cycle is ErrExists. It does not look at the attempt, so a stale park can
// always be cleared.
func (s *Store) UnparkAttempt(mutationID, repository, taskID, step string, number int, cycle int) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	if number < 1 || cycle < 0 {
		return Receipt{}, invalid("unpark needs a positive attempt number and a cycle that is not negative")
	}
	req := AttemptPark{Repository: repository, TaskID: taskID, Step: step, Attempt: number, Cycle: cycle}
	return s.mutate(mutationID, "unpark-attempt", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, attemptUnparkRecord(step, number)
		b := repoBucket(tx, repository, bucketAttemptParks)
		p, err := readPark(b, taskID, step, number)
		if err != nil {
			return err
		}
		switch {
		case !p.Parked:
			return invalid("attempt %d of %s/%s step %q is not parked", number, repository, taskID, step)
		case p.Cycle != cycle:
			return fmt.Errorf("%w: park of attempt %d of %s/%s step %q is at cycle %d, not %d", ErrExists, number, repository, taskID, step, p.Cycle, cycle)
		}
		p.Parked, p.Cycle = false, cycle+1
		return putJSON(b, attemptKey(taskID, step, number), p)
	})
}

// AttemptPark returns the park record of attempt number of step, parked or not, or ErrNotFound when it has none.
func (s *Store) AttemptPark(repository, taskID, step string, number int) (AttemptPark, error) {
	var p AttemptPark
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketAttemptParks), attemptKey(taskID, step, number), &p)
	})
	if err != nil {
		return AttemptPark{}, fmt.Errorf("store: park of attempt %d of %s/%s step %q: %w", number, repository, taskID, step, err)
	}
	return p, nil
}

// readPark reads a park record from b, the zero record when there is none.
func readPark(b *bolt.Bucket, taskID, step string, number int) (AttemptPark, error) {
	var p AttemptPark
	if err := getJSON(b, attemptKey(taskID, step, number), &p); err != nil && !errors.Is(err, ErrNotFound) {
		return AttemptPark{}, err
	}
	return p, nil
}

// latestAttempt returns the step's latest attempt, or ErrNotFound when the step has none.
func latestAttempt(tx *bolt.Tx, repository, taskID, step string) (StageAttempt, error) {
	var last StageAttempt
	if b := repoBucket(tx, repository, bucketAttempts); b != nil {
		prefix := recordKey(taskID, step, "")
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if err := decode(k, v, &last); err != nil {
				return StageAttempt{}, err
			}
		}
	}
	if last.Number == 0 {
		return StageAttempt{}, fmt.Errorf("attempts of %s/%s step %q: %w", repository, taskID, step, ErrNotFound)
	}
	return last, nil
}

func outcomeName(o AttemptOutcome) string {
	if o == "" {
		return "running"
	}
	return string(o)
}
