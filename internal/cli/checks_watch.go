package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// taskRef names one task.
type taskRef struct{ repo, task string }

// checksWatch is the session supervisor's continuation of the tasks whose last advance waits for their published
// head's checks (docs/director.md#checks), so a task does not stay at validate once its checks finish with no
// model turn spent asking.
//
// Each round lists the tasks waiting with waiting and advances each in turn with advance, as task start would,
// reporting each failure with report; the advance itself sends the Director what changed, and its messages wake
// the Director only for what is new. tick starts a round when none is running and every has passed since the last
// round ended, by now, so each read a round makes is at least every old by the next and the checks are read once
// per every, the config's checks_refresh or checksWatchFloor; the first tick starts one at once. A failed advance is tried again in
// the next round. wait waits for a running round to end.
type checksWatch struct {
	every   time.Duration
	waiting func() ([]taskRef, error)
	advance func(taskRef) error
	report  func(taskRef, error)
	now     func() time.Time
	// drain, when set, is the round instead, as the start-queue watcher's is (startQueueRound).
	drain func()

	mu      sync.Mutex
	last    time.Time
	started bool
	busy    bool
	running sync.WaitGroup
}

func (w *checksWatch) tick(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy || w.started && now.Sub(w.last) < w.every {
		return
	}
	w.busy, w.started = true, true
	w.running.Add(1)
	go func() {
		defer w.running.Done()
		if w.drain != nil {
			w.drain()
		} else {
			w.round()
		}
		w.mu.Lock()
		w.busy, w.last = false, w.now()
		w.mu.Unlock()
	}()
}

// round advances each waiting task in turn, reporting each failure.
func (w *checksWatch) round() {
	tasks, err := w.waiting()
	if err != nil {
		w.report(taskRef{}, err)
		return
	}
	for _, t := range tasks {
		if err := w.advance(t); err != nil {
			w.report(t, err)
		}
	}
}

func (w *checksWatch) wait() {
	if w != nil {
		w.running.Wait()
	}
}

// checksWaiting lists, for the watcher of the store at path, the open tasks whose next.json under state waits for
// checks, while the newest session is running or recovering; none while it is paused, stopping or stopped, which
// hold new work.
func checksWaiting(path, state string) func() ([]taskRef, error) {
	return waitingFor(path, state, interp.WaitChecks)
}

// dependenciesWaiting lists, as checksWaiting does, the open tasks whose next.json says the dependency gate holds
// them (docs/director.md#dependencies).
func dependenciesWaiting(path, state string) func() ([]taskRef, error) {
	return waitingFor(path, state, interp.WaitDependencies)
}

// mergeQueueWaiting lists, as checksWaiting does, the open tasks whose next.json says they wait in their
// repository's merge queue (docs/interpreter.md#the-merge-queue), and then each repository's held front, whatever
// its next.json waits on, so its advance releases the front to the next task; each task is listed once.
func mergeQueueWaiting(path, state string) func() ([]taskRef, error) {
	return waitingAlso(path, state, interp.WaitMergeQueue, heldFronts)
}

// heldFronts lists, for each repository of s, the task holding its merge queue's front when that task's merge is
// held.
func heldFronts(s *store.Store) ([]taskRef, error) {
	repos, err := s.Repositories()
	if err != nil {
		return nil, err
	}
	var out []taskRef
	for _, repo := range repos {
		q, err := s.MergeQueue(repo)
		if err != nil {
			return nil, err
		}
		if q.Front != nil && q.Holds[q.Front.TaskID] {
			out = append(out, taskRef{repo, q.Front.TaskID})
		}
	}
	return out, nil
}

// waitingFor lists the open tasks whose next.json under state says wait, while the newest session of the store at
// path is running or recovering.
func waitingFor(path, state string, wait interp.Wait) func() ([]taskRef, error) {
	return waitingAlso(path, state, wait, nil)
}

// waitingAlso is waitingFor followed by the tasks also lists from the same read of the store, when also is set,
// each not listed already.
func waitingAlso(path, state string, wait interp.Wait, also func(*store.Store) ([]taskRef, error)) func() ([]taskRef, error) {
	type read struct {
		tasks []store.Task
		also  []taskRef
	}
	return func() ([]taskRef, error) {
		r, err := withStore(path, true, func(s *store.Store) (read, error) {
			d, err := s.LatestDirectorSession()
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return read{}, err
			case d.State != store.DirectorRunning && d.State != store.DirectorRecovering:
				return read{}, nil
			}
			var r read
			if r.tasks, err = listTasks(s, ""); err != nil || also == nil {
				return r, err
			}
			r.also, err = also(s)
			return r, err
		})
		if err != nil {
			return nil, err
		}
		var out []taskRef
		for _, t := range r.tasks {
			if t.State != store.TaskOpen {
				continue
			}
			b, err := atomicfile.Read(filepath.Join(state, t.Repository, t.ID, nextFile))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return out, err
			}
			var n next
			if json.Unmarshal(b, &n) == nil && n.Wait == wait {
				out = append(out, taskRef{t.Repository, t.ID})
			}
		}
		for _, t := range r.also {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		return out, nil
	}
}

// watchChecks is session n's checks watcher: every checks_refresh of the session's config, but never more often
// than every App.ChecksWatchFloor, checksWatchFloor when zero, it advances each task waiting for checks as `task start` does, over the copy's store, config
// and state, with the advance's standard-error output and each failure appended to checks.log in the session directory dir.
func (a *App) watchChecks(p startPlan, n int, dir, copyDir, storePath, state string) (*checksWatch, error) {
	return a.watchWaiting(p, dir, copyDir, storePath, state, checksWaiting(storePath, state), checksLog)
}

// watchDependencies is session n's dependency watcher: a second checks watcher, on the same interval, advancing each
// task the dependency gate holds, so a task whose dependencies merged starts with no model turn; it logs to
// dependencies.log in dir (docs/director.md#dependencies).
func (a *App) watchDependencies(p startPlan, n int, dir, copyDir, storePath, state string) (*checksWatch, error) {
	return a.watchWaiting(p, dir, copyDir, storePath, state, dependenciesWaiting(storePath, state), dependenciesLog)
}

// watchMergeQueue is session n's merge-queue watcher: a third checks watcher, on the same interval, advancing each
// task waiting in its repository's merge queue, so the next task claims the front once the one ahead merges, with no
// model turn, and each held front, so its advance releases the front to the next task; it logs to merge-queue.log in dir. Like the others it runs only while the session is running or
// recovering, so a paused Director pauses the queue.
func (a *App) watchMergeQueue(p startPlan, n int, dir, copyDir, storePath, state string) (*checksWatch, error) {
	return a.watchWaiting(p, dir, copyDir, storePath, state, mergeQueueWaiting(storePath, state), mergeQueueLog)
}

// watchWaiting is a watcher of the tasks waiting lists, logging to the file log in dir.
func (a *App) watchWaiting(p startPlan, dir, copyDir, storePath, state string, waiting func() ([]taskRef, error), log string) (*checksWatch, error) {
	w, advance, err := a.watcher(p, dir, copyDir, storePath, state, log)
	if err != nil {
		return nil, err
	}
	w.waiting = waiting
	w.advance = func(t taskRef) error {
		_, _, err := advance(t)
		return err
	}
	return w, nil
}

// watcher is a watcher on the session's interval, with its report logging to the file log in dir, and advance, which
// advances a task as `task start` does, logging its standard-error output there; advance does nothing, reporting
// false, while the newest session holds new attempts.
func (a *App) watcher(p startPlan, dir, copyDir, storePath, state, log string) (*checksWatch, func(taskRef) (interp.Next, bool, error), error) {
	every, err := p.cfg.ChecksRefresh()
	if err != nil {
		return nil, nil, err
	}
	floor := a.ChecksWatchFloor
	if floor == 0 {
		floor = checksWatchFloor
	}
	every = max(every, floor)
	logf := watchLog(filepath.Join(dir, log))
	f := advanceFlags{store: storePath, config: p.cfgPath, state: state, copy: copyFlags{dir: copyDir}}
	advance := func(t taskRef) (interp.Next, bool, error) {
		f := f
		f.repo = t.repo
		if err := f.listed(); err != nil {
			return interp.Next{}, false, err
		}
		if hold, err := withRecords(storeRecords{path: storePath, wait: f.wait}, true, nil, held); err != nil || hold {
			return interp.Next{}, false, err
		}
		b := *a
		var out strings.Builder
		b.Stdout, b.Stderr = io.Discard, &out
		n, err := b.advance(f, interp.Event{Kind: interp.TaskCreated, Repository: t.repo, Task: t.task})
		if s := strings.TrimSpace(out.String()); s != "" {
			logf("%s/%s: %s", t.repo, t.task, oneLine(s))
		}
		return n, true, err
	}
	w := &checksWatch{every: every, now: time.Now,
		report: func(t taskRef, err error) {
			if t == (taskRef{}) {
				logf("waiting tasks: %v", err)
				return
			}
			logf("%s/%s: advance: %v", t.repo, t.task, err)
		},
	}
	return w, advance, nil
}

// watchLog appends a time-stamped line to the log at path for each call, ignoring a log it cannot open.
func watchLog(path string) func(format string, args ...any) {
	return func(format string, args ...any) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
		_ = f.Close()
	}
}

// watchStartQueue is session n's start-queue watcher: a fourth checks watcher, on the same interval, that drains the
// start queue (docs/director.md#the-start-queue), so a queued start begins once a slot frees, with no model turn; it
// logs to start-queue.log in dir. Like the others it runs only while the session is running or recovering, so a
// paused Director keeps the queue but drains none of it.
func (a *App) watchStartQueue(p startPlan, n int, dir, copyDir, storePath, state string) (*checksWatch, error) {
	w, advance, err := a.watcher(p, dir, copyDir, storePath, state, startQueueLog)
	if err != nil {
		return nil, err
	}
	w.drain = func() { startQueueRound(storePath, advance, watchLog(filepath.Join(dir, startQueueLog))) }
	return w, nil
}

// startQueueRound is one round of the start-queue watcher over the store at path, while its newest session is running
// or recovering: it advances the queued tasks in queue order as `task start` does, with advance, and stops at the
// first whose start stays queued, since none behind it is admitted before it. An advance that neither starts the
// queued attempt nor queues it again, such as one the dependency gate holds, one that fails or one that now waits on
// another step, queued or not, takes the entry out of the queue, so it does not hold the front; the task's next.json reports that
// advance as it does any other's, and logf records the drop.
func startQueueRound(path string, advance func(taskRef) (interp.Next, bool, error), logf func(format string, args ...any)) {
	read := func() ([]store.QueuedStart, error) {
		return withStore(path, true, func(s *store.Store) ([]store.QueuedStart, error) {
			switch d, err := s.LatestDirectorSession(); {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return nil, err
			case d.State != store.DirectorRunning && d.State != store.DirectorRecovering:
				return nil, nil
			}
			return s.StartQueue()
		})
	}
	queue, err := read()
	if err != nil {
		logf("start queue: %v", err)
		return
	}
	for _, e := range queue {
		t := taskRef{e.Repository, e.TaskID}
		n, advanced, err := advance(t)
		if !advanced && err == nil {
			// The session holds new attempts: nothing drains until it lets them go.
			return
		}
		if err == nil && n.Wait == interp.WaitQueued && n.Step == e.Step && n.Attempt == e.Attempt {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, store.ErrLocked) {
			// The task or the store was busy: the entry keeps its place and the next round tries again.
			logf("%s/%s: advance: %v", t.repo, t.task, err)
			return
		}
		after, rerr := read()
		if rerr != nil {
			logf("start queue: %v", rerr)
			return
		}
		if !slices.ContainsFunc(after, func(q store.QueuedStart) bool { return q.StartQueueEntry == e.StartQueueEntry }) {
			// Started, or no longer queued for another reason the store already took account of.
			continue
		}
		why := fmt.Sprintf("the advance waits %s for step %s", n.Wait, n.Step)
		if err != nil {
			why = fmt.Sprintf("advance: %v", err)
		}
		_, lerr := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
			return s.LeaveStartQueue(store.LeaveStartQueueMutation(e.StartQueueEntry), e.StartQueueEntry)
		})
		if lerr != nil {
			logf("%s/%s: leave the start queue: %v", t.repo, t.task, lerr)
			continue
		}
		logf("%s/%s: attempt %d of step %s left the start queue: %s", t.repo, t.task, e.Attempt, e.Step, oneLine(why))
	}
}

// checksWatchFloor is the shortest time between a checks watcher's rounds, whatever checks_refresh is, so a short
// or "0s" refresh does not read the forge at every supervisor tick (docs/director.md#checks).
const checksWatchFloor = 30 * time.Second

// checksLog is the file in a session's directory its checks watcher logs to (docs/director.md#checks).
const checksLog = "checks.log"

// dependenciesLog is the file in a session's directory its dependency watcher logs to (docs/director.md#dependencies).
const dependenciesLog = "dependencies.log"

// mergeQueueLog is the file in a session's directory its merge-queue watcher logs to.
const mergeQueueLog = "merge-queue.log"

// startQueueLog is the file in a session's directory its start-queue watcher logs to (docs/director.md#the-start-queue).
const startQueueLog = "start-queue.log"
