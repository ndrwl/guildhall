package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/endpoint"
)

// The waiter operations (docs/decisions/director-wake-hook.md). A Claude Code Director session arms an async hook,
// `guildhall director wait-wake`, which polls the supervisor with OpWaitWake until it is handed a wake line; a
// sending command hands one with OpWake, counts only. OpRefresh tells the supervisor a waiter near its hook timeout
// is waking the Director with a counts-only refresh line.
const (
	OpWaitWake = "wait-wake"
	OpWake     = "wake"
	OpRefresh  = "refresh"
)

// ErrNoWaiter reports a wake no waiter was registered to take: the Director's session has no armed wait-wake hook.
var ErrNoWaiter = errors.New("no waiter is registered at the session endpoint")

// ErrRetired reports a waiter a newer registration has replaced; it exits 0 silently.
var ErrRetired = errors.New("a newer waiter has replaced this one")

// The waiter timings. Each is a variable only so tests can shorten them.
var (
	// PollHold is how long a wait-wake poll is held open before it is answered with nothing, so the waiter polls
	// again and the supervisor can tell a live waiter from a gone one.
	PollHold = 30 * time.Second
	// WaiterLease is how long after its last poll ended a waiter still counts as registered: the gap between two
	// polls of a live waiter is far shorter.
	WaiterLease = 5 * time.Second
)

// waiters is the registration of the one waiter of a session: the newest registered id, whether a poll of it is
// open, when its last poll ended, whether it has taken a wake line, and a wake line handed and not yet taken.
type waiters struct {
	mu      sync.Mutex
	id      string
	polls   int
	ended   time.Time
	spent   bool
	line    string
	has     bool
	wake    chan struct{}
	retire  chan struct{}
	handed  uint64
	refresh uint64
	closed  bool
}

// alive reports whether the newest waiter is registered: it has not taken a wake line, which ends it, and a poll
// of it is open or its last ended within WaiterLease. The caller holds mu.
func (w *waiters) alive(now time.Time) bool {
	return w.id != "" && !w.spent && (w.polls > 0 || now.Sub(w.ended) < WaiterLease)
}

// poll registers waiter id, retiring an older one, and waits up to hold for a wake line. It returns the line, ""
// with no error when hold passed with none, or ErrRetired when a newer waiter has replaced id. A line handed to an
// older waiter that it did not take is kept for id. A waiter that takes a line is spent: it wakes the Director and
// exits, so it no longer counts as registered, and a wake before the next waiter registers is ErrNoWaiter rather
// than a line no waiter takes.
func (w *waiters) poll(id string, hold time.Duration) (string, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return "", ErrRetired
	}
	if w.id != id {
		if w.retire != nil {
			close(w.retire)
		}
		w.id, w.retire = id, make(chan struct{})
		w.polls = 0
	}
	// A spent id polls again only after an orphaned server-side poll: the client's call context ended, but poll has
	// no context, so it went on to take a line the client never saw (recorded woke, handed++). The client then repolls
	// with the same id, and clearing spent registers it again. Nothing is lost: the message stays unread, and the
	// coming refresh wakes the Director with the unread counts, since Refresh accepts the newest id; any later wake
	// also reaches the re-registered waiter. No missed-wake notice is shown for that wake, because it was recorded
	// woke and not no waiter.
	w.spent = false
	if w.wake == nil {
		w.wake = make(chan struct{})
	}
	w.polls++
	wake, retire, has := w.wake, w.retire, w.has
	w.mu.Unlock()
	if !has {
		timer := time.NewTimer(hold)
		select {
		case <-wake:
		case <-retire:
		case <-timer.C:
		}
		timer.Stop()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.id != id {
		return "", ErrRetired
	}
	w.polls--
	w.ended = time.Now()
	if !w.has {
		return "", nil
	}
	line := w.line
	w.has, w.line, w.spent = false, "", true
	w.handed++
	return line, nil
}

// hand gives line to the registered waiter, replacing a line it has not taken yet, and reports whether one is
// registered to take it.
func (w *waiters) hand(line string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.alive(time.Now()) {
		return false
	}
	w.line, w.has = line, true
	if w.wake != nil {
		close(w.wake)
		w.wake = nil
	}
	return true
}

// release answers every open poll as retired, so Close need not wait out PollHold.
func (w *waiters) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.retire != nil {
		close(w.retire)
		w.retire = nil
	}
	w.id, w.closed = "", true
}

// WaiterState is what the supervisor reads of its session's waiter.
type WaiterState struct {
	// Registered is set while a waiter is registered.
	Registered bool
	// Handed counts the wake lines waiters have taken, and Refreshed the refreshes waiters have reported; each is
	// a wake of the Director the supervisor counts as activity.
	Handed, Refreshed uint64
}

// Waiter reads the session's waiter registration.
func (e *Endpoint) Waiter() WaiterState {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return WaiterState{Registered: e.w.alive(time.Now()), Handed: e.w.handed, Refreshed: e.w.refresh}
}

// Hand gives line, a counts-only wake line, to the session's registered waiter, which wakes the Director with it;
// with none registered it gives nothing and returns ErrNoWaiter.
func (e *Endpoint) Hand(line string) error {
	if !e.w.hand(line) {
		return ErrNoWaiter
	}
	return nil
}

// WaitWake is one poll of waiter id of session number, in the absolute session directory dir: it registers id,
// retiring an older waiter, and waits for a wake line. It returns the line, "" when the poll ended with none, so
// the waiter polls again, or ErrRetired once a newer waiter has replaced id. Nothing listening is
// ErrNoSupervisor. ctx bounds the dial and the wait.
func WaitWake(ctx context.Context, dir string, number int, id string) (string, error) {
	rep, err := call(ctx, dir, number, request{Op: OpWaitWake, Waiter: id})
	if err != nil {
		return "", err
	}
	if rep.Retired {
		return "", ErrRetired
	}
	return rep.Line, nil
}

// Wake hands line, a counts-only wake line, to the waiter of session number in the absolute session directory
// dir. A session with no registered waiter is ErrNoWaiter, and nothing listening ErrNoSupervisor.
func Wake(ctx context.Context, dir string, number int, line string) error {
	rep, err := call(ctx, dir, number, request{Op: OpWake, Line: line})
	if err != nil {
		return err
	}
	if !rep.Delivered {
		return ErrNoWaiter
	}
	return nil
}

// Refresh tells the supervisor of session number that waiter id is waking the Director with a refresh line before
// its hook times out; the supervisor counts it as a wake. A retired id is ErrRetired.
func Refresh(ctx context.Context, dir string, number int, id string) error {
	rep, err := call(ctx, dir, number, request{Op: OpRefresh, Waiter: id})
	if err != nil {
		return err
	}
	if rep.Retired {
		return ErrRetired
	}
	return nil
}

// call sends req to the supervisor of session number in dir and returns its reply.
func call(ctx context.Context, dir string, number int, req request) (reply, error) {
	p, err := EndpointPath(dir)
	if err != nil {
		return reply{}, err
	}
	req.Schema, req.Session = sessionProtocol.Current(), number
	rep, err := endpoint.Call(ctx, p, sessionProtocol, req, func(rep reply) bool { return rep.Session == number })
	switch {
	case errors.Is(err, endpoint.ErrNotListening):
		return rep, fmt.Errorf("session: %w: %w", ErrNoSupervisor, err)
	case errors.Is(err, ErrMismatch):
		return rep, fmt.Errorf("session: %w; it serves session %d, not %d", err, rep.Session, number)
	case err != nil:
		return rep, fmt.Errorf("session: %w", err)
	case rep.Error != "":
		return rep, fmt.Errorf("session: endpoint %s refused %s: %s", p, req.Op, rep.Error)
	}
	return rep, nil
}

// serveWaiter answers the waiter operations.
func (s signalServer) serveWaiter(req request) reply {
	rep := reply{Schema: sessionProtocol.Current(), Session: s.number}
	w := &s.e.w
	switch req.Op {
	case OpWaitWake:
		if req.Waiter == "" {
			return s.Refuse("wait-wake needs a waiter id")
		}
		line, err := w.poll(req.Waiter, PollHold)
		rep.Line, rep.Retired = line, errors.Is(err, ErrRetired)
	case OpWake:
		if req.Line == "" {
			return s.Refuse("wake needs a line")
		}
		rep.Delivered = w.hand(req.Line)
	case OpRefresh:
		w.mu.Lock()
		if w.id != req.Waiter {
			rep.Retired = true
		} else {
			w.refresh++
		}
		w.mu.Unlock()
	}
	return rep
}
