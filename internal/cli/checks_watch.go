package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
// per every, the config's checks_refresh; the first tick starts one at once. A failed advance is tried again in the next round. wait waits for a running round to end.
type checksWatch struct {
	every   time.Duration
	waiting func() ([]taskRef, error)
	advance func(taskRef) error
	report  func(taskRef, error)
	now     func() time.Time

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
		w.round()
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

func (w *checksWatch) wait() { w.running.Wait() }

// checksWaiting lists, for the watcher of the store at path, the open tasks whose next.json under state waits for
// checks, while the newest session is running or recovering; none while it is paused, stopping or stopped, which
// hold new work.
func checksWaiting(path, state string) func() ([]taskRef, error) {
	return func() ([]taskRef, error) {
		tasks, err := withStore(path, true, func(s *store.Store) ([]store.Task, error) {
			d, err := s.LatestDirectorSession()
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return nil, err
			case d.State != store.DirectorRunning && d.State != store.DirectorRecovering:
				return nil, nil
			}
			return listTasks(s, "")
		})
		if err != nil {
			return nil, err
		}
		var out []taskRef
		for _, t := range tasks {
			if t.State != store.TaskOpen {
				continue
			}
			b, err := os.ReadFile(filepath.Join(state, t.Repository, t.ID, nextFile))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return out, err
			}
			var n next
			if json.Unmarshal(b, &n) == nil && n.Wait == interp.WaitChecks {
				out = append(out, taskRef{t.Repository, t.ID})
			}
		}
		return out, nil
	}
}

// watchChecks is session n's checks watcher: every checks_refresh of the session's config it advances each task
// waiting for checks as `task start` does, over the copy's store, config and state, with the advance's output and
// each failure appended to checks.log in the session directory dir.
func (a *App) watchChecks(p startPlan, n int, dir, copyDir, storePath, state string) (*checksWatch, error) {
	every, err := p.cfg.ChecksRefresh()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, checksLog)
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
		_ = f.Close()
	}
	f := advanceFlags{store: storePath, config: p.cfgPath, state: state, copy: copyFlags{dir: copyDir}}
	return &checksWatch{every: every, waiting: checksWaiting(storePath, state), now: time.Now,
		advance: func(t taskRef) error {
			if hold, err := heldNow(storePath); err != nil || hold {
				return err
			}
			b := *a
			var out strings.Builder
			b.Stdout, b.Stderr = io.Discard, &out
			f := f
			f.repo = t.repo
			_, err := b.advance(f, interp.Event{Kind: interp.TaskCreated, Repository: t.repo, Task: t.task})
			if s := strings.TrimSpace(out.String()); s != "" {
				logf("%s/%s: %s", t.repo, t.task, oneLine(s))
			}
			return err
		},
		report: func(t taskRef, err error) {
			if t == (taskRef{}) {
				logf("waiting tasks: %v", err)
				return
			}
			logf("%s/%s: advance: %v", t.repo, t.task, err)
		},
	}, nil
}

// checksLog is the file in a session's directory its checks watcher logs to (docs/director.md#checks).
const checksLog = "checks.log"
