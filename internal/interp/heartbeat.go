package interp

import (
	"errors"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

func (r *run) workerStalled(step string, number int) error {
	interval := r.e.StallInterval
	if interval <= 0 {
		return nil
	}
	h, err := r.e.Records.Heartbeat(r.task.Repository, r.task.ID, step, number)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
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
