package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
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
		base := store.DirectorSession{Pane: pane, Adapter: p.name}
		n, sp, err := recordNextSession(storePath, self, p.cfg, base, copyDir, func(n int) (store.HandoffPolicy, error) {
			return handoffPolicy(p.cfg, p.runtime, sessionDir(copyDir, n))
		})
		if err != nil {
			return a.fail(fmt.Errorf("director start: record session: %w", err))
		}
		a.warnUnremoved(sp)
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
		if err := a.preflightSkillPolicies(&p); err != nil {
			return a.fail(fmt.Errorf("director start: reload: skills: %w", err))
		}
	}
}

// recordNextSession records the next session as recovering for supervisor self under cfg, in base's pane and
// with its runtime adapter, its directory in copyDir and the handoff policy policy gives for its number, and
// returns its number beside what checkSupersede found of the session it supersedes, whose failed removal the
// caller warns of. A reload requested before it started is recorded applied by it.
func recordNextSession(storePath string, self artisan.Identity, cfg any, base store.DirectorSession, copyDir string,
	policy func(n int) (store.HandoffPolicy, error)) (int, superseded, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return 0, superseded{}, err
	}
	if copyDir, err = filepath.Abs(copyDir); err != nil {
		return 0, superseded{}, err
	}
	var sp superseded
	n, err := withStore(storePath, false, func(s *store.Store) (int, error) {
		d := store.DirectorSession{State: store.DirectorRecovering, Identity: store.ProcessIdentity(self),
			Release: Version(), ConfigDigest: string(digest.Of(data)), Pane: base.Pane, Adapter: base.Adapter}
		var err error
		if sp, err = checkSupersede(s); err != nil {
			return 0, err
		}
		newest := sp.Number
		d.Number = newest + 1
		d.Dir = sessionDir(copyDir, d.Number)
		if d.Handoff, err = policy(d.Number); err != nil {
			return 0, err
		}
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
	return n, sp, err
}

// runSession renders session n and runs its runtime to the end, returning the supervisor's exit status and
// whether the session ended for a reload. A signal received before the runtime starts ends the session with no
// runtime started.
func (a *App) runSession(p startPlan, n int, copyDir, storePath, state string, signals <-chan os.Signal) (int, bool) {
	// Every session, the first and each reload's, starts with a workspace close sweep, so a Herdr workspace that
	// became due while no supervisor ran is closed; the session's end waits for a running sweep.
	sweep, err := a.workspaceCloseSweep(p.cfg, storePath, state)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	closes := &workspaceCloseWatch{every: workspaceCloseEvery, round: sweep.run}
	closes.tick(time.Now())
	defer closes.wait()
	status, err := readDirectorStatus(context.Background(), storePath, state, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director start: in-flight snapshot: %w", err)), false
	}
	var inFlight []string
	for _, it := range status.NeedsMe {
		if it.Kind == checksKind {
			inFlight = append(inFlight, fmt.Sprintf("%s: %s; the session supervisor advances it", it.Kind, oneLine(it.Reason)))
			continue
		}
		inFlight = append(inFlight, fmt.Sprintf("%s: %s; run `%s`", it.Kind, oneLine(it.Reason), it.Command))
	}
	// Parked attempts come from the same status read, so what a session is told cannot drift from the feed (D71).
	var parked []string
	for _, p := range status.Parked {
		parked = append(parked, fmt.Sprintf("parked: attempt %d of %s of %s/%s %s; reason: %s; retry with `%s`",
			p.Attempt, p.Step, p.Repository, p.TaskID, p.Outcome, oneLine(p.Reason), p.Command))
	}
	// Every live note goes to the new session; the old handoff note is not shown (D94).
	type startupNotes struct {
		lines []string
		bytes int
	}
	notes, err := withStore(storePath, true, func(s *store.Store) (startupNotes, error) {
		lines, size, err := liveNoteLines(s, a.now())
		return startupNotes{lines, size}, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director start: notes: %w", err)), false
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
	dir := sessionDir(copyDir, n)
	r, err := render.Session(render.SessionInput{Dir: dir, Number: n, Role: role, SkillsDir: p.runtime.SkillsDir(),
		RuntimeFiles: p.runtime.SessionFiles(copyDir), InFlight: inFlight, Parked: parked, Notes: notes.lines, NotesBytes: notes.bytes}, []render.SkillRoot{{Name: "release", Dir: p.skills}})
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	workDir, firstMessage := dir, render.StartupMessage
	if persistent, ok := p.runtime.(runtimes.PersistentSession); ok {
		workDir, err = persistent.PrepareSessionWorkspace(copyDir, dir)
		if err != nil {
			return a.fail(fmt.Errorf("director start: prepare workspace: %w", err)), false
		}
		// Claude Code, the one persistent runtime, shows the binary's version first with its /version command,
		// which then reads startup.md by its absolute path.
		firstMessage = claude.StartupMessage(r.StartupPath)
	}
	cmd, err := p.runtime.Session(adapter.SessionSpec{Dir: workDir, Copy: copyDir, RolePrompt: r.RolePath,
		FirstMessage: firstMessage, Model: model, Effort: p.cfg.EffectiveEffort(effort), Env: []string{session.NumberEnv + "=" + strconv.Itoa(n)}})
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
	checks, err := a.watchChecks(p, n, dir, copyDir, storePath, state)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	deps, err := a.watchDependencies(p, n, dir, copyDir, storePath, state)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	queue, err := a.watchMergeQueue(p, n, dir, copyDir, storePath, state)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	starts, err := a.watchStartQueue(p, n, dir, copyDir, storePath, state)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	u, err := startRuntime(c, n, storePath)
	if err != nil {
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err)), false
	}
	idleAfter, err := p.cfg.IdleAfter()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	triggers, err := p.cfg.Handoff()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	quiet, err := p.cfg.WakeQuiet()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	maxDelay, err := p.cfg.WakeMaxDelay()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err)), false
	}
	w := sessionWatch{copyDir: copyDir, storePath: storePath, state: state, n: n, grace: a.stopGrace(), hook: ep != nil && p.runtime.TurnEndHook(),
		idleAfter: idleAfter, handoffIdle: triggers.Idle, pane: a.directorPane(), now: a.Now, checks: checks, deps: deps, queue: queue, starts: starts, closes: closes,
		quiet: quiet, maxDelay: maxDelay}
	if ep != nil {
		w.turns = ep.Turns()
		// A session that is not typed into is woken through its waiter, with the notice in its pane while none is
		// registered (docs/decisions/director-wake-hook.md), and a burst's message wakes are held to wake it once.
		if p.name != store.TypedWakeAdapter {
			ep.SetHold(quiet > 0)
			w.waiter = ep
			if d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.DirectorSession(n) }); err == nil && d.Pane != (store.DirectorPane{}) {
				w.notice = a.wakeNotice(d.Pane)
			}
		}
	}
	ended, ending, err := a.waitSession(u, c, signals, &w)
	w.setNotice("")
	checks.wait()
	deps.wait()
	queue.wait()
	starts.wait()
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
// attempts' launchers, which leave it (artisan.SessionEnv). The runtime stays gated or suspended until its unit is
// recorded on session n in the store at storePath, so director stop can sweep it once the supervisor is gone;
// a failed record ends the runtime with its unit without letting its program execute.
func startRuntime(c *exec.Cmd, n int, storePath string) (runtimeUnit, error) {
	// Session numbers are store-local; one supervisor process can serve multiple stores.
	u, err := artisan.NewSessionUnit(fmt.Sprintf("director-%d-session-%d-%s", os.Getpid(), n, rand.Text()))
	if err != nil {
		return nil, err
	}
	if c.Env == nil {
		c.Env = os.Environ()
	}
	// The attempts' launchers the runtime starts leave the unit, under their own ownership and stop.
	c.Env = append(c.Env, artisan.SessionEnv(u)...)
	_, err = artisan.StartRecorded(u, c, func(id artisan.Identity) error {
		uc := u.Containment()
		rec := store.Containment{Kind: uc.Kind, Name: uc.Name, Reason: uc.Reason, Worker: store.ProcessIdentity(id)}
		_, err := withStore(storePath, false, func(s *store.Store) (store.Receipt, error) { return s.RecordDirectorRuntime(n, rec) })
		return err
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start recorded runtime: %w", err), u.Close())
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
	copyDir          string
	labelsAt         time.Time
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
	// checks is the session's checks watcher, ticked at every poll; nil without one.
	checks *checksWatch
	// deps is the session's dependency watcher, a second checks watcher of the tasks the dependency gate holds,
	// ticked with checks; nil without one.
	deps *checksWatch
	// queue is the session's merge-queue watcher, a third checks watcher of the tasks waiting in a merge queue,
	// ticked with checks; nil without one.
	queue *checksWatch
	// starts is the session's start-queue watcher, a fourth checks watcher, which drains the start queue, ticked
	// with checks; nil without one.
	starts *checksWatch
	// closes is the session's workspace close sweep, ticked at every poll; nil without one.
	closes *workspaceCloseWatch

	// wanted is when an end now wanted was first seen, zero while none is, and signalled when the newest turn
	// signal or wake arrived, or the first decision was made.
	wanted, signalled time.Time
	// idled is the signalled time of the quiet period the idle line was last typed in.
	idled time.Time
	// wakes is the newest wake sequence read, woke the newest wake that typed into the pane, and turnWake the
	// newest wake sequence when the last turn ended; turned is set while the last turn signal is a turn end.
	wakes, woke, turnWake uint64
	turned                bool

	// waiter is the session endpoint's waiter registration, nil without an endpoint, and notice the wake notice
	// outside the input box, nil for none (docs/decisions/director-wake-hook.md).
	waiter waiterSource
	notice wakeNotice
	// handed is the waiter's count of wakes taken and refreshes when last read. missed is set while a wake recorded
	// "no waiter" waits to be handed to the next waiter, and noticed is the notice shown, "" for none.
	handed  uint64
	missed  bool
	noticed string
	// quiet is [local] wake_quiet, 0 for holding off, and maxDelay [local] wake_max_delay: held message wakes are
	// released once none has arrived for quiet, or maxDelay after the first. heldSeen is the waiter's count of held
	// wakes when last read, and heldFirst and heldLast when the first and the newest pending one were seen.
	quiet, maxDelay     time.Duration
	heldSeen            uint64
	heldFirst, heldLast time.Time
}

// waiterSource is the session endpoint as the supervisor reads its waiter: *session.Endpoint.
type waiterSource interface {
	Waiter() session.WaiterState
	Hand(line string) error
	Release(line string) error
}

// wakeNotice shows and clears the wake notice outside the Director's input box, such as its pane's title.
type wakeNotice interface {
	Show(ctx context.Context, text string) error
	Clear(ctx context.Context) error
}

// waitSession waits for the started runtime c, in unit u, to end, passing its unit each termination among signals,
// and returns the last termination passed on, if any, why the supervisor ended the runtime, if it did, and c's end.
// Every App.SessionPoll it ticks the checks watcher and the workspace close sweep, and every poll and at each turn signal it decides with
// decideEnd whether to end the runtime's unit; once it has, it kills the unit if the runtime has not exited
// stopKill later. However the runtime ends, it returns only once the unit is empty: whatever the runtime left
// running is killed, and a unit not proven empty stopKill after the runtime's end is an error.
func (a *App) waitSession(u runtimeUnit, c *exec.Cmd, signals <-chan os.Signal, w *sessionWatch) (os.Signal, endAction, error) {
	poll := a.SessionPoll
	if poll <= 0 {
		poll = time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	w.refreshLabels(time.Now())
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
			w.refreshLabels(time.Now())
			if w.checks != nil {
				w.checks.tick(time.Now())
			}
			if w.deps != nil {
				w.deps.tick(time.Now())
			}
			if w.queue != nil {
				w.queue.tick(time.Now())
			}
			if w.starts != nil {
				w.starts.tick(time.Now())
			}
			w.closes.tick(time.Now())
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
	// A wake a waiter took, or a waiter's refresh, is the wake's activity: the Director is not idle again until a
	// turn ends after it, either the rewake turn it starts or the running turn it is added to. It is counted
	// before the turn signal, which may already be that turn's end.
	var held session.WaiterState
	if w.waiter != nil {
		held = w.waiter.Waiter()
		if n := held.Handed + held.Refreshed; n > w.handed {
			w.handed, w.turned, w.signalled = n, false, now
		}
		w.seeHeld(held, now)
	}
	switch turn {
	case session.TurnStarted:
		w.turned = false
	case session.TurnEnded:
		w.turned = true
	}
	var unread map[string]int
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
				switch {
				// A typed wake's record is its activity; a handed one was counted when the waiter took it.
				case wk.Decision == store.WakeWoke && (in.Session.TypedWakes() || w.waiter == nil):
					w.woke, w.signalled = wk.Sequence, now
				case wk.Decision == store.WakeQueued && wk.Reason == messaging.ReasonNoWaiter:
					w.missed = true
				}
			}
		}
		if in.TurnEnded {
			w.turnWake = w.wakes
		}
		// A held wake is a wake on its way: until it is released the Director is neither idle nor quiet, so a stop
		// or reload does not end the session before the line reaches it.
		if held.Pending {
			w.signalled = now
		}
		in.Idle = w.turned && w.woke <= w.turnWake && !held.Pending
		in.Quiet = now.Sub(w.signalled)
		if w.waiter != nil {
			if unread, err = s.DirectorUnread(); err != nil {
				return in, err
			}
		}
		return in, nil
	})
	if err != nil {
		return keepRunning
	}
	w.holdTick(in, held, unread, now)
	w.waiterTick(in, unread)
	w.idleTrigger(in)
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

// seeHeld stamps the held wakes in st read at now: the first pending one when holding starts, and the newest
// whenever the count of held wakes grows.
func (w *sessionWatch) seeHeld(st session.WaiterState, now time.Time) {
	if !st.Pending {
		w.heldFirst = time.Time{}
	}
	if st.Held > w.heldSeen {
		if w.heldFirst.IsZero() && st.Pending {
			w.heldFirst = now
		}
		w.heldSeen, w.heldLast = st.Held, now
	}
}

// holdTick releases the session's held message wakes (docs/messaging.md#waking-the-director) as one wake line of
// the unread counts now, with the held wakes' handoff suffix, once none has arrived for quiet or maxDelay has
// passed since the first. A release that finds no waiter is a missed wake, which the next waiter is handed and the
// notice shows meanwhile. With nothing unread and no handoff due, or a session that is not running or recovering,
// the hold is cleared without a wake.
func (w *sessionWatch) holdTick(in endInput, st session.WaiterState, unread map[string]int, now time.Time) {
	if w.waiter == nil || w.quiet <= 0 || !st.Pending {
		return
	}
	if now.Sub(w.heldLast) < w.quiet && now.Sub(w.heldFirst) < w.maxDelay {
		return
	}
	w.heldFirst = time.Time{}
	live := in.Session.State == store.DirectorRunning || in.Session.State == store.DirectorRecovering
	line := ""
	if live && (len(unread) > 0 || st.Due) {
		line = messaging.WakeLine(unread)
	}
	if errors.Is(w.waiter.Release(line), session.ErrNoWaiter) {
		w.missed = true
	}
}

// waiterTick keeps the session's waiter in step with its unread messages (docs/decisions/director-wake-hook.md):
// once a waiter is registered after a wake was recorded "no waiter", it hands that waiter one wake line of the
// current unread counts, and with no waiter registered it shows the wake notice outside the input box while
// messages are unread and either a wake was missed or the Director is idle. The notice is cleared once the wake is
// delivered or nothing is unread. A session that is not running or recovering, such as a paused one, is not woken
// and shows no notice.
func (w *sessionWatch) waiterTick(in endInput, unread map[string]int) {
	if w.waiter == nil {
		return
	}
	live := in.Session.State == store.DirectorRunning || in.Session.State == store.DirectorRecovering
	if !live || len(unread) == 0 {
		w.missed = false
	}
	registered := w.waiter.Waiter().Registered
	if registered && w.missed && w.waiter.Hand(messaging.WakeLine(unread)) == nil {
		w.missed = false
	}
	want := ""
	if live && len(unread) > 0 && !registered && (w.missed || in.Idle) {
		want = messaging.WakeLine(unread)
	}
	w.setNotice(want)
}

// setNotice shows text as the wake notice, or clears it for "", when that changes what is shown. A notice Herdr
// refused is not tried again until the text changes.
func (w *sessionWatch) setNotice(text string) {
	if w.notice == nil || text == w.noticed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	if text == "" {
		_ = w.notice.Clear(ctx)
	} else {
		_ = w.notice.Show(ctx, text)
	}
	w.noticed = text
}

// idleTrigger types the idle line (docs/director.md#automatic-handoff) once per quiet period: when handoff_idle is
// set, the session is running or recovering with no reload pending, the Director has been quiet for handoff_idle
// and, with a hook, its turn is over. A line that could not be typed is not tried again in the same quiet period.
func (w *sessionWatch) idleTrigger(in endInput) {
	live := in.Session.State == store.DirectorRunning || in.Session.State == store.DirectorRecovering
	turnOver := !w.hook || in.Idle || in.Quiet >= w.idleAfter
	if w.handoffIdle <= 0 || !live || in.Reload != nil || w.idled.Equal(w.signalled) || in.Quiet < w.handoffIdle || !turnOver {
		return
	}
	w.idled = w.signalled
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	_, _, _ = messaging.WakeIdle(ctx, storeRecords{path: w.storePath}, w.pane, w.handoffIdle)
}

// stopSession records session n stopped unless it already is or a newer session superseded it.
func stopSession(storePath string, n int) error {
	_, err := withStore(storePath, false, func(s *store.Store) (struct{}, error) {
		d, err := s.LatestDirectorSession()
		if err != nil || d.Number != n || d.State == store.DirectorStopped {
			return struct{}{}, err
		}
		if runtimeEnded(d) != nil {
			if !d.Sweeping && d.State != store.DirectorStopping {
				_, err = s.RequestDirectorStop(n, false, d.StateSequence)
			}
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
