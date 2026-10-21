package artisan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/strictjson"
)

// # Heartbeat
//
// A Launcher writes HeartbeatFile in the attempt directory once before Run
// starts the worker and then every heartbeat interval from its supervision
// loop, so a launcher whose loop is blocked stops beating even while its
// process and endpoint live (docs/artisan.md#heartbeat). Each write replaces
// the file atomically and increases Sequence, continuing from a heartbeat an
// earlier launcher of the attempt left. See docs/artisan.md#heartbeat for
// the supervision phases that continue or pause beats. A failed write is logged to
// EventsFile and retried on the next beat; it never ends the attempt. A
// heartbeat is evidence only: a stale one never proves the launcher exited.
// The file is an observation with no store record: reading or writing it
// never changes a record.

// HeartbeatFile is the launcher's heartbeat in the attempt directory.
const HeartbeatFile = "heartbeat.json"

// HeartbeatSchema is the schema HeartbeatFile carries.
const HeartbeatSchema = heartbeatSchema + "1"

const (
	heartbeatSchema   = "guildhall.attempt-heartbeat/"
	heartbeatVersion  = 1
	maxHeartbeatBytes = 4096
	// maxWorkerStateBytes bounds Heartbeat.WorkerState.
	maxWorkerStateBytes = 256
)

// DefaultHeartbeatInterval is how often a Launcher writes its heartbeat when
// its config gives no interval.
const DefaultHeartbeatInterval = 5 * time.Second

var (
	// ErrNoHeartbeat reports an attempt directory with no HeartbeatFile.
	ErrNoHeartbeat = errors.New("artisan: no heartbeat")
	// ErrInvalidHeartbeat reports a heartbeat that is malformed or breaks its
	// rules. A newer schema matches strictjson.ErrNewerSchema instead.
	ErrInvalidHeartbeat = errors.New("artisan: invalid heartbeat")
)

// Heartbeat is the content of HeartbeatFile, which reconcile and the
// interpreter's worker-stalled observation read.
type Heartbeat struct {
	// Schema is HeartbeatSchema; WriteHeartbeat sets it.
	Schema     string `json:"schema"`
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

func (h Heartbeat) validate() error {
	switch {
	case h.Repository == "" || h.TaskID == "" || h.Step == "" || h.Attempt < 1:
		return fmt.Errorf("%w: incomplete attempt %s", ErrInvalidHeartbeat, h.Key())
	case h.Sequence == 0:
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidHeartbeat)
	case h.Time.IsZero() || h.LastOutputAt.IsZero() || h.LastOutputAt.After(h.Time):
		return fmt.Errorf("%w: last output must be set and not after the beat", ErrInvalidHeartbeat)
	case strings.TrimSpace(h.WorkerState) == "" || len(h.WorkerState) > maxWorkerStateBytes || !utf8.ValidString(h.WorkerState):
		return fmt.Errorf("%w: worker state must be nonblank UTF-8 of at most %d bytes", ErrInvalidHeartbeat, maxWorkerStateBytes)
	}
	return nil
}

// WriteHeartbeat replaces HeartbeatFile in the attempt directory dir with h
// by one atomic rename. An invalid h is refused with ErrInvalidHeartbeat and
// nothing is written.
func WriteHeartbeat(dir string, h Heartbeat) error {
	if err := h.validate(); err != nil {
		return err
	}
	h.Schema = HeartbeatSchema
	return replaceJSON(filepath.Join(dir, HeartbeatFile), h)
}

// ReadHeartbeat reads HeartbeatFile in the attempt directory dir. A missing
// file is ErrNoHeartbeat, a newer schema matches strictjson.ErrNewerSchema,
// and anything else it cannot accept is ErrInvalidHeartbeat.
func ReadHeartbeat(dir string) (Heartbeat, error) {
	b, err := os.ReadFile(filepath.Join(dir, HeartbeatFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Heartbeat{}, fmt.Errorf("%w: %w", ErrNoHeartbeat, err)
	}
	if err != nil {
		return Heartbeat{}, err
	}
	var h Heartbeat
	err = strictjson.Decode(b, &h, strictjson.Options{Schema: heartbeatSchema, Version: heartbeatVersion, MaxBytes: maxHeartbeatBytes})
	if errors.Is(err, strictjson.ErrNewerSchema) {
		return Heartbeat{}, fmt.Errorf("artisan: heartbeat: %w", err)
	}
	if err != nil {
		return Heartbeat{}, fmt.Errorf("%w: %w", ErrInvalidHeartbeat, err)
	}
	return h, h.validate()
}
