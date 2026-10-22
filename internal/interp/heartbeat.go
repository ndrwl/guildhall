package interp

import (
	"errors"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

func (r *run) workerStalled(step string, number int) error {
	interval := r.e.StallInterval
	if interval <= 0 {
		return nil
	}
	h, err := artisan.ReadHeartbeat(r.attemptDir(step, number))
	if errors.Is(err, artisan.ErrNoHeartbeat) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	if h.Key() != r.key(step, number) {
		return nil
	}
	now := r.now()
	if h.Time.After(now) || now.Sub(h.Time) > interval || now.Sub(h.LastOutputAt) <= interval {
		return nil
	}
	m, err := StalledMessage(h.Key(), h.LastOutputAt)
	if err != nil {
		return err
	}
	_, err = r.e.Outbox.Send(store.Recipient{Role: store.RecipientDirector}, m)
	return err
}

// StalledMessage is the worker-stalled Director message for attempt k silent since lastOutput: the engine's
// advance and the attempt's launcher both send it, so its id, derived from k and lastOutput as the engine's
// notify derives every id, makes the later of the two a replay (docs/interpreter.md#worker-stalled-observations).
func StalledMessage(k artisan.AttemptKey, lastOutput time.Time) (messaging.Envelope, error) {
	output := lastOutput.UTC().Format(time.RFC3339Nano)
	return directorMessage(k.Repository, k.TaskID, fmt.Sprintf("worker-stalled.%s-%d.%s", k.Step, k.Attempt, digest.Of([]byte(output)).Hex()), store.MessageWorkerStalled,
		fmt.Sprintf("Attempt %d of step %s has a fresh heartbeat but no output since %s.", k.Attempt, k.Step, output))
}

// now is the engine's clock.
func (r *run) now() time.Time {
	if r.e.Now != nil {
		return r.e.Now()
	}
	return time.Now()
}
