package store

import (
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// RejectAttempt records a result-validation failure, with its reason, as a failed
// attempt. It accepts only unfinished or succeeded attempts. A distinct
// reject-attempt receipt preserves any launcher finish in the audit history;
// FinishAttempt's first-finish rule is unchanged. Ownership is released atomically.
// Mutation replay is idempotent; a changed request conflicts, and an unknown
// attempt is ErrNotFound. Invalid numbers, blank reasons and already failed or
// stopped attempts are ErrInvalid and write nothing.
func (s *Store) RejectAttempt(mutationID, repository, taskID, step string, number int, reason string) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	if number < 1 || strings.TrimSpace(reason) == "" {
		return Receipt{}, invalid("intake rejection needs a positive attempt number and nonempty reason")
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: AttemptFailed, IntakeRejection: reason}
	return s.mutate(mutationID, "reject-attempt", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = repository, taskID, number, attemptRecord(step, number)
		attempts := repoBucket(tx, repository, bucketAttempts)
		key := attemptKey(taskID, step, number)
		var a StageAttempt
		if err := getJSON(attempts, key, &a); err != nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, err)
		}
		if a.Outcome != "" && a.Outcome != AttemptSucceeded {
			return invalid("attempt %d of step %q already finished: %s", number, step, a.Outcome)
		}
		a.Outcome, a.IntakeRejection = AttemptFailed, reason
		if err := putJSON(attempts, key, a); err != nil {
			return err
		}
		return releaseOwnership(tx, repository, taskID, step, number)
	})
}
