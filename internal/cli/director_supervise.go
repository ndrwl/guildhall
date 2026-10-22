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
	// Idle is set when a turn has ended since the newest wake typed into the Director's pane.
	Idle bool
	// Hook is set when the runtime signals the end of its turns and the session endpoint is bound.
	Hook bool
	// StageRunning is set when a Director stage attempt is running. It is read only for a plain reload.
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
//   - a plain reload ends it at a quiet point, with no Director stage attempt running and the Director idle, or,
//     once Grace has passed, with no stage running and no hook;
//   - a stop wins over a reload, and nothing else ends it.
func decideEnd(in endInput) endAction {
	afterGrace := in.Waited >= in.Grace
	turnOver := in.TurnEnded || afterGrace && (!in.Hook || in.Idle)
	switch {
	case in.Session.State == store.DirectorStopping && (in.Session.StopNow || turnOver):
		return endForStop
	case in.Session.State == store.DirectorStopping, in.Reload == nil || in.Reload.State != store.ReloadRequested:
		return keepRunning
	case in.Reload.Now && turnOver:
		return endForReload
	case !in.Reload.Now && !in.StageRunning && (in.Idle || afterGrace && !in.Hook):
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
		if err := endSession(storePath, n, "reloaded"); err != nil {
			return a.fail(fmt.Errorf("director start: record session %d reloaded: %w", n, err))
		}
		_, _ = fmt.Fprintf(a.Stdout, "session: %d reloaded\n", n)
		// A reload takes the copy's current settings.
		if p.cfg, err = admitConfig(copyDir, p.cfgPath); err != nil {
			return a.fail(fmt.Errorf("director start: reload: %w", err))
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
	if err := c.Start(); err != nil {
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err)), false
	}
	w := sessionWatch{storePath: storePath, state: state, n: n, grace: a.stopGrace(), hook: ep != nil && p.runtime.TurnEndHook()}
	if ep != nil {
		w.turnEnded = ep.TurnEnded()
	}
	ended, ending, err := a.waitSession(c, signals, &w)
	var exit *exec.ExitError
	switch {
	case ending == endForStop:
		_, _ = fmt.Fprintf(a.Stdout, "session: %d stopped by director stop\n", n)
		return 0, false
	case ended != nil:
		return a.fail(fmt.Errorf("director start: ended by %v", ended)), false
	case ending == endForReload || w.reloadWanted():
		return 0, true
	case errors.As(err, &exit):
		return a.fail(fmt.Errorf("director start: the runtime exited with status %d", exit.ExitCode())), false
	case err != nil:
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err)), false
	}
	return 0, false
}

// stopKill is how long a runtime the supervisor ended has to exit before it is killed.
const stopKill = 10 * time.Second

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
	grace            time.Duration
	hook             bool
	// turnEnded receives the session endpoint's turn-ended signals; nil without an endpoint.
	turnEnded <-chan struct{}

	// wanted is when an end now wanted was first seen, zero while none is.
	wanted time.Time
	// wakes is the newest wake sequence read, woke the newest wake that typed into the pane, and turnWake the
	// newest wake sequence when the last turn ended; turned is set once a turn has ended.
	wakes, woke, turnWake uint64
	turned                bool
}

// waitSession waits for the started runtime c to end, passing it each termination among signals, and returns the
// last termination passed on, if any, why the supervisor ended the runtime, if it did, and c's end.
// Every App.SessionPoll, and at each turn-ended signal, it decides with decideEnd whether to end the runtime;
// once it has, it kills the runtime if it has not exited stopKill later.
func (a *App) waitSession(c *exec.Cmd, signals <-chan os.Signal, w *sessionWatch) (os.Signal, endAction, error) {
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	poll := a.SessionPoll
	if poll <= 0 {
		poll = time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var ended os.Signal
	ending, endedAt := keepRunning, time.Time{}
	decide := func(turnEnded bool) {
		if ending != keepRunning {
			if time.Since(endedAt) >= stopKill {
				_ = c.Process.Kill()
			}
			return
		}
		if ending = w.decide(turnEnded); ending != keepRunning {
			endRuntime(c.Process)
			endedAt = time.Now()
		}
	}
	for {
		select {
		case s := <-signals:
			if terminate(c.Process, s) {
				ended = s
			}
		case <-w.turnEnded:
			decide(true)
		case <-tick.C:
			decide(false)
		case err := <-waited:
			return ended, ending, err
		}
	}
}

// decide reads the session, the pending reload and the wakes since the last read and applies decideEnd; a
// failed read keeps the runtime running until the next.
func (w *sessionWatch) decide(turnEnded bool) endAction {
	in, err := withStore(w.storePath, true, func(s *store.Store) (endInput, error) {
		in := endInput{TurnEnded: turnEnded, Hook: w.hook, Grace: w.grace}
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
					w.woke = wk.Sequence
				}
			}
		}
		if turnEnded {
			w.turned, w.turnWake = true, w.wakes
		}
		in.Idle = w.turned && w.woke <= w.turnWake
		if in.Reload != nil && !in.Reload.Now && (in.Idle || !w.hook) {
			starts, err := runningDirectorStages(s, w.state)
			in.StageRunning = len(starts) > 0
			return in, err
		}
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
		w.wanted = time.Now()
	}
	in.Waited = time.Since(w.wanted)
	return decideEnd(in)
}

// reloadWanted reports whether a runtime that ended by itself should be followed by a new session: a reload is
// requested and the session is not stopping.
func (w *sessionWatch) reloadWanted() bool {
	ok, err := withStore(w.storePath, true, func(s *store.Store) (bool, error) {
		d, err := s.DirectorSession(w.n)
		if err != nil || d.State == store.DirectorStopping {
			return false, err
		}
		r, err := s.LatestDirectorReload()
		return err == nil && r.State == store.ReloadRequested, nil
	})
	return err == nil && ok
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

// endSession records session n stopped with reason unless it already is stopped or a newer session superseded it.
func endSession(storePath string, n int, reason string) error {
	_, err := withStore(storePath, false, func(s *store.Store) (struct{}, error) {
		d, err := s.LatestDirectorSession()
		if err != nil || d.Number != n || d.State == store.DirectorStopped {
			return struct{}{}, err
		}
		_, err = s.EndDirectorSession(n, reason, d.StateSequence)
		return struct{}{}, err
	})
	return err
}
