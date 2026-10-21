package store

import (
	"fmt"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

var bucketFullReviews = []byte("full-reviews")

// FullReview is the Director's request that a task's next code review round review the whole head,
// never only a rebase's resolution (docs/tasks.md#full-review).
// Requests are numbered per task from 1; at most one is pending, and the round it forces consumes it.
type FullReview struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Number     int    `json:"number"`
	// ConsumedBy is the review attempt, <step>-<n>, the request made a full review; empty while pending.
	ConsumedBy string `json:"consumed_by,omitempty"`
}

// RequestFullReview records the next full-review request, receipt record full-review/<n>, on an open task.
// A request while another is pending is refused with ErrExists; an unknown task is ErrNotFound.
func (s *Store) RequestFullReview(mutationID, repository, taskID string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := FullReview{Repository: repository, TaskID: taskID}
	return s.mutate(mutationID, "request-full-review", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketFullReviews)
		if err != nil {
			return err
		}
		var all []FullReview
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		for _, f := range all {
			if f.ConsumedBy == "" {
				return fmt.Errorf("%w: full review %d of %s/%s is pending", ErrExists, f.Number, repository, taskID)
			}
		}
		req.Number = len(all) + 1
		rc.Record = fullReviewRecord(req.Number)
		return putJSON(b, fullReviewKey(taskID, req.Number), req)
	})
}

// ConsumeFullReview records that attempt by, <step>-<n>, consumed request number. A request is consumed once:
// consuming it again, by any attempt, is refused with ErrInvalid; an unknown request is ErrNotFound.
func (s *Store) ConsumeFullReview(mutationID, repository, taskID string, number int, by string) (Receipt, error) {
	if err := validateIDs(repository, taskID, by); err != nil {
		return Receipt{}, err
	}
	req := FullReview{Repository: repository, TaskID: taskID, Number: number, ConsumedBy: by}
	return s.mutate(mutationID, "consume-full-review", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, fullReviewRecord(number)
		key := fullReviewKey(taskID, number)
		var f FullReview
		if err := getJSON(repoBucket(tx, repository, bucketFullReviews), key, &f); err != nil {
			return fmt.Errorf("full review %d: %w", number, err)
		}
		if f.ConsumedBy != "" {
			return invalid("full review %d was already consumed by %s", number, f.ConsumedBy)
		}
		f.ConsumedBy = by
		return putJSON(repoBucket(tx, repository, bucketFullReviews), key, f)
	})
}

// FullReviews lists a task's full-review requests by number.
func (s *Store) FullReviews(repository, taskID string) ([]FullReview, error) {
	return list[FullReview](s, repository, bucketFullReviews, recordKey(taskID, ""))
}

func fullReviewRecord(number int) string { return "full-review/" + strconv.Itoa(number) }

// fullReviewKey zero-pads the number so requests sort numerically.
func fullReviewKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
