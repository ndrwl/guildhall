package artisan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
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
// Every stop request, from the endpoint's stop, from Stop, or from SIGTERM
// or SIGHUP delivered to the launcher, joins the one ladder; concurrent
// stops wait for it and all answer with its end. Cancel writes the marker
// and does not escalate. An interrupt delivered to
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
type Launcher struct {
	c        LauncherConfig
	ran      atomic.Bool
	stopOnce sync.Once
	stopReq  chan struct{}
	done     chan struct{}

	mu      sync.Mutex
	phase   string
	started bool
	err     error // Run's error, set before done closes
	logErr  error
}

var _ EndpointHandler = (*Launcher)(nil)

// pollInterval is how often Run looks for the result and the marker.
const pollInterval = 50 * time.Millisecond

// NewLauncher returns the launcher for c, in phase "starting".
func NewLauncher(c LauncherConfig) (*Launcher, error) {
	if c.Unit == nil || c.Files == nil {
		return nil, errors.New("artisan: launcher needs a unit and attempt files")
	}
	if fi, err := os.Stat(c.Dir); err != nil || !fi.IsDir() || !filepath.IsAbs(c.Dir) {
		return nil, fmt.Errorf("artisan: attempt directory %q must be an existing absolute directory: %v", c.Dir, err)
	}
	if c.Ladder == (Ladder{}) {
		c.Ladder = DefaultLadder
	}
	if c.ResultGrace == 0 {
		c.ResultGrace = DefaultResultGrace
	}
	return &Launcher{c: c, stopReq: make(chan struct{}), done: make(chan struct{}), phase: "starting"}, nil
}

// Run starts cmd in the unit and supervises it until the attempt is decided
// and the unit is empty or the ladder is exhausted, relaying signals as
// StopSignals describes. A stop requested, a cancel marker written or a
// valid result published before the worker starts decides the attempt
// without starting it. Run returns the outcome it wrote; the
// error is ErrNotEmpty when survivors remain, ErrDecided when an outcome was
// already present, and joins any failure to write the outcome or an event.
func (l *Launcher) Run(cmd *exec.Cmd, signals <-chan os.Signal) (Outcome, error) {
	if !l.ran.CompareAndSwap(false, true) {
		return Outcome{}, ErrLauncherRan
	}
	out, err := l.run(cmd, signals)
	l.mu.Lock()
	l.phase, l.err = "ended", errors.Join(err, l.logErr)
	err = l.err
	l.mu.Unlock()
	close(l.done)
	return out, err
}

func (l *Launcher) run(cmd *exec.Cmd, signals <-chan os.Signal) (Outcome, error) {
	if p := filepath.Join(l.c.Dir, OutcomeFile); fileExists(p) {
		return Outcome{}, fmt.Errorf("artisan: %w: %s", ErrDecided, p)
	}
	select {
	case <-l.stopReq:
		l.event("stop-before-start", nil)
		return l.stop(nil)
	default:
	}
	// A marker or result already present decides without starting a worker.
	if d, err := l.decide(false, ""); err != nil || d.State != "" {
		return l.finish(d, 0, nil, err)
	}
	worker, err := l.c.Unit.Start(cmd)
	if err != nil {
		d, derr := l.decide(false, "start worker: "+err.Error())
		return l.finish(d, 0, nil, derr)
	}
	l.mu.Lock()
	l.started = true
	if l.phase == "starting" {
		l.phase = "running"
	}
	l.mu.Unlock()
	l.event("running", map[string]any{"worker": worker})
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var decided *Outcome
	var grace <-chan time.Time
	var cooperative <-chan time.Time
	exited := l.c.Unit.Exited()
	for {
		select {
		case <-tick.C:
			if exited == nil && l.empty() {
				l.event("rung-emptied", map[string]any{"rung": 1})
				return l.finish(*decided, 1, nil, nil)
			}
			if decided != nil {
				continue
			}
			// A failed read is logged and polled again; only a stop or the
			// worker's exit turns it into an outcome.
			if d, err := l.decide(false, ""); err == nil && d.State != "" {
				decided = &d
				if d.State == Finished {
					l.event("result-accepted", nil)
					grace = time.After(l.c.ResultGrace)
				}
			}
		case <-grace:
			l.event("worker-outlived-result", nil)
			return l.ladder(*decided, 2, nil)
		case <-cooperative:
			cooperative = nil
			alive, aerr := l.c.Unit.Members()
			l.event("rung-timeout", map[string]any{"rung": 1, "alive": alive, "error": errString(aerr)})
		case <-exited:
			l.event("worker-exited", nil)
			d, err := l.decide(false, "worker exited without a result")
			if err == nil && d.State == Cancelled {
				if l.empty() {
					return l.finish(d, 0, nil, nil)
				}
				exited, decided, grace = nil, &d, nil
				l.event("ladder", map[string]any{"first": 1})
				l.event("rung", map[string]any{"rung": 1, "name": "cancel-marker", "deadline": l.c.Ladder.Cooperative.String()})
				cooperative = time.After(l.c.Ladder.Cooperative)
				continue
			}
			return l.ladder(d, 2, err)
		case <-l.stopReq:
			return l.stop(decided)
		case s := <-signals:
			if s == os.Interrupt {
				l.event("signal-forwarded", map[string]any{"signal": s.String(), "error": errString(l.c.Unit.Interrupt())})
				continue
			}
			l.event("signal", map[string]any{"signal": s.String()})
			l.requestStop()
			return l.stop(decided)
		}
	}
}

// decide applies the precedence rule under the attempt lock: a marker is
// cancelled, then a valid result is finished; otherwise mark cancels, and
// a non-empty fail fails. With neither the zero Outcome means undecided.
// Once OutcomeFile exists nothing is marked.
func (l *Launcher) decide(mark bool, fail string) (Outcome, error) {
	var d Outcome
	err := l.locked(func() error {
		if fileExists(filepath.Join(l.c.Dir, OutcomeFile)) {
			return nil
		}
		var err error
		d, err = l.decideLocked(mark, fail)
		return err
	})
	if err != nil {
		d = Outcome{State: Failed, Detail: "attempt files: " + err.Error()}
		l.event("decide-failed", map[string]any{"error": err.Error()})
	}
	return d, err
}

func (l *Launcher) decideLocked(mark bool, fail string) (Outcome, error) {
	if m, err := l.c.Files.Marked(); err != nil || m {
		return Outcome{State: Cancelled, Detail: "cancel marker present"}, err
	}
	if v, err := l.c.Files.ValidResult(); err != nil || v {
		return Outcome{State: Finished, Detail: "result accepted"}, err
	}
	if mark {
		l.event("cancel-marker", nil)
		return Outcome{State: Cancelled, Detail: "stopped"}, l.c.Files.Mark()
	}
	if fail != "" {
		return Outcome{State: Failed, Detail: fail}, nil
	}
	return Outcome{}, nil
}

// fileExists reports whether anything, readable or not, is at path.
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func (l *Launcher) locked(fn func() error) error {
	f, err := os.OpenFile(filepath.Join(l.c.Dir, LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// Closing the file releases the lock.
	defer func() { _ = f.Close() }()
	if err := lockFile(f); err != nil {
		return err
	}
	return fn()
}

// stop decides a stop: a cancelled attempt climbs from rung 1; a finished
// one is left to end within the result grace, then climbs from rung 2.
func (l *Launcher) stop(decided *Outcome) (Outcome, error) {
	l.setStopping()
	if decided == nil || decided.State == "" {
		d, err := l.decide(true, "")
		if err != nil {
			return l.ladder(d, 2, err)
		}
		decided = &d
	}
	if decided.State == Cancelled {
		return l.ladder(*decided, 1, nil)
	}
	l.event("stop-after-decision", map[string]any{"state": decided.State})
	if l.started {
		select {
		case <-l.c.Unit.Exited():
		case <-time.After(l.c.ResultGrace):
		}
	}
	return l.ladder(*decided, 2, nil)
}

// ladder climbs from rung first until the unit is empty, then finishes.
func (l *Launcher) ladder(d Outcome, first int, err error) (Outcome, error) {
	if !l.started || l.empty() {
		return l.finish(d, 0, nil, err)
	}
	l.event("ladder", map[string]any{"first": first})
	rungs := []struct {
		name string
		do   func() error
		wait time.Duration
	}{
		{"cancel-marker", func() error { return nil }, l.c.Ladder.Cooperative},
		{"interrupt", l.c.Unit.Interrupt, l.c.Ladder.Interrupt},
		{"terminate", l.c.Unit.Terminate, l.c.Ladder.Terminate},
		{"kill", l.c.Unit.Kill, l.c.Ladder.Kill},
	}
	for i, r := range rungs[first-1:] {
		n := first + i
		members, _ := l.c.Unit.Members()
		rerr := r.do()
		if errors.Is(rerr, ErrUnsupportedRung) {
			l.event("rung-skipped", map[string]any{"rung": n, "name": r.name, "error": rerr.Error()})
			continue
		}
		fields := map[string]any{"rung": n, "name": r.name, "members": members, "deadline": r.wait.String(), "error": errString(rerr)}
		if r.name == "interrupt" {
			fields["graceful"] = interruptGraceful
		}
		l.event("rung", fields)
		if l.waitEmpty(r.wait) {
			l.event("rung-emptied", map[string]any{"rung": n})
			return l.finish(d, n, nil, err)
		}
		alive, aerr := l.c.Unit.Members()
		l.event("rung-timeout", map[string]any{"rung": n, "alive": alive, "error": errString(aerr)})
	}
	alive, _ := l.c.Unit.Members()
	return l.finish(d, 4, alive, errors.Join(err, ErrNotEmpty))
}

// empty reports whether the unit is proven empty; a failed listing is not.
func (l *Launcher) empty() bool {
	m, err := l.c.Unit.Members()
	return err == nil && len(m) == 0
}

func (l *Launcher) waitEmpty(d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(25 * time.Millisecond) {
		if l.empty() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// finish records how the attempt ended in OutcomeFile, written once.
func (l *Launcher) finish(d Outcome, rung int, survivors []Identity, err error) (Outcome, error) {
	if werr := l.locked(func() error {
		final, derr := l.decideLocked(false, d.Detail)
		if derr != nil {
			err = errors.Join(err, derr)
			d = Outcome{State: Failed, Detail: "attempt files: " + derr.Error()}
		} else if final.State != "" {
			d = final
		}
		d.Schema, d.Rung, d.Survivors = OutcomeSchema, rung, survivors
		if d.State == Cancelled {
			var rerr error
			d.LateResultRejected, rerr = l.c.Files.ValidResult()
			err = errors.Join(err, rerr)
		}
		return writeOnce(filepath.Join(l.c.Dir, OutcomeFile), d)
	}); werr != nil {
		err = errors.Join(err, fmt.Errorf("artisan: write outcome: %w", werr))
	}
	l.event("ended", map[string]any{"outcome": d})
	return d, err
}

// writeOnce publishes v as JSON at path, refusing to replace a file there.
func writeOnce(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	return os.Link(tmp, path)
}

// event appends one line to EventsFile; the first failure is kept for Run.
func (l *Launcher) event(kind string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"], fields["at"] = kind, time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(fields)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		var f *os.File
		if f, err = os.OpenFile(filepath.Join(l.c.Dir, EventsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, err = f.Write(append(b, '\n'))
			err = errors.Join(err, f.Close())
		}
	}
	if err != nil && l.logErr == nil {
		l.logErr = fmt.Errorf("artisan: write event %s: %w", kind, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (l *Launcher) requestStop() { l.stopOnce.Do(func() { close(l.stopReq) }) }

func (l *Launcher) setStopping() {
	l.mu.Lock()
	if l.phase != "ended" {
		l.phase = "stopping"
	}
	l.mu.Unlock()
}

// Status returns the phase, "starting", "running", "stopping" or "ended",
// and the unit's members once the worker has started.
func (l *Launcher) Status() (EndpointState, error) {
	l.mu.Lock()
	st, started := EndpointState{Phase: l.phase}, l.started && l.phase != "ended"
	l.mu.Unlock()
	if started {
		m, err := l.c.Unit.Members()
		if err != nil {
			return st, err
		}
		st.Members = m
	}
	return st, nil
}

// Stop requests the stop ladder and waits until Run has returned, answering
// with phase "ended" and Run's error. Every call, concurrent or later, shares
// the one ladder; the phase is "stopping" as soon as the request is made.
func (l *Launcher) Stop() (EndpointState, error) {
	l.setStopping()
	l.requestStop()
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	return EndpointState{Phase: l.phase}, l.err
}

// Cancel takes rung 1 alone: it writes the cancel marker unless a valid
// result is present and returns without escalating; the attempt is
// cancelled once the worker honours the marker and exits.
func (l *Launcher) Cancel() (EndpointState, error) {
	_, err := l.decide(true, "")
	l.mu.Lock()
	defer l.mu.Unlock()
	return EndpointState{Phase: l.phase}, err
}
