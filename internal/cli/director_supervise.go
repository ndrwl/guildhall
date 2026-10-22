package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// endAction is what the session supervisor does about its running runtime at one moment.
type endAction int

const (
	// keepRunning leaves the runtime running.
	keepRunning endAction = iota
	// endForStop ends the runtime and records the session stopped.
	endForStop
	// endForReload ends the runtime and starts a new session in the same supervisor.
	endForReload
)

// endInput is what the supervisor knows when it decides, at each poll and at each turn-ended signal.
type endInput struct {
	// Session is the supervised session as last read.
	Session store.DirectorSession
	// Reload is the requested reload, or nil.
	Reload *store.DirectorReload
	// TurnEnded is set when the decision is made for a turn-ended signal that has just arrived.
	TurnEnded bool
	// Idle is set when a turn has ended since the last turn-started signal and the newest wake typed into the
	// Director's pane.
	Idle bool
	// Quiet is how long no turn signal and no wake has arrived, and IdleAfter how long that makes a runtime with a
	// hook idle.
	Quiet, IdleAfter time.Duration
	// Hook is set when the runtime signals the start and end of its turns and the session endpoint is bound.
	Hook bool
	// StageRunning is set when a Director stage attempt is running. It is read only for a plain reload that would
	// otherwise be applied.
	StageRunning bool
	// Waited is how long the end now decided on has been wanted, and Grace how long the end of a turn is
	// awaited without a hook.
	Waited, Grace time.Duration
}

// decideEnd is the supervisor's end-of-turn rule (docs/director.md#the-end-of-a-turn):
//
//   - a stop --now ends the runtime at once;
//   - a stop, or a reload --now, ends it at the end of the current turn: at a turn-ended signal or, once Grace
//     has passed, when the runtime has no hook or the Director is idle;
//   - a runtime with a hook is idle, too, once it has been Quiet for IdleAfter, such as after an interrupted turn;
//   - a plain reload ends it at a quiet point, with no Director stage attempt running and the Director idle, or,
//     once Grace has passed, with no stage running and no hook;
//   - a stop wins over a reload, and nothing else ends it.
func decideEnd(in endInput) endAction {
	afterGrace := in.Waited >= in.Grace
	idle := in.Idle || in.Hook && in.Quiet >= in.IdleAfter
	turnOver := in.TurnEnded || afterGrace && (!in.Hook || idle)
	switch {
	case in.Session.State == store.DirectorStopping && (in.Session.StopNow || turnOver):
		return endForStop
	case in.Session.State == store.DirectorStopping, in.Reload == nil || in.Reload.State != store.ReloadRequested:
		return keepRunning
	case in.Reload.Now && turnOver:
		return endForReload
	case !in.Reload.Now && !in.StageRunning && (idle || afterGrace && !in.Hook):
		return endForReload
	}
	return keepRunning
}

// superviseSession records a new session as recovering, renders it, runs the runtime in it as this process's
// child and records the session stopped when the runtime ends; when it ended for a reload, it records the session
// stopped as reloaded and starts the next session the same way. From before the first session is recorded until
// the last is recorded stopped the supervisor handles the session signals rather than dying of them.
func (a *App) superviseSession(p startPlan, copyDir, storePath, state string, pane store.DirectorPane) int {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, sessionSignals...)
	defer signal.Stop(signals)
	self, err := artisan.Self()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	if state, err = filepath.Abs(state); err != nil {
		return a.fail(err)
	}
	for {
		n, err := recordNextSession(storePath, self, p.cfg, pane)
		if err != nil {
			return a.fail(fmt.Errorf("director start: record session: %w", err))
		}
		code, reload := a.runSession(p, n, copyDir, storePath, state, signals)
		if !reload {
			if err := stopSession(storePath, n); err != nil {
				return a.fail(fmt.Errorf("director start: record session %d stopped: %w", n, err))
			}
			return code
		}
		stopped, err := reloadSession(storePath, n)
		if err != nil {
			return a.fail(fmt.Errorf("director start: record session %d reloaded: %w", n, err))
		}
		if stopped {
			_, _ = fmt.Fprintf(a.Stdout, "session: %d stopped by director stop\n", n)
			return 0
		}
		_, _ = fmt.Fprintf(a.Stdout, "session: %d reloaded\n", n)
		// A reload takes the copy's current settings, its runtime among them.
		if p.cfg, err = admitConfig(copyDir, p.cfgPath); err != nil {
			return a.fail(fmt.Errorf("director start: reload: %w", err))
		}
		if _, err := a.resolveRuntime(&p); err != nil {
			return a.fail(fmt.Errorf("director start: reload: runtime: %w", err))
		}
	}
}

// recordNextSession records the next session as recovering for supervisor self under cfg in pane and returns its
// number. A reload requested before it started is recorded applied by it.
func recordNextSession(storePath string, self artisan.Identity, cfg any, pane store.DirectorPane) (int, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return 0, err
	}
	return withStore(storePath, false, func(s *store.Store) (int, error) {
		d := store.DirectorSession{State: store.DirectorRecovering, Identity: store.ProcessIdentity(self),
			Release: Version(), ConfigDigest: string(digest.Of(data)), Pane: pane}
		newest, err := checkSupersede(s)
		if err != nil {
			return 0, err
		}
		d.Number = newest + 1
		if _, err := s.StartDirectorSession(d); err != nil {
			return 0, err
		}
		r, err := s.LatestDirectorReload()
		switch {
		case errors.Is(err, store.ErrNotFound):
			return d.Number, nil
		case err != nil:
			return 0, err
		case r.State == store.ReloadRequested:
			_, err = s.ApplyDirectorReload(r.Sequence, newest, d.Number)
		}
		return d.Number, err
	})
}

// runSession renders session n and runs its runtime to the end, returning the supervisor's exit status and
// whether the session ended for a reload. A signal received before the runtime starts ends the session with no
// runtime started.
func (a *App) runSession(p startPlan, n int, copyDir, storePath, state string, signals <-chan os.Signal) (int, bool) {
	status, err := readDirectorStatus(context.Background(), storePath, state, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director start: in-flight snapshot: %w", err)), false
	}
	var inFlight []string
	for _, it := range status.NeedsMe {
		inFlight = append(inFlight, fmt.Sprintf("%s: %s; run `%s`", it.Kind, oneLine(it.Reason), it.Command))
	}
	handoff, err := withStore(storePath, true, func(s *store.Store) (string, error) {
		note, err := s.LatestHandoffNote()
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return filepath.Join(state, "documents", filepath.FromSlash(note.Path)), err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director start: handoff note: %w", err)), false
	}
	cfgDir := filepath.Dir(p.cfgPath)
	role := baselineSource(filepath.Join(cfgDir, "baseline"), p.baseline, "roles/director.md")
	var model, effort string
	if d := p.cfg.Director; d != nil {
		if d.Role != "" {
			role = render.Source{Name: d.Role, Path: filepath.Join(cfgDir, filepath.FromSlash(d.Role)), Root: "copy"}
		}
		model, effort = d.Model, d.Effort
	}
	dir := filepath.Join(copyDir, "director", fmt.Sprintf("session-%d", n))
	r, err := render.Session(render.SessionInput{Dir: dir, Number: n, Role: role, SkillsDir: p.runtime.SkillsDir(),
		RuntimeFiles: p.runtime.SessionFiles(copyDir), InFlight: inFlight, Handoff: handoff}, []render.SkillRoot{{Name: "release", Dir: p.skills}})
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	cmd, err := p.runtime.Session(adapter.SessionSpec{Dir: dir, Copy: copyDir, RolePrompt: r.RolePath,
		FirstMessage: render.StartupMessage, Model: model, Effort: effort})
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	_, _ = fmt.Fprintf(a.Stdout, "session: %d (%s)\n", n, dir)
	ep, err := session.Listen(dir, n)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stdout, "session: %d has no turn-end endpoint, so it waits %s for a turn to end: %v\n", n, a.stopGrace(), err)
	} else {
		defer func() { _ = ep.Close() }()
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	c.Stdin, c.Stdout, c.Stderr = a.Stdin, a.Stdout, a.Stderr
	select {
	case s := <-signals:
		return a.fail(fmt.Errorf("director start: ended by %v before the runtime started", s)), false
	default:
	}
	// The terminal's interrupt reaches the runtime, which handles it; a termination is passed on to it. Either
	// way the supervisor outlives the runtime to record the end.
	u, err := startRuntime(c, n)
	if err != nil {
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err)), false
	}
	idleAfter, err := p.cfg.IdleAfter()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	w := sessionWatch{storePath: storePath, state: state, n: n, grace: a.stopGrace(), hook: ep != nil && p.runtime.TurnEndHook(),
		idleAfter: idleAfter, now: a.Now}
	if ep != nil {
		w.turns = ep.Turns()
	}
	ended, ending, err := a.waitSession(u, c, signals, &w)
	var exit *exec.ExitError
	switch {
	case errors.Is(err, errRuntimeOutlived):
		return a.fail(fmt.Errorf("director start: %w", err)), false
	case ending == endForStop:
		_, _ = fmt.Fprintf(a.Stdout, "session: %d stopped by director stop\n", n)
		return 0, false
	case ended != nil:
		return a.fail(fmt.Errorf("director start: ended by %v", ended)), false
	case ending == endForReload:
		return 0, true
	case errors.As(err, &exit):
		return a.fail(fmt.Errorf("director start: the runtime exited with status %d", exit.ExitCode())), false
	case err != nil:
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err)), false
	}
	return 0, false
}

// stopKill is how long a runtime the supervisor ended has to exit before it is killed, and how long the runtime's
// unit has to empty after the runtime's end.
const stopKill = 10 * time.Second

// runtimeUnit is the containment a session's runtime runs in, the operations of artisan.Unit the supervisor uses.
type runtimeUnit interface {
	Members() ([]artisan.Identity, error)
	Terminate() error
	Kill() error
	Exited() <-chan struct{}
	Close() error
}

// startRuntime starts c, the runtime of session n, in a unit of its own (artisan.NewSessionUnit), so the
// supervisor can end every process the runtime starts, such as its shell and tool children, with it, except the
// attempts' launchers, which leave it (artisan.SessionEnv).
func startRuntime(c *exec.Cmd, n int) (runtimeUnit, error) {
	u, err := artisan.NewSessionUnit(fmt.Sprintf("director-%d-session-%d", os.Getpid(), n))
	if err != nil {
		return nil, err
	}
	if c.Env == nil {
		c.Env = os.Environ()
	}
	// The attempts' launchers the runtime starts leave the unit, under their own ownership and stop.
	c.Env = append(c.Env, artisan.SessionEnv(u)...)
	if _, err := u.Start(c); err != nil {
		return nil, errors.Join(err, u.Close())
	}
	return u, nil
}

// errRuntimeOutlived is waitSession's error when the runtime's unit is not proven empty and released after the
// runtime's end: the session's processes may still run, whatever ended the runtime.
var errRuntimeOutlived = errors.New("the runtime's unit is not proven empty")

// empty kills whatever is left in the unit u of a runtime that has exited and waits until it is empty, reading its
// members every poll, for at most stopKill.
func empty(u runtimeUnit, poll time.Duration) error {
	for end := time.Now().Add(stopKill); ; time.Sleep(poll) {
		left, err := u.Members()
		if err == nil && len(left) == 0 {
			return nil
		}
		if time.Now().After(end) {
			if err != nil {
				return fmt.Errorf("the runtime's processes: %w", err)
			}
			return fmt.Errorf("%d of the runtime's processes outlived it, the first %d", len(left), left[0].PID)
		}
		_ = u.Kill()
	}
}

func (a *App) stopGrace() time.Duration {
	if a.StopGrace <= 0 {
		return 60 * time.Second
	}
	return a.StopGrace
}

// sessionWatch is what the supervisor of session n follows while its runtime runs.
type sessionWatch struct {
	storePath, state string
	n                int
	grace, idleAfter time.Duration
	hook             bool
	// handoffIdle is [director] handoff_idle, 0 for off, and pane the Director's pane the idle line is typed into.
	handoffIdle time.Duration
	pane        messaging.DirectorPane
	// now is the clock; nil means wall time.
	now func() time.Time
	// turns receives the session endpoint's turn signals; nil without an endpoint.
	turns <-chan session.Turn

	// wanted is when an end now wanted was first seen, zero while none is, and signalled when the newest turn
	// signal or wake arrived, or the first decision was made.
	wanted, signalled time.Time
	// wakes is the newest wake sequence read, woke the newest wake that typed into the pane, and turnWake the
	// newest wake sequence when the last turn ended; turned is set while the last turn signal is a turn end.
	wakes, woke, turnWake uint64
	turned                bool
}

// waitSession waits for the started runtime c, in unit u, to end, passing its unit each termination among signals,
// and returns the last termination passed on, if any, why the supervisor ended the runtime, if it did, and c's end.
// Every App.SessionPoll, and at each turn signal, it decides with decideEnd whether to end the runtime's unit;
// once it has, it kills the unit if the runtime has not exited stopKill later. However the runtime ends, it returns
// only once the unit is empty: whatever the runtime left running is killed, and a unit not proven empty stopKill
// after the runtime's end is an error.
func (a *App) waitSession(u runtimeUnit, c *exec.Cmd, signals <-chan os.Signal, w *sessionWatch) (os.Signal, endAction, error) {
	poll := a.SessionPoll
	if poll <= 0 {
		poll = time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var ended os.Signal
	ending, endedAt := keepRunning, time.Time{}
	decide := func(turn session.Turn) {
		if ending != keepRunning {
			if time.Since(endedAt) >= stopKill {
				_ = u.Kill()
			}
			return
		}
		if ending = w.decide(turn); ending != keepRunning {
			endRuntime(u)
			endedAt = time.Now()
		}
	}
	for {
		select {
		case s := <-signals:
			if terminate(u, s) {
				ended = s
			}
		case t := <-w.turns:
			decide(t)
		case <-tick.C:
			decide("")
		case <-u.Exited():
			err := errors.Join(empty(u, poll), u.Close())
			if err != nil {
				err = fmt.Errorf("%w: %w", errRuntimeOutlived, err)
			} else if ps := c.ProcessState; ps != nil && !ps.Success() {
				err = &exec.ExitError{ProcessState: ps}
			}
			return ended, ending, err
		}
	}
}

// decide reads the session, the pending reload and the wakes since the last read and applies decideEnd for turn,
// the turn signal that has just arrived or none; a failed read keeps the runtime running until the next.
func (w *sessionWatch) decide(turn session.Turn) endAction {
	now := time.Now()
	if w.now != nil {
		now = w.now()
	}
	if turn != "" || w.signalled.IsZero() {
		w.signalled = now
	}
	switch turn {
	case session.TurnStarted:
		w.turned = false
	case session.TurnEnded:
		w.turned = true
	}
	in, err := withStore(w.storePath, true, func(s *store.Store) (endInput, error) {
		in := endInput{TurnEnded: turn == session.TurnEnded, Hook: w.hook, Grace: w.grace, IdleAfter: w.idleAfter}
		var err error
		if in.Session, err = s.DirectorSession(w.n); err != nil {
			return in, err
		}
		r, err := s.LatestDirectorReload()
		if err == nil && r.State == store.ReloadRequested {
			in.Reload = &r
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return in, err
		}
		for more := true; more; {
			var ws []store.Wake
			if ws, more, err = s.Wakes(w.wakes, store.MaxInboxLimit); err != nil {
				return in, err
			}
			for _, wk := range ws {
				w.wakes = wk.Sequence
				if wk.Decision == store.WakeWoke {
					w.woke, w.signalled = wk.Sequence, now
				}
			}
		}
		if in.TurnEnded {
			w.turnWake = w.wakes
		}
		in.Idle = w.turned && w.woke <= w.turnWake
		in.Quiet = now.Sub(w.signalled)
		return in, nil
	})
	if err != nil {
		return keepRunning
	}
	if in.Session.State != store.DirectorStopping && in.Reload == nil {
		w.wanted = time.Time{}
		return keepRunning
	}
	if w.wanted.IsZero() {
		w.wanted = now
	}
	in.Waited = now.Sub(w.wanted)
	act := decideEnd(in)
	if act != endForReload || in.Reload.Now {
		return act
	}
	running, err := withStore(w.storePath, true, func(s *store.Store) (bool, error) {
		starts, err := runningDirectorStages(s, w.state)
		return len(starts) > 0, err
	})
	if err != nil {
		return keepRunning
	}
	in.StageRunning = running
	return decideEnd(in)
}

// stopSession records session n stopped unless it already is or a newer session superseded it.
func stopSession(storePath string, n int) error {
	_, err := withStore(storePath, false, func(s *store.Store) (struct{}, error) {
		d, err := s.LatestDirectorSession()
		if err != nil || d.Number != n || d.State == store.DirectorStopped {
			return struct{}{}, err
		}
		_, err = s.SetDirectorState(n, store.DirectorStopped, d.StateSequence)
		return struct{}{}, err
	})
	return err
}

// reloadSession records session n stopped as reloaded unless it already is stopped or a newer session superseded
// it. A session a director stop has made stopping meanwhile is recorded stopped as a stop does instead, and
// reported stopped, since a stop wins over a reload.
func reloadSession(storePath string, n int) (bool, error) {
	return withStore(storePath, false, func(s *store.Store) (bool, error) {
		d, err := s.LatestDirectorSession()
		switch {
		case err != nil || d.Number != n || d.State == store.DirectorStopped:
			return false, err
		case d.State == store.DirectorStopping:
			_, err = s.SetDirectorState(n, store.DirectorStopped, d.StateSequence)
			return true, err
		}
		_, err = s.EndDirectorSession(n, "reloaded", d.StateSequence)
		return false, err
	})
}
