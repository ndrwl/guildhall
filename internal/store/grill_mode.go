package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var bucketGrillModes = []byte("grill_modes")

// Who sets a task's grill mode: the Director by its own judgement, or the Director acting on the Guildmaster's words
// (docs/tasks.md#grill-mode).
const (
	GrillModeByDirector    = "director"
	GrillModeByGuildmaster = "guildmaster"
)

// GrillMode is one record of a task's grill mode (docs/tasks.md#grill-mode): whether its grill is live or unattended,
// the one-line reason, who set it and when. Records are numbered per task from 1 and append-only; the latest is in
// effect (CurrentGrillMode), and a task with none is live.
type GrillMode struct {
	Repository string             `json:"repository"`
	TaskID     string             `json:"task_id"`
	Number     int                `json:"number"`
	Mode       workflow.GrillMode `json:"mode"`
	// Reason is the one-line reason for the mode; a live record may have none.
	Reason string `json:"reason,omitempty"`
	By     string `json:"by"`
	// AddedAt is the commit time of the record's receipt.
	AddedAt time.Time `json:"added_at"`
}

// grillModeRequest is what SetGrillMode is asked, which a replay must repeat exactly.
type grillModeRequest struct {
	Repository string             `json:"repository"`
	TaskID     string             `json:"task_id"`
	Mode       workflow.GrillMode `json:"mode"`
	Reason     string             `json:"reason"`
	By         string             `json:"by"`
	Prior      int                `json:"prior"`
}

// SetGrillMode records the task's next grill-mode record, receipt record grill-mode/<n>, on an open task. prior is
// how many records the task already has, so a command that did not see the latest one cannot stack a record on top
// of it: another count is refused with ErrExists. A mode other than live or unattended, a setter other than director
// or guildmaster, an unattended mode without a reason, a reason over MaxTextBytes or of more than one line, a task
// that is not open and the mode already in effect by a record are refused with ErrInvalid; an unknown task is
// ErrNotFound. The store does not judge the task's workflow or attempts, which guildhall task new and task grill do
// before they record.
func (s *Store) SetGrillMode(mutationID, repository, taskID string, mode workflow.GrillMode, reason, by string, prior int) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	switch {
	case !slices.Contains(workflow.GrillModes, mode):
		return Receipt{}, invalid("grill mode %q must be live or unattended", mode)
	case by != GrillModeByDirector && by != GrillModeByGuildmaster:
		return Receipt{}, invalid("grill mode set by %q must be director or guildmaster", by)
	case mode == workflow.GrillUnattended && strings.TrimSpace(reason) == "":
		return Receipt{}, invalid("an unattended grill needs a reason")
	case strings.ContainsAny(reason, "\r\n"):
		return Receipt{}, invalid("a grill mode's reason is one line")
	case prior < 0:
		return Receipt{}, invalid("grill mode prior count %d must not be negative", prior)
	}
	if reason != "" {
		if err := validateText("grill mode reason", reason, MaxTextBytes); err != nil {
			return Receipt{}, err
		}
	}
	req := grillModeRequest{Repository: repository, TaskID: taskID, Mode: mode, Reason: reason, By: by, Prior: prior}
	return s.mutate(mutationID, "set-grill-mode", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		b, err := createRepoBucket(tx, repository, bucketGrillModes)
		if err != nil {
			return err
		}
		var all []GrillMode
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		switch {
		case len(all) != prior:
			return fmt.Errorf("store: %w: task %s/%s has %d grill mode records, not %d", ErrExists, repository, taskID, len(all), prior)
		case len(all) > 0 && all[len(all)-1].Mode == mode:
			return invalid("task %s/%s's grill is already %s", repository, taskID, mode)
		}
		g := GrillMode{Repository: repository, TaskID: taskID, Number: len(all) + 1, Mode: mode, Reason: reason, By: by}
		// The record's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		g.AddedAt, rc.Record = rc.CommittedAt, GrillModeRecord(g.Number)
		return putJSON(b, grillModeKey(taskID, g.Number), g)
	})
}

// GrillModes lists a task's grill-mode records by number.
func (s *Store) GrillModes(repository, taskID string) ([]GrillMode, error) {
	return list[GrillMode](s, repository, bucketGrillModes, recordKey(taskID, ""))
}

// CurrentGrillMode returns the grill mode in effect given a task's records by number: the latest, or, for a task with
// none, live with number 0, no reason and no setter.
func CurrentGrillMode(records []GrillMode) GrillMode {
	if len(records) == 0 {
		return GrillMode{Mode: workflow.GrillLive}
	}
	return records[len(records)-1]
}

// Line is the grill mode as task show prints it: "grill: live" for a task with no record, else
// "grill: <mode> (<by>)", then ": <reason>" when it has one.
func (g GrillMode) Line() string {
	if g.Number == 0 {
		return "grill: " + string(workflow.GrillLive)
	}
	line := fmt.Sprintf("grill: %s (%s)", g.Mode, g.By)
	if g.Reason != "" {
		line += ": " + g.Reason
	}
	return line
}

// GrillModeRecord is the receipt record name of grill-mode record number.
func GrillModeRecord(number int) string { return "grill-mode/" + strconv.Itoa(number) }

// grillModeKey zero-pads the number so records sort numerically.
func grillModeKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
