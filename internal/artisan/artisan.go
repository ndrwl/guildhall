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
//
// Limits are reported, not hidden: a Unix descendant that leaves the group
// (setsid, setpgid) is outside the unit and survives Kill; Linux's
// parent-death signal reaches the direct child only; macOS has none.
package artisan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
