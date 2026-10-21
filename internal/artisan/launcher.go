package artisan

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// # Stop ladder and precedence
//
// A Launcher supervises one started worker in its Unit and decides the
// attempt exactly once, from the attempt's files before anything else
// (docs/artisan.md#stop-ladder):
//
//  1. a cancel marker present means cancelled, and a result that appears
//     with it or after it is rejected as late;
//  2. otherwise a valid result means finished;
//  3. otherwise a stop writes the cancel marker, under the attempt lock and
//     only after checking again that no valid result is present, and the
//     attempt is cancelled; a worker that exits without either has failed.
//
// The decision is written once to OutcomeFile in the attempt directory,
// together with how the worker tree ended.
//
// Stopping climbs the ladder, each rung bounded by its Ladder duration and
// logged to EventsFile: rung 1 is the cancel marker the worker may honour,
// rung 2 Unit.Interrupt, rung 3 Unit.Terminate (Unix; skipped where refused
// with ErrUnsupportedRung), rung 4 Unit.Kill. A rung succeeds only when the
// unit is empty afterwards. A finished or failed attempt whose tree is still
// running starts at rung 2. On Windows rung 2 is CTRL_BREAK, which reaches
// only a worker sharing the launcher's console and which a worker may treat
// as fatal rather than graceful: it is not a dependable graceful rung, and
// its event says so.
//
// Every stop request, from the endpoint, from Stop or Cancel, or from SIGTERM
// or SIGHUP delivered to the launcher, joins the one ladder; concurrent
// stops wait for it and all answer with its end. An interrupt delivered to
// the launcher (Ctrl+C) is forwarded to the worker with Unit.Interrupt and
// does not stop the attempt by itself.

// Errors returned by a Launcher.
var (
	// ErrDecided refuses to run an attempt whose OutcomeFile already exists;
	// the worker is not started.
	ErrDecided = errors.New("attempt already has an outcome")
	// ErrNotEmpty reports that the last rung left contained processes alive;
	// the outcome lists them as survivors.
	ErrNotEmpty = errors.New("stop ladder ended with contained processes alive")
	// ErrLauncherRan refuses a second Run of one Launcher.
	ErrLauncherRan = errors.New("launcher already ran")
)

// Files the launcher writes in the attempt directory.
const (
	// OutcomeFile holds the attempt's one decision, written once.
	OutcomeFile = "outcome.json"
	// EventsFile is the launcher's append-only log, one JSON object a line.
	EventsFile = "events.jsonl"
	// LockFile is the attempt lock every decision is taken under.
	LockFile = "attempt.lock"
)

// OutcomeSchema identifies version 1 of the outcome file.
const OutcomeSchema = "guildhall.attempt-outcome/1"

// Ladder bounds how long each rung waits for the unit to empty.
type Ladder struct {
	Cooperative time.Duration
	Interrupt   time.Duration
	Terminate   time.Duration
	Kill        time.Duration
}

// DefaultLadder is the ladder a Launcher uses when its config gives none.
var DefaultLadder = Ladder{Cooperative: 3 * time.Second, Interrupt: 3 * time.Second, Terminate: 3 * time.Second, Kill: 5 * time.Second}

// DefaultResultGrace is how long a worker may keep running after its result
// is accepted before the launcher stops its tree.
const DefaultResultGrace = 3 * time.Second

// StopSignals are the signals the launcher's caller relays to Run:
// os.Interrupt is forwarded to the worker, SIGTERM and SIGHUP stop the
// attempt. Windows delivers SIGTERM for console close, logoff and shutdown.
var StopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// AttemptFiles is the attempt's result and cancel marker as the launcher
// consumes them. The launcher calls them only while holding the attempt lock.
type AttemptFiles interface {
	// ValidResult reports whether a valid result for this attempt is
	// published. A missing or invalid result is false with no error.
	ValidResult() (bool, error)
	// Marked reports whether the cancel marker is present.
	Marked() (bool, error)
	// Mark writes the cancel marker; marking a marked attempt changes nothing.
	Mark() error
}

// OutcomeState is the decision an attempt ended with.
type OutcomeState string

// Outcome states.
const (
	Finished  OutcomeState = "finished"
	Cancelled OutcomeState = "cancelled"
	Failed    OutcomeState = "failed"
)

// Outcome is the content of OutcomeFile.
type Outcome struct {
	Schema string       `json:"schema"`
	State  OutcomeState `json:"state"`
	Detail string       `json:"detail"`
	// LateResultRejected is set on a cancelled attempt whose result appeared
	// with or after the cancel marker.
	LateResultRejected bool `json:"late_result_rejected,omitempty"`
	// Rung is the rung that emptied the unit, 4 when survivors remain, and 0
	// when no ladder was needed.
	Rung int `json:"rung,omitempty"`
	// Survivors are the contained processes alive after the last rung.
	Survivors []Identity `json:"survivors,omitempty"`
}

// LauncherConfig configures a Launcher.
type LauncherConfig struct {
	// Dir is the existing attempt directory.
	Dir string
	// Unit is the unstarted unit the worker runs in; Run starts it and the
	// caller closes it after Run returns.
	Unit Unit
	// Files are the attempt's result and cancel marker.
	Files AttemptFiles
	// Ladder bounds the rungs; the zero Ladder means DefaultLadder.
	Ladder Ladder
	// ResultGrace is how long the worker may outlive its accepted result;
	// zero means DefaultResultGrace.
	ResultGrace time.Duration
}

// Launcher supervises one attempt's worker. It is the EndpointHandler of
// the attempt's control endpoint; its methods are safe for concurrent use.
type Launcher struct{}

var _ EndpointHandler = (*Launcher)(nil)

// NewLauncher returns the launcher for c, in phase "starting".
func NewLauncher(c LauncherConfig) (*Launcher, error) {
	return nil, errors.ErrUnsupported
}

// Run starts cmd in the unit and supervises it until the attempt is decided
// and the unit is empty or the ladder is exhausted, relaying signals as
// StopSignals describes. A stop requested before the worker starts cancels
// the attempt without starting it. Run returns the outcome it wrote; the
// error is ErrNotEmpty when survivors remain, ErrDecided when an outcome was
// already present, and joins any failure to write the outcome or an event.
func (l *Launcher) Run(cmd *exec.Cmd, signals <-chan os.Signal) (Outcome, error) {
	return Outcome{}, errors.ErrUnsupported
}

// Status returns the phase, "starting", "running", "stopping" or "ended",
// and the unit's members once the worker has started.
func (l *Launcher) Status() (EndpointState, error) {
	return EndpointState{}, errors.ErrUnsupported
}

// Stop requests the stop ladder and waits until Run has returned, answering
// with phase "ended" and Run's error. Every call, concurrent or later, shares
// the one ladder; the phase is "stopping" as soon as the request is made.
func (l *Launcher) Stop() (EndpointState, error) {
	return EndpointState{}, errors.ErrUnsupported
}

// Cancel takes rung 1 alone: it writes the cancel marker unless a valid
// result is present and returns without escalating; the attempt is
// cancelled once the worker honours the marker and exits.
func (l *Launcher) Cancel() (EndpointState, error) {
	return EndpointState{}, errors.ErrUnsupported
}
