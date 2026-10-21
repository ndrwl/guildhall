package artisan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// # Heartbeat
//
// A running Launcher writes HeartbeatFile in the attempt directory once when
// Run begins and then every heartbeat interval from its own supervision loop,
// so a launcher whose loop is blocked stops beating even while its process
// and endpoint live (docs/artisan.md#heartbeat). Each write replaces the file
// atomically and increases Sequence, continuing from a heartbeat an earlier
// launcher of the attempt left. Beats pause while the stop ladder waits on a
// rung. A failed write is logged to EventsFile and retried on the next beat;
// it never ends the attempt. A heartbeat is evidence only: a stale one never
// proves the launcher exited.

// HeartbeatFile is the launcher's heartbeat in the attempt directory.
const HeartbeatFile = "heartbeat.json"

// DefaultHeartbeatInterval is how often a Launcher writes its heartbeat when
// its config gives no interval.
const DefaultHeartbeatInterval = 5 * time.Second

// Heartbeat is the content of HeartbeatFile. It is also the store's heartbeat
// record (store.Heartbeat), which the interpreter reads for worker-stalled.
type Heartbeat struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	// Sequence increases with every write for the attempt, from 1.
	Sequence uint64 `json:"sequence"`
	// Time is when the beat was written.
	Time time.Time `json:"time"`
	// WorkerState is the launcher's phase: starting, running or stopping.
	WorkerState string `json:"worker_state"`
	// LastOutputAt is the worker's last output as LauncherConfig.LastOutput
	// reports it, never after Time.
	LastOutputAt time.Time `json:"last_output_at"`
}

// Key returns the attempt the heartbeat names.
func (h Heartbeat) Key() AttemptKey {
	return AttemptKey{Repository: h.Repository, TaskID: h.TaskID, Step: h.Step, Attempt: h.Attempt}
}

// ReadHeartbeat reads HeartbeatFile in the attempt directory dir; a missing
// file is an error matching fs.ErrNotExist.
func ReadHeartbeat(dir string) (Heartbeat, error) {
	var h Heartbeat
	b, err := os.ReadFile(filepath.Join(dir, HeartbeatFile))
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(b, &h)
}
