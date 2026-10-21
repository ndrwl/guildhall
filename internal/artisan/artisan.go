// Package artisan is the per-attempt launcher that owns one Artisan worker
// process tree. This file holds the contract of its operating-system seams:
// process identity and the containment unit the launcher starts the worker in
// and stops it through (docs/artisan.md).
//
// # Identity
//
// An Identity is a PID plus a creation token that differs for a later process
// reusing the PID, so a recorded process is never confused with a stranger.
// Lookup reads the identity of a live process; Probe reports whether a
// recorded identity is still live. A process that has exited is Gone even
// while it is an unreaped zombie. A probe that fails for any reason other than
// absence, such as a permission error, is Unverified and never Gone.
//
// # Containment
//
// A Unit holds one worker and every descendant that stays inside it. Members
// lists the live contained processes by Identity; the unit is empty when
// Members returns none, and a stop rung (Interrupt, Terminate, Kill) counts as
// successful only when the unit is empty afterwards, never because the signal
// was delivered. The backends are:
//
//   - Windows: a named Job Object with only JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
//     (breakaway is not allowed), a handle the worker cannot inherit, the
//     worker created suspended, assigned, checked with IsProcessInJob and only
//     then resumed. Terminate is refused with ErrUnsupportedRung.
//   - Unix: the worker leads its own process group (and is the terminal's
//     foreground group when its stdin is the launcher's terminal). The leader
//     is never reaped before Close, so the group id cannot be reused while the
//     unit can signal it, and every signal is preceded by the leader check:
//     a live process at the group id that is not the recorded worker refuses
//     the signal with ErrGroupReused. Linux also sets a parent-death signal,
//     started from a locked thread.
//   - Linux with a delegated cgroup v2: the process group as above, and the
//     worker is created inside its own child cgroup guildhall-<name> of the
//     launcher's cgroup (CLONE_INTO_CGROUP). Members reads cgroup.procs, the
//     rungs signal every process in the cgroup and Kill writes cgroup.kill,
//     so a descendant that leaves the process group is still a member.
//   - Linux otherwise: the process group with the launcher as child
//     subreaper (PR_SET_CHILD_SUBREAPER), so a descendant orphaned outside
//     the group reparents to the launcher, where Escapes reports it.
//
// Which backend contains a unit is its Containment, and RecordContainment
// writes that to the attempt directory; on Linux the detection that chose it
// is part of the record.
//
// Limits are reported, not hidden: a Unix descendant that leaves the group
// (setsid, setpgid) is outside a process-group unit and survives Kill; Linux's
// parent-death signal reaches the direct child only; macOS has none.
package artisan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Errors returned by a Unit.
var (
	// ErrContainment reports that the unit could not be created or the worker's
	// membership could not be verified; the worker is killed and never reported
	// running.
	ErrContainment = errors.New("containment could not be established")
	// ErrUnsupportedRung refuses a stop rung the platform does not have; it
	// matches errors.ErrUnsupported.
	ErrUnsupportedRung = fmt.Errorf("stop rung not available on this platform: %w", errors.ErrUnsupported)
	// ErrGroupReused refuses a signal because the process group id names a
	// process other than the recorded worker.
	ErrGroupReused = errors.New("process group leader is not the recorded worker; not signalled")
	// ErrStarted refuses a second Start on one unit.
	ErrStarted = errors.New("unit already started")
	// ErrNotStarted refuses Members or a rung on a unit with no worker.
	ErrNotStarted = errors.New("unit not started")
	// ErrClosed refuses operations other than Close and Exited after successful cleanup.
	ErrClosed = errors.New("unit closed")
)

// Unit is the containment the launcher drives, one per attempt. Its methods
// are safe for concurrent use.
type Unit interface {
	// Start runs cmd inside the unit and returns the worker's identity once
	// membership is verified. It sets cmd.SysProcAttr. A failure after the
	// process was created kills it and returns ErrContainment.
	Start(cmd *exec.Cmd) (Identity, error)
	// Members lists the live contained processes. An empty list means the unit
	// is empty; a member whose identity cannot be read is an error, never
	// omitted. Before Start it returns ErrNotStarted.
	Members() ([]Identity, error)
	// Interrupt asks every member to stop (SIGINT to the group on Unix,
	// CTRL_BREAK to the worker's console group on Windows). Members may ignore it.
	// On Windows, no shared console means no delivery; after the worker exits,
	// Interrupt sends nothing and returns nil, even if descendants remain.
	Interrupt() error
	// Terminate sends SIGTERM to the group on Unix; on Windows it returns
	// ErrUnsupportedRung and changes nothing.
	Terminate() error
	// Kill ends every member without their cooperation.
	Kill() error
	// Exited is closed once the worker, the direct child, has exited.
	Exited() <-chan struct{}
	// Close kills any remaining members, waits for the worker to exit, then
	// releases the unit. A termination error returns without waiting for exit;
	// cleanup errors retain ownership so Close can be retried.
	// After success, Close is idempotent and Start, Members and stop rungs
	// return ErrClosed. Exited continues to return the worker's exit channel.
	Close() error
	// Containment says which backend contains the unit; it is fixed when the
	// unit is created.
	Containment() Containment
}

// NewUnit returns an unstarted unit of this platform's backend, named name
// where the backend names its containment (job Local\guildhall-<name>,
// cgroup guildhall-<name>); a name already in use is refused.
func NewUnit(name string) (Unit, error) { return newUnit(name) }

// Containment kinds.
const (
	// KindJob is a Windows Job Object.
	KindJob = "job"
	// KindCgroup is a Linux cgroup v2 child of the launcher's delegated cgroup.
	KindCgroup = "cgroup"
	// KindSubreaper is a Linux process group with the launcher as child
	// subreaper, used when no delegated cgroup v2 is available.
	KindSubreaper = "subreaper+pgroup"
	// KindGroup is a process group alone (macOS).
	KindGroup = "pgroup"
)

// Containment is the record of how a unit contains its worker.
type Containment struct {
	// Kind is one of the Kind constants.
	Kind string `json:"containment"`
	// Name is what a later sweep can find the unit by: the job name, or the
	// cgroup's directory. It is empty for a process group, which is found by
	// the recorded worker identity instead.
	Name string `json:"name,omitempty"`
	// Reason is the detection result that chose Kind on Linux, such as why
	// no delegated cgroup v2 was usable.
	Reason string `json:"reason,omitempty"`
}

// Contained lists the live processes still in the unit recorded as c, whose
// worker Start returned as worker, looked up from outside the launcher that
// created it, as reconcile does once that launcher is gone. A job is opened
// by its name and a cgroup read from its directory; a job that no longer
// opens, or a cgroup directory that no longer exists, holds nothing. A
// process group is found by its leader, the worker: a live process at the
// group id that is not worker means the group is gone and holds nothing. A
// kind this platform does not have is an error matching errors.ErrUnsupported.
// An empty list with a nil error proves the unit empty; an error proves nothing.
func Contained(c Containment, worker Identity) ([]Identity, error) {
	return contained(c, worker)
}

// ContainmentFile is the file RecordContainment writes in the attempt
// directory.
const ContainmentFile = "containment.json"

// RecordContainment writes c to ContainmentFile in the attempt directory dir,
// replacing any earlier record atomically.
func RecordContainment(dir string, c Containment) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	name := filepath.Join(dir, ContainmentFile)
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// Identity names one process: its PID plus an OS creation token (Linux: boot
// id and start time in clock ticks; Windows: creation time; macOS: start time).
type Identity struct {
	PID   int    `json:"pid"`
	Token string `json:"token"`
}

// Liveness is the result of probing an Identity.
type Liveness int

const (
	// Gone means no live process has this PID and token: proven exited.
	Gone Liveness = iota
	// Alive means a live process has this PID and token.
	Alive
	// Unverified means the probe failed for a reason other than absence.
	Unverified
)

func (l Liveness) String() string {
	switch l {
	case Gone:
		return "gone"
	case Alive:
		return "alive"
	default:
		return "unverified"
	}
}

// Self returns the calling process's identity.
func Self() (Identity, error) { return Lookup(os.Getpid()) }

// Probe reports whether id still names a live process.
func Probe(id Identity) (Liveness, error) {
	got, err := Lookup(id.PID)
	switch {
	case errors.Is(err, errNoProcess):
		return Gone, nil
	case err != nil:
		return Unverified, err
	case got.Token != id.Token:
		return Gone, nil
	}
	return Alive, nil
}

// errNoProcess is what Lookup returns, wrapped, when pid names no live process.
var errNoProcess = errors.New("no such live process")
