package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskReconcile runs `guildhall task reconcile`: it reads the forge now for the pull requests an open task recorded and,
// when one was merged outside Guildhall, records the task done as an advance would (docs/tasks.md#task-reconcile).
// Under the task lock it stops the task's running attempts as task cancel does, then applies the outside-merge rule
// and prints what it found. A done or cancelled task prints its state and changes nothing. Every write is idempotent,
// so a reconcile that fails part-way is finished by running it again, or by the task's next advance.
func (a *App) taskReconcile(args []string) int {
	fl := flag.NewFlagSet("task reconcile", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	if err := parseInterspersed(fl, args); err != nil {
		return a.taskUsage("task reconcile: " + err.Error())
	}
	if err := f.resolve(a); err != nil {
		return a.taskUsage("task reconcile: " + err.Error())
	}
	if fl.NArg() != 1 || !workflow.ValidID(fl.Arg(0)) {
		return a.taskUsage("task reconcile takes one task id")
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("task reconcile: %w", err))
	}
	if _, err := os.Stat(f.store); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("task reconcile: %w", noStore(f.store)))
	}
	if err := a.reconcileTask(f, fl.Arg(0)); err != nil {
		return a.fail(fmt.Errorf("task reconcile: %w", err))
	}
	return 0
}

// reconcileTask is task reconcile's work over f's copy, under the task lock.
func (a *App) reconcileTask(f advanceFlags, task string) error {
	cfg, repo, state, release, err := lockTask(f, task)
	if err != nil {
		return err
	}
	defer release()

	records := storeRecords{path: f.store, wait: f.wait}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(a.Stdout, format+"\n", args...) }
	t, err := records.Task(f.repo, task)
	if err != nil {
		return err
	}
	switch t.State {
	case store.TaskCancelled:
		say("task %s/%s is already cancelled: %s", f.repo, task, t.CancelReason)
		return nil
	case store.TaskDone:
		say("task %s/%s is already done", f.repo, task)
		return nil
	}
	attempts, err := records.Attempts(f.repo, task)
	if err != nil {
		return err
	}
	for _, at := range attempts {
		if at.Outcome != "" {
			continue
		}
		if err := a.stopForCancel(records, state, at); err != nil {
			return err
		}
		say("attempt %d of step %s stopped", at.Number, at.Step)
	}
	got, err := a.reconcileEngine(f, cfg, repo, state).Reconcile(context.Background(), f.repo, task)
	if err != nil {
		return err
	}
	if !got.Done {
		say("no change: no recorded pull request is merged")
		return nil
	}
	say("done: pull request %s merged outside Guildhall", got.URL)
	return nil
}

// lockTask loads f's config and listed repository and takes task's lock in the absolute state directory, as task
// cancel and task reconcile do before changing a task's records; release frees the lock.
func lockTask(f advanceFlags, task string) (cfg *config.Config, repo config.Repository, state string, release func(), err error) {
	if cfg, err = config.Load(f.config); err != nil {
		return nil, config.Repository{}, "", nil, err
	}
	if repo, err = cfg.ListedRepository(f.repo); err != nil {
		return nil, config.Repository{}, "", nil, err
	}
	if state, err = filepath.Abs(f.state); err != nil {
		return nil, config.Repository{}, "", nil, err
	}
	taskDir := filepath.Join(state, f.repo, task)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, config.Repository{}, "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.wait)
	release, err = artisan.LockAttempt(ctx, taskDir)
	cancel()
	if err != nil {
		return nil, config.Repository{}, "", nil, fmt.Errorf("lock task %s/%s: %w", f.repo, task, err)
	}
	return cfg, repo, state, release, nil
}

// forge is the App's forge: GitHub unless a test set one.
func (a *App) forge() interp.Forge {
	if a.Forge != nil {
		return a.Forge
	}
	return forge.GitHub{}
}

// reconcileEngine is the engine Engine.Reconcile runs on for task cancel and task reconcile: the records, forge and
// workspaces an advance has, and nothing that launches an attempt.
func (a *App) reconcileEngine(f advanceFlags, cfg *config.Config, repo config.Repository, state string) *interp.Engine {
	records := storeRecords{path: f.store, wait: f.wait}
	var ws interp.Workspaces = git.Program{}
	if a.Workspaces != nil {
		ws = a.Workspaces
	}
	cfgDir := filepath.Dir(f.config)
	return &interp.Engine{
		Records: records, Outbox: records, Config: cfg,
		Workflow: func(rev workflow.Revision) (*workflow.Definition, error) {
			return boundWorkflow(filepath.Join(cfgDir, "workflows"), cfg, repo, rev)
		},
		DocumentDir: filepath.Join(state, "documents"), StateDir: state, Forge: a.forge(), Workspaces: ws,
	}
}
