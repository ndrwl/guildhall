package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketHeartbeats = []byte("heartbeats")

type Heartbeat struct {
	Repository   string    `json:"repository"`
	TaskID       string    `json:"task_id"`
	Step         string    `json:"step"`
	Attempt      int       `json:"attempt"`
	Sequence     uint64    `json:"sequence"`
	Time         time.Time `json:"time"`
	WorkerState  string    `json:"worker_state"`
	LastOutputAt time.Time `json:"last_output_at"`
}

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
