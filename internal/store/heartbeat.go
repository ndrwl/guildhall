package store

import (
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/artisan"
)

var bucketHeartbeats = []byte("heartbeats")

// Heartbeat is an attempt's launcher heartbeat, the record the launcher
// writes to its attempt directory (artisan.HeartbeatFile), in the same shape.
type Heartbeat = artisan.Heartbeat

func (s *Store) Heartbeat(repository, taskID, step string, number int) (Heartbeat, error) {
	var h Heartbeat
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketHeartbeats), attemptKey(taskID, step, number), &h)
	})
	return h, err
}

func (s *Store) PutHeartbeat(mutationID string, h Heartbeat) (Receipt, error) {
	if err := validateIDs(h.Repository, h.TaskID, h.Step); err != nil {
		return Receipt{}, err
	}
	if h.Attempt < 1 || h.Sequence == 0 || h.Time.IsZero() || h.LastOutputAt.IsZero() || h.LastOutputAt.After(h.Time) {
		return Receipt{}, invalid("invalid heartbeat identity, sequence or time")
	}
	if err := validateText("worker state", h.WorkerState, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	return s.mutate(mutationID, "put-heartbeat", h, func(tx *bolt.Tx, rc *Receipt) error {
		key := attemptKey(h.TaskID, h.Step, h.Attempt)
		var a StageAttempt
		if err := getJSON(repoBucket(tx, h.Repository, bucketAttempts), key, &a); err != nil {
			return err
		}
		if a.Outcome != "" {
			return invalid("attempt is finished")
		}
		b, err := createRepoBucket(tx, h.Repository, bucketHeartbeats)
		if err != nil {
			return err
		}
		if b.Get(key) != nil {
			var previous Heartbeat
			if err := getJSON(b, key, &previous); err != nil {
				return err
			}
			if h.Sequence <= previous.Sequence {
				return invalid("heartbeat sequence must increase")
			}
		}
		rc.Repository, rc.TaskID, rc.Attempt = h.Repository, h.TaskID, h.Attempt
		rc.Record = fmt.Sprintf("heartbeat/%s/%d", h.Step, h.Attempt)
		return putJSON(b, key, h)
	})
}
