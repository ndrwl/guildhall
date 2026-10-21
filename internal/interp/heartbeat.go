package interp

import (
	"errors"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
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
	if h.Key() != (artisan.AttemptKey{Repository: r.task.Repository, TaskID: r.task.ID, Step: step, Attempt: number}) {
		return nil
	}
	now := time.Now()
	if r.e.Now != nil {
		now = r.e.Now()
	}
	if h.Time.After(now) || now.Sub(h.Time) > interval || now.Sub(h.LastOutputAt) <= interval {
		return nil
	}
	output := h.LastOutputAt.UTC().Format(time.RFC3339Nano)
	return r.notify(fmt.Sprintf("worker-stalled.%s-%d.%s", step, number, digest.Of([]byte(output)).Hex()), "worker-stalled",
		fmt.Sprintf("Attempt %d of step %s has a fresh heartbeat but no output since %s.", number, step, output))
}
