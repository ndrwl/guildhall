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

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
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
// and does not escalate: surviving descendants remain cooperatively supervised
// after rung 1 times out, until they exit or an explicit stop requests escalation.
// Automatic signal dispatch checks the marker under the same attempt lock as
// Cancel, so an accepted cancellation prevents subsequent automatic signals.
// An interrupt delivered to
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
	// ResultFile is the adapter's result file, written by the worker.
	ResultFile = "result.json"
)

// OutcomeSchema identifies version 1 of the outcome file.
const OutcomeSchema = outcomeSchema + "1"

const (
	outcomeSchema  = "guildhall.attempt-outcome/"
	outcomeVersion = 1
	// maxOutcomeBytes bounds OutcomeFile; it only tells damage from a
	// document, so it is generous.
	maxOutcomeBytes = 1 << 20
)

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
// An error decides the attempt failed, so every adapter reads the result
// through the bounded publication-race retry (internal/atomicfile,
// docs/contracts/atomicfile.md) before returning an error to the launcher;
// artisantest.RunStopContract checks it on Windows.
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

// ErrInvalidOutcome reports an OutcomeFile that is malformed or names no
// outcome state. A newer schema matches strictjson.ErrNewerSchema instead.
var ErrInvalidOutcome = errors.New("artisan: invalid outcome")

// ReadOutcome reads OutcomeFile in the attempt directory dir. A missing file
// matches fs.ErrNotExist and a newer schema strictjson.ErrNewerSchema; a
// malformed file, or one naming no outcome state, is ErrInvalidOutcome.
func ReadOutcome(dir string) (Outcome, error) {
	b, err := atomicfile.Read(filepath.Join(dir, OutcomeFile))
	if err != nil {
		return Outcome{}, fmt.Errorf("artisan: outcome: %w", err)
	}
	var out Outcome
	err = strictjson.Decode(b, &out, strictjson.Options{Schema: outcomeSchema, Version: outcomeVersion, MaxBytes: maxOutcomeBytes})
	if errors.Is(err, strictjson.ErrNewerSchema) {
		return Outcome{}, fmt.Errorf("artisan: outcome: %w", err)
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %w", ErrInvalidOutcome, err)
	}
	switch out.State {
	case Finished, Cancelled, Failed:
		return out, nil
	}
	return Outcome{}, fmt.Errorf("%w: unknown state %q", ErrInvalidOutcome, out.State)
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
	// ResultTimeout bounds the result wait after Unit.Start returns, not setup
	// or ownership acquisition. Zero waits without a result deadline.
	ResultTimeout time.Duration
	// Key is the attempt the launcher owns, named by its heartbeat.
	Key AttemptKey
	// HeartbeatInterval is how often the heartbeat is written; zero means
	// DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// LastOutput reports when the worker last produced output, for the
	// heartbeat's LastOutputAt. Nil, or a zero time, reports the time Run
	// began; a time after the beat reports the beat's time.
	LastOutput func() time.Time
	// Beat, when set, is called with each heartbeat once it is written, from
	// Run's goroutine, so the supervision loop waits for it and it must return
	// promptly. A failed write calls nothing.
	Beat func(Heartbeat)
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

	// began and seq are the heartbeat's, used only by Run's goroutine.
	began time.Time
	seq   uint64
}

var _ EndpointHandler = (*Launcher)(nil)

// pollInterval is how often Run looks for the result and the marker.
const pollInterval = 50 * time.Millisecond

// NewLauncher returns the launcher for c, in phase "starting".
func NewLauncher(c LauncherConfig) (*Launcher, error) {
	if c.Unit == nil || c.Files == nil {
		return nil, errors.New("artisan: launcher needs a unit and attempt files")
	}
	if err := c.Key.validate(); err != nil {
		return nil, err
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
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
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
		l.event(eventStopBeforeStart, nil)
		return l.stop(nil)
	default:
	}
	// A marker or result already present decides without starting a worker.
	if d, err := l.decide(false, ""); err != nil || d.State != "" {
		return l.finish(d, 0, nil, err)
	}
	l.began = time.Now()
	if h, err := ReadHeartbeat(l.c.Dir); err == nil && h.Key() == l.c.Key {
		l.seq = h.Sequence
	}
	l.heartbeat()
	worker, err := l.c.Unit.Start(cmd)
	if err != nil {
		d, derr := l.decide(false, "start worker: "+err.Error())
		return l.finish(d, 0, nil, errors.Join(err, derr))
	}
	l.mu.Lock()
	l.started = true
	if l.phase == "starting" {
		l.phase = "running"
	}
	l.mu.Unlock()
	l.event(eventRunning, map[string]any{"worker": worker})
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	beat := time.NewTicker(l.c.HeartbeatInterval)
	defer beat.Stop()
	var resultDeadline <-chan time.Time
	if l.c.ResultTimeout > 0 {
		timer := time.NewTimer(l.c.ResultTimeout)
		defer timer.Stop()
		resultDeadline = timer.C
	}
	var decided *Outcome
	var grace <-chan time.Time
	for {
		select {
		case <-tick.C:
			if decided != nil {
				continue
			}
			// A failed read is logged and polled again; only a stop or the
			// worker's exit turns it into an outcome.
			if d, err := l.decide(false, ""); err == nil && d.State != "" {
				decided = &d
				if d.State == Finished {
					l.event(eventResultAccepted, nil)
					grace = time.After(l.c.ResultGrace)
				}
			}
		case <-beat.C:
			l.heartbeat()
		case <-grace:
			l.event(eventWorkerOutlivedResult, nil)
			return l.ladder(*decided, 2, nil, false, signals)
		case <-l.c.Unit.Exited():
			l.event(eventWorkerExited, nil)
			d, err := l.decide(false, "worker exited without a result")
			return l.ladder(d, 2, err, false, signals)
		case <-resultDeadline:
			return l.stop(decided)
		case <-l.stopReq:
			return l.stop(decided)
		case s := <-signals:
			if s == os.Interrupt {
				l.event(eventSignalForwarded, map[string]any{"signal": s.String(), "error": errString(l.c.Unit.Interrupt())})
				continue
			}
			l.event(eventSignal, map[string]any{"signal": s.String()})
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
		l.event(eventDecideFailed, map[string]any{"error": err.Error()})
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
		l.event(eventCancelMarker, nil)
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
	if err := lockFile(f, false); err != nil {
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
			return l.ladder(d, 2, err, true, nil)
		}
		decided = &d
	}
	if decided.State == Cancelled {
		return l.ladder(*decided, 1, nil, true, nil)
	}
	l.event(eventStopAfterDecision, map[string]any{"state": decided.State})
	if l.started {
		select {
		case <-l.c.Unit.Exited():
		case <-time.After(l.c.ResultGrace):
		}
	}
	return l.ladder(*decided, 2, nil, true, nil)
}

// ladder climbs from rung first until the unit is empty, then finishes.
// Automatic cleanup switches to cooperative supervision if cancellation wins
// the attempt lock before signal dispatch.
func (l *Launcher) ladder(d Outcome, first int, err error, explicit bool, signals <-chan os.Signal) (Outcome, error) {
	if !l.started {
		return l.finish(d, 0, nil, err)
	}
	if l.empty() {
		if first == 1 {
			// The marker emptied the tree before the first check.
			l.event(eventRungEmptied, map[string]any{"rung": 1})
			return l.finish(d, 1, nil, err)
		}
		return l.finish(d, 0, nil, err)
	}
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
		var rerr error
		if explicit {
			rerr = r.do()
		} else {
			lerr := l.locked(func() error {
				marked, merr := l.c.Files.Marked()
				if merr != nil {
					return merr
				}
				if marked {
					d = Outcome{State: Cancelled, Detail: "cancel marker present"}
					return nil
				}
				if d.State != Cancelled {
					rerr = r.do()
				}
				return nil
			})
			if lerr != nil {
				return l.finish(d, 4, members, errors.Join(err, lerr, ErrNotEmpty))
			}
			if d.State == Cancelled {
				return l.cooperate(d, signals, err)
			}
		}
		if i == 0 {
			l.event(eventLadder, map[string]any{"first": first})
		}
		if errors.Is(rerr, ErrUnsupportedRung) {
			l.event(eventRungSkipped, map[string]any{"rung": n, "name": r.name, "error": rerr.Error()})
			continue
		}
		fields := map[string]any{"rung": n, "name": r.name, "members": members, "deadline": r.wait.String(), "error": errString(rerr)}
		if r.name == "interrupt" {
			fields["graceful"] = interruptGraceful
		}
		l.event(eventRung, fields)
		if l.waitEmpty(r.wait) {
			l.event(eventRungEmptied, map[string]any{"rung": n})
			return l.finish(d, n, nil, err)
		}
		alive, aerr := l.c.Unit.Members()
		l.event(eventRungTimeout, map[string]any{"rung": n, "alive": alive, "error": errString(aerr)})
	}
	alive, _ := l.c.Unit.Members()
	return l.finish(d, 4, alive, errors.Join(err, ErrNotEmpty))
}

func (l *Launcher) cooperate(d Outcome, signals <-chan os.Signal, err error) (Outcome, error) {
	l.event(eventLadder, map[string]any{"first": 1})
	l.event(eventRung, map[string]any{"rung": 1, "name": "cancel-marker", "deadline": l.c.Ladder.Cooperative.String()})
	timer := time.NewTimer(l.c.Ladder.Cooperative)
	defer timer.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	beat := time.NewTicker(l.c.HeartbeatInterval)
	defer beat.Stop()
	for {
		select {
		case <-beat.C:
			l.heartbeat()
		case <-tick.C:
			if l.empty() {
				l.event(eventRungEmptied, map[string]any{"rung": 1})
				return l.finish(d, 1, nil, err)
			}
		case <-timer.C:
			alive, aerr := l.c.Unit.Members()
			l.event(eventRungTimeout, map[string]any{"rung": 1, "alive": alive, "error": errString(aerr)})
		case <-l.stopReq:
			out, stopErr := l.stop(&d)
			return out, errors.Join(err, stopErr)
		case s := <-signals:
			if s == os.Interrupt {
				l.event(eventSignalForwarded, map[string]any{"signal": s.String(), "error": errString(l.c.Unit.Interrupt())})
				continue
			}
			l.event(eventSignal, map[string]any{"signal": s.String()})
			l.requestStop()
			out, stopErr := l.stop(&d)
			return out, errors.Join(err, stopErr)
		}
	}
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
	l.event(eventEnded, map[string]any{"outcome": d})
	return d, err
}

// heartbeat writes the next beat to HeartbeatFile; a failed write is logged
// and the next beat tries again.
func (l *Launcher) heartbeat() {
	now := time.Now()
	out := l.began
	if l.c.LastOutput != nil {
		if t := l.c.LastOutput(); !t.IsZero() {
			out = t
		}
	}
	if out.After(now) {
		out = now
	}
	l.mu.Lock()
	phase := l.phase
	l.mu.Unlock()
	l.seq++
	k := l.c.Key
	h := Heartbeat{Repository: k.Repository, TaskID: k.TaskID, Step: k.Step, Attempt: k.Attempt,
		Sequence: l.seq, Time: now.UTC(), WorkerState: phase, LastOutputAt: out.UTC()}
	if err := WriteHeartbeat(l.c.Dir, h); err != nil {
		l.event(eventHeartbeatFailed, map[string]any{"sequence": l.seq, "error": err.Error()})
		return
	}
	if l.c.Beat != nil {
		h.Schema = HeartbeatSchema
		l.c.Beat(h)
	}
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
func (l *Launcher) event(kind eventName, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"], fields["at"] = kind.String(), time.Now().UTC().Format(time.RFC3339Nano)
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
