package artisan

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// This file holds what reconciling an attempt whose launcher is gone needs:
// killing one recorded process, sweeping a recorded unit from outside the
// launcher that created it, and deciding the attempt from its files
// (docs/artisan.md#resume).

// Kill ends the process id names, only while a live process still has its
// PID and token, so a reused PID is never signalled. A process that is gone,
// or whose PID now names another process, is not an error. It never matches
// a process by name. macOS checks the token and then signals, so a PID
// reused between the two is a limit there; Linux signals through a pidfd
// taken before the check and Windows through a handle taken before it.
func Kill(id Identity) error { return kill(id) }

// Sweep kills every process still in the unit recorded as c, whose worker
// Start returned as worker, from outside the launcher that created it, and
// returns the members it found. It is for an attempt whose launcher is proven
// gone: Sweep does not check ownership, and the caller must have proven the
// launcher exited first.
//
// A job is opened by its name and terminated; a cgroup is killed with
// cgroup.kill, and a cgroup found empty is removed. A process group is found
// as Contained finds it, by its leader, and each member is killed with Kill,
// so a live process at the group id that is not worker means the group is
// gone and nothing is signalled. A unit that no longer exists holds nothing.
// Descendants that left a process group (setsid) are not members and are not
// swept.
//
// Sweep proves nothing by itself: the unit is empty only when Contained
// returns no processes afterwards, so a caller repeats Sweep until it does.
// A kind this platform does not have is an error matching
// errors.ErrUnsupported.
func Sweep(c Containment, worker Identity) ([]Identity, error) { return sweep(c, worker) }

// Settle decides an attempt whose launcher is gone and whose unit is proven
// empty, from the attempt directory dir and its files, with the launcher's
// precedence: an existing OutcomeFile is returned as it is; otherwise a
// cancel marker is cancelled, with any valid result rejected as late; a valid
// result is finished; and anything else is failed with detail. A new outcome
// has rung 0, is written once to OutcomeFile and is logged as "settled" in
// EventsFile; an event that cannot be logged is not an error. Settle never
// writes the marker.
//
// The caller holds the attempt lock (LockAttempt) across Settle and whatever
// it records from the outcome. An OutcomeFile that cannot be read is
// returned as ReadOutcome's error and nothing is written.
func Settle(dir string, files AttemptFiles, detail string) (Outcome, error) {
	out, err := ReadOutcome(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return out, err
	}
	l := &Launcher{c: LauncherConfig{Dir: dir, Files: files}}
	d, err := l.decideLocked(false, detail)
	if err != nil {
		return Outcome{}, fmt.Errorf("artisan: settle: %w", err)
	}
	d.Schema = OutcomeSchema
	if d.State == Cancelled {
		if d.LateResultRejected, err = files.ValidResult(); err != nil {
			return Outcome{}, fmt.Errorf("artisan: settle: %w", err)
		}
	}
	if err := writeOnce(filepath.Join(dir, OutcomeFile), d); err != nil {
		return Outcome{}, fmt.Errorf("artisan: write outcome: %w", err)
	}
	l.event(eventSettled, map[string]any{"outcome": d})
	return d, nil
}
