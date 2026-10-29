package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// advanceFlags name what an advance runs over (docs/running.md); a copy defaults the store, config and state.
type advanceFlags struct {
	store, config, state, repo string
	copy                       copyFlags
	wait                       time.Duration
}

func (f *advanceFlags) register(fl *flag.FlagSet) {
	f.copy.register(fl)
	fl.StringVar(&f.store, "store", "", "Director store file")
	fl.StringVar(&f.config, "config", "", "Director config file")
	fl.StringVar(&f.state, "state", "", "state directory")
	fl.StringVar(&f.repo, "repo", "", "repository id")
}

// resolve fills the paths from the copy, if one is selected, and checks every flag is given.
func (f *advanceFlags) resolve(getenv func(string) string) error {
	if err := f.copy.apply(getenv, &f.store, &f.config, &f.state); err != nil {
		return err
	}
	if f.store == "" || f.config == "" || f.state == "" || f.repo == "" {
		return errors.New("--store, --config, --state and --repo are required, or --copy and --repo")
	}
	return nil
}

// listed refuses, before anything else is read or written, a repository the config does not list: the
// repositories guard rail (docs/director.md#guard-rails).
func (f *advanceFlags) listed() error {
	cfg, err := config.Load(f.config)
	if err != nil {
		return err
	}
	if _, err = cfg.ListedRepository(f.repo); err != nil {
		return err
	}
	return f.resolveWait(cfg)
}

func (f *advanceFlags) resolveWait(cfg *config.Config) error {
	if f.wait > 0 {
		return nil
	}
	var err error
	f.wait, err = cfg.LockWait()
	return err
}

// next is what `task start`, `step done`, `step finish` and `step retry` print: interp.Next as JSON.
type next struct {
	Wait     interp.Wait `json:"wait"`
	Step     string      `json:"step"`
	Attempt  int         `json:"attempt,omitempty"`
	Decision string      `json:"decision,omitempty"`
	// Position is a queued start's 1-based place in the start queue at the time of the advance (wait queued).
	Position int `json:"position,omitempty"`
}

// taskStart runs `guildhall task start`: one advance with task-created, or with --decision,
// decision-answered (docs/running.md).
func (a *App) taskStart(args []string) int {
	fl := flag.NewFlagSet("task start", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	decision := fl.String("decision", "", "answered decision id")
	ignoreDeps := fl.Bool("ignore-deps", false, "start without waiting for the task's dependencies to merge")
	reason := fl.String("reason", "", "the Director's reason for --ignore-deps")
	if err := parseInterspersed(fl, args); err != nil {
		return a.taskUsage("task start: " + err.Error())
	}
	reasonSet := false
	fl.Visit(func(fl *flag.Flag) { reasonSet = reasonSet || fl.Name == "reason" })
	if err := f.resolve(a.Getenv); err != nil {
		return a.taskUsage("task start: " + err.Error())
	}
	switch {
	case fl.NArg() != 1 || !workflow.ValidID(fl.Arg(0)):
		return a.taskUsage("task start takes one task id")
	case *ignoreDeps && strings.TrimSpace(*reason) == "":
		return a.taskUsage("task start: --ignore-deps needs a nonempty --reason")
	case reasonSet && !*ignoreDeps:
		return a.taskUsage("task start: --reason is only given with --ignore-deps")
	case len(*reason) > store.MaxTextBytes:
		return a.taskUsage(fmt.Sprintf("task start: --reason is %d bytes; the limit is %d", len(*reason), store.MaxTextBytes))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("task start: %w", err))
	}
	ev := interp.Event{Kind: interp.TaskCreated, Repository: f.repo, Task: fl.Arg(0)}
	if *decision != "" {
		ev.Kind, ev.Decision = interp.DecisionAnswered, *decision
	}
	records := storeRecords{path: f.store, wait: f.wait}
	if err := newWorkAdmitted(records, f.repo, ev.Task); err != nil {
		return a.fail(fmt.Errorf("task start: %w", err))
	}
	if *ignoreDeps {
		if err := overrideDependencies(records, f.repo, ev.Task, *reason); err != nil {
			return a.fail(fmt.Errorf("task start: %w", err))
		}
	}
	return a.advanced(a.advance(f, ev))
}

// overrideDependencies records the Director's dependency override of an open task that has dependencies, under a
// mutation id derived from the task, so repeating the command with the same reason replays it and the advance that
// follows passes the dependency gate (docs/running.md#task-start). A second override with another reason is refused.
func overrideDependencies(records storeRecords, repo, task, reason string) error {
	if _, err := os.Stat(records.path); errors.Is(err, fs.ErrNotExist) {
		return noStore(records.path)
	}
	_, err := withRecords(records, false, nil, func(s *store.Store) (struct{}, error) {
		t, err := s.Task(repo, task)
		switch {
		case err != nil:
			return struct{}{}, err
		case t.State != store.TaskOpen:
			return struct{}{}, fmt.Errorf("--ignore-deps: task %s/%s is %s", repo, task, t.State)
		case len(t.DependsOn) == 0:
			return struct{}{}, fmt.Errorf("--ignore-deps: task %s/%s has no dependencies", repo, task)
		}
		_, err = s.OverrideDependencies(dependencyOverrideMutation(repo, task), repo, task, reason)
		if errors.Is(err, store.ErrMutationConflict) || errors.Is(err, store.ErrExists) {
			prior, perr := s.DependencyOverride(repo, task)
			if perr != nil {
				return struct{}{}, errors.Join(err, perr)
			}
			return struct{}{}, fmt.Errorf("--ignore-deps: task %s/%s was already overridden, with reason %q", repo, task, prior.Reason)
		}
		return struct{}{}, err
	})
	return err
}

// dependencyOverrideMutation is the mutation id of a task's dependency override, dependency-override.<repo>.<task>,
// hashed to fit the store's 128 bytes.
func dependencyOverrideMutation(repo, task string) string {
	id := "dependency-override." + repo + "." + task
	if len(id) > 128 {
		id = "dependency-override." + digest.Of([]byte(repo+"/"+task)).Hex()
	}
	return id
}

// newWorkAdmitted refuses to start a task with no attempt yet while the newest Director session is recovering;
// a task already under way advances (docs/director.md#ready). A missing store is left to the advance to refuse.
func newWorkAdmitted(records storeRecords, repo, task string) error {
	if _, err := os.Stat(records.path); err != nil {
		return nil
	}
	_, err := withRecords(records, true, nil, func(s *store.Store) (struct{}, error) {
		refusal := refuseWhileRecovering(s)
		if refusal == nil {
			return struct{}{}, nil
		}
		attempts, err := s.Attempts(repo, task)
		if err != nil || len(attempts) > 0 {
			return struct{}{}, err
		}
		return struct{}{}, refusal
	})
	return err
}

// step runs `guildhall step done|finish|retry|send-back|park|unpark`.
func (a *App) step(args []string) int {
	switch {
	case len(args) > 0 && args[0] == "done":
		return a.stepDone(args[1:])
	case len(args) > 0 && args[0] == "finish":
		return a.stepFinish(args[1:])
	case len(args) > 0 && args[0] == "retry":
		return a.stepRetry(args[1:])
	case len(args) > 0 && args[0] == "send-back":
		return a.stepSendBack(args[1:])
	case len(args) > 0 && args[0] == "park":
		return a.stepPark(args[1:])
	case len(args) > 0 && args[0] == "unpark":
		return a.stepUnpark(args[1:])
	}
	return a.usageError("step: the commands are done, finish, retry, send-back, park and unpark")
}

// stepRetry runs `guildhall step retry`: it feeds attempt-retried for the Director's retry of a failed or
// stopped attempt, with its reason when given (docs/running.md).
func (a *App) stepRetry(args []string) int {
	fl := flag.NewFlagSet("step retry", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	step := fl.String("step", "", "step id")
	attempt := fl.String("attempt", "", "attempt number")
	reason := fl.String("reason", "", "the Director's reason")
	if err := fl.Parse(args); err != nil {
		return a.usageError("step retry: " + err.Error())
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("step retry: " + ferr.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("step retry: unexpected argument %q", fl.Arg(0)))
	case *task == "" || *step == "" || *attempt == "":
		return a.usageError("step retry: --task, --step and --attempt are required")
	case !workflow.ValidID(*task) || !workflow.ValidID(*step):
		return a.usageError("step retry: --task and --step must be ids")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("step retry: --attempt %q must be a positive attempt number", *attempt))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("step retry: %w", err))
	}
	return a.advanced(a.advance(f, interp.Event{Kind: interp.AttemptRetried, Repository: f.repo, Task: *task, Step: *step, Attempt: n, Reason: *reason}))
}

// stepPark runs `guildhall step park`: it records the Director's park of a failed or stopped attempt, with its
// reason, so status leaves the attempt out of needs_me (docs/running.md#step-park). It takes no task lock and
// writes no attempt outcome, next.json, decision or message.
func (a *App) stepPark(args []string) int {
	return a.parkCommand("step park", args, true)
}

// stepUnpark runs `guildhall step unpark`: it lifts a park, so the attempt is back in needs_me.
func (a *App) stepUnpark(args []string) int {
	return a.parkCommand("step unpark", args, false)
}

func (a *App) parkCommand(name string, args []string, park bool) int {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	step := fl.String("step", "", "step id")
	attempt := fl.String("attempt", "", "attempt number")
	var reason *string
	if park {
		reason = fl.String("reason", "", "the Director's reason")
	}
	if err := fl.Parse(args); err != nil {
		return a.usageError(name + ": " + err.Error())
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError(name + ": " + ferr.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case *task == "" || *step == "" || *attempt == "":
		return a.usageError(name + ": --task, --step and --attempt are required")
	case !workflow.ValidID(*task) || !workflow.ValidID(*step):
		return a.usageError(name + ": --task and --step must be ids")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("%s: --attempt %q must be a positive attempt number", name, *attempt))
	case park && strings.TrimSpace(*reason) == "":
		return a.usageError(name + ": --reason is required")
	case park && len(*reason) > store.MaxTextBytes:
		return a.usageError(fmt.Sprintf("%s: --reason is %d bytes; the limit is %d", name, len(*reason), store.MaxTextBytes))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if _, err := os.Stat(f.store); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("%s: %w", name, noStore(f.store)))
	}
	records := storeRecords{path: f.store, wait: f.wait}
	var p store.AttemptPark
	if park {
		p, err = parkAttempt(records, f.repo, *task, *step, n, *reason)
	} else {
		p, err = unparkAttempt(records, f.repo, *task, *step, n)
	}
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(p)
}

// parkAttempt parks an attempt under the mutation id of its park record's current cycle, so repeating an identical
// park replays it, and refuses a park with another reason while the attempt is parked.
func parkAttempt(records storeRecords, repo, task, step string, n int, reason string) (store.AttemptPark, error) {
	return withRecords(records, false, nil, func(s *store.Store) (store.AttemptPark, error) {
		prior, err := s.AttemptPark(repo, task, step, n)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.AttemptPark{}, err
		}
		alreadyParked := func(p store.AttemptPark) error {
			return fmt.Errorf("attempt %d of %s of %s/%s is already parked, with reason %q; unpark it first", n, step, repo, task, p.Reason)
		}
		if prior.Parked && prior.Reason != reason {
			return store.AttemptPark{}, alreadyParked(prior)
		}
		_, err = s.ParkAttempt(attemptParkMutation("park", repo, task, step, n, prior.Cycle), repo, task, step, n, reason, prior.Cycle)
		if errors.Is(err, store.ErrMutationConflict) || errors.Is(err, store.ErrExists) {
			// A park or unpark between the read and the write: report the record as it now stands.
			if now, rerr := s.AttemptPark(repo, task, step, n); rerr == nil && now.Parked {
				return store.AttemptPark{}, alreadyParked(now)
			}
		}
		if err != nil {
			return store.AttemptPark{}, err
		}
		return s.AttemptPark(repo, task, step, n)
	})
}

// unparkAttempt unparks a parked attempt under the mutation id of its park record's current cycle.
func unparkAttempt(records storeRecords, repo, task, step string, n int) (store.AttemptPark, error) {
	return withRecords(records, false, nil, func(s *store.Store) (store.AttemptPark, error) {
		prior, err := s.AttemptPark(repo, task, step, n)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.AttemptPark{}, err
		}
		if !prior.Parked {
			return store.AttemptPark{}, fmt.Errorf("attempt %d of %s of %s/%s is not parked", n, step, repo, task)
		}
		if _, err := s.UnparkAttempt(attemptParkMutation("unpark", repo, task, step, n, prior.Cycle), repo, task, step, n, prior.Cycle); err != nil {
			return store.AttemptPark{}, err
		}
		return s.AttemptPark(repo, task, step, n)
	})
}

// attemptParkMutation is the mutation id of a park or unpark at a park record's cycle,
// <op>.<repo>.<task>.<step>.<n>.<cycle>, hashed to fit the store's 128 bytes.
func attemptParkMutation(op, repo, task, step string, n, cycle int) string {
	id := fmt.Sprintf("%s.%s.%s.%s.%d.%d", op, repo, task, step, n, cycle)
	if len(id) > 128 {
		id = op + "." + digest.Of(fmt.Appendf(nil, "%s/%s/%s/%d/%d", repo, task, step, n, cycle)).Hex()
	}
	return id
}

// stepDone runs `guildhall step done`: it validates the attempt's result file and feeds attempt-result (docs/running.md).
func (a *App) stepDone(args []string) int {
	fl := flag.NewFlagSet("step done", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	step := fl.String("step", "", "step id")
	attempt := fl.String("attempt", "", "attempt number")
	result := fl.String("result", "", "result file")
	if err := fl.Parse(args); err != nil {
		return a.usageError("step done: " + err.Error())
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("step done: " + ferr.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("step done: unexpected argument %q", fl.Arg(0)))
	case *task == "" || *step == "" || *result == "":
		return a.usageError("step done: --task, --step, --attempt and --result are required")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("step done: --attempt %q must be a positive attempt number", *attempt))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("step done: %w", err))
	}
	state, err := filepath.Abs(f.state)
	if err != nil {
		return a.fail(err)
	}
	file, err := filepath.Abs(*result)
	if err != nil {
		return a.fail(err)
	}
	k := artisan.AttemptKey{Repository: f.repo, TaskID: *task, Step: *step, Attempt: n}
	if want := filepath.Join(k.Dir(state), artisan.ResultFile); file != want {
		return a.fail(fmt.Errorf("step done: result %s is not attempt %s's result file %s", file, k, want))
	}
	if _, err := adapter.ReadResult(file, adapter.Identity{TaskID: k.TaskID, AttemptID: k.AttemptID()}); err != nil &&
		!errors.Is(err, adapter.ErrMalformedResult) && !errors.Is(err, adapter.ErrIdentityMismatch) {
		return a.fail(fmt.Errorf("step done: %w", err))
	}
	// Contract violations are taken under the task lock by the interpreter, which
	// records rejection; operational read errors above remain replayable.
	return a.advanced(a.advance(f, interp.Event{Kind: interp.AttemptResult, Repository: f.repo, Task: *task, Step: *step, Attempt: n}))
}

// advanced prints where an advance left the task.
func (a *App) advanced(n interp.Next, err error) int {
	if err != nil {
		return a.fail(err)
	}
	return a.printJSON(nextOf(n))
}

// parseInterspersed parses flags given before or after positional arguments.
func parseInterspersed(fl *flag.FlagSet, args []string) error {
	var positional []string
	for len(args) > 0 {
		if err := fl.Parse(args); err != nil {
			return err
		}
		if args = fl.Args(); len(args) > 0 {
			positional, args = append(positional, args[0]), args[1:]
		}
	}
	return fl.Parse(append([]string{"--"}, positional...))
}

// storeWait bounds each record operation's wait for the store's lock, which launchers take briefly meanwhile, where
// no copy's lock wait applies: config.DefaultLockWait, which an advance replaces with its copy's lock_wait.
const storeWait = config.DefaultLockWait

// advance runs one engine advance of ev's task over f (docs/running.md).
func (a *App) advance(f advanceFlags, ev interp.Event) (interp.Next, error) {
	cfg, err := config.Load(f.config)
	if err != nil {
		return interp.Next{}, fmt.Errorf("config: %w", err)
	}
	stall, err := cfg.StallInterval()
	if err != nil {
		return interp.Next{}, fmt.Errorf("config: %w", err)
	}
	refresh, err := cfg.ChecksRefresh()
	if err != nil {
		return interp.Next{}, fmt.Errorf("config: %w", err)
	}
	rails, err := cfg.GuardRails()
	if err != nil {
		return interp.Next{}, fmt.Errorf("config: %w", err)
	}
	if err := f.resolveWait(cfg); err != nil {
		return interp.Next{}, fmt.Errorf("config: %w", err)
	}
	wait := f.wait
	records := storeRecords{path: f.store, wait: wait}
	r, err := cfg.ListedRepository(f.repo)
	if err != nil {
		return interp.Next{}, fmt.Errorf("config %s: %w", f.config, err)
	}
	cfgDir := filepath.Dir(f.config)
	state, err := filepath.Abs(f.state)
	if err != nil {
		return interp.Next{}, err
	}
	if _, err := os.Stat(f.store); errors.Is(err, fs.ErrNotExist) {
		return interp.Next{}, noStore(f.store)
	} else if err != nil {
		return interp.Next{}, fmt.Errorf("no store at %s: %w", f.store, err)
	}
	exe, err := a.Executable()
	if err != nil {
		return interp.Next{}, err
	}
	copyDir := f.copy.dir
	if copyDir != "" {
		if copyDir, err = filepath.Abs(copyDir); err != nil {
			return interp.Next{}, err
		}
	}
	if a.InstallSkills == nil || a.InstallBaseline == nil {
		return interp.Next{}, errors.New("release skills and baseline installers are not configured")
	}
	taskDir := filepath.Join(state, ev.Repository, ev.Task)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return interp.Next{}, err
	}
	// Deferred before the task lock is taken, so the Director is woken for what the advance sent once the lock is
	// released (docs/messaging.md#waking-the-director), whether or not the advance succeeded.
	outbox := &deferredOutbox{records: records}
	defer outbox.wakeAll(wakingOutbox{records, a.directorPane(), a.Stderr})
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	release, err := artisan.LockAttempt(ctx, taskDir)
	cancel()
	if err != nil {
		return interp.Next{}, fmt.Errorf("lock task %s/%s: %w", ev.Repository, ev.Task, err)
	}
	defer release()
	skills, err := os.MkdirTemp("", "guildhall-release-*")
	if err != nil {
		return interp.Next{}, err
	}
	defer func() { _ = os.RemoveAll(skills) }()
	if err := a.InstallSkills(skills); err != nil {
		return interp.Next{}, err
	}
	releaseBaseline, err := os.MkdirTemp("", "guildhall-baseline-*")
	if err != nil {
		return interp.Next{}, err
	}
	defer func() { _ = os.RemoveAll(releaseBaseline) }()
	if err := a.InstallBaseline(releaseBaseline); err != nil {
		return interp.Next{}, err
	}
	standards, _ := cfg.Setting("standards_revision")
	var fg interp.Forge = forge.GitHub{}
	if a.Forge != nil {
		fg = a.Forge
	}
	var ws interp.Workspaces = git.Program{}
	if a.Workspaces != nil {
		ws = a.Workspaces
	}
	// An interrupt ends the advance and releases the task lock; the next advance replays it.
	notify := a.AdvanceContext
	if notify == nil {
		notify = signal.NotifyContext
	}
	ctx, stop := notify(context.Background(), os.Interrupt)
	defer stop()
	context.AfterFunc(ctx, stop)
	e := &interp.Engine{
		Copy:    copyDir,
		Records: records,
		Launcher: &launch.Process{Executable: exe, Store: f.store, StateDir: state, StallInterval: stall, LockWait: wait, OpenPresentation: func(ctx context.Context) (*launch.Presentation, error) {
			return artisanPresentation(ctx, cfg, f.store, state, a.Stderr)
		}},
		Prompter: baselinePrompter(cfg, cfgDir, releaseBaseline, skills),
		Outbox:   outbox,
		// The task's bound revision is resolved by its name, not the repository's current workflow setting.
		Workflow: func(rev workflow.Revision) (*workflow.Definition, error) {
			return boundWorkflow(filepath.Join(cfgDir, "workflows"), cfg, r, rev)
		},
		Config: cfg, StandardsRevision: standards, CheckSettings: runtimeSettings,
		DocumentDir: filepath.Join(state, "documents"), StateDir: state, StallInterval: stall, ChecksRefresh: refresh, Forge: fg,
		Workspaces: ws,
		// The task's base is read from the repository's own refs, which the worktree custody fetched, and the
		// classifier runs over the same repository.
		Base:    func(string) (string, error) { return git.Program{}.Base(ctx, r.Path, interp.TaskBranch(ev.Task)) },
		Rebases: git.Program{},
		Hold:    func() (bool, error) { return withRecords(records, true, nil, held) },
		Caps:    store.AttemptCaps{MaxRunning: rails.MaxRunningAttempts, MaxPerDay: rails.MaxAttemptsPerDay, Hold: true},
	}
	n, err := e.Advance(ctx, ev)
	if err != nil {
		return n, err
	}
	// Where the task now stands, for director status, written while the task lock is still held.
	b, err := json.Marshal(nextOf(n))
	if err != nil {
		return n, err
	}
	return n, atomicfile.Write(filepath.Join(taskDir, nextFile), append(b, '\n'))
}

// nextFile is what the task's last advance left it waiting for, beside its task lock (docs/running.md).
const nextFile = "next.json"

func nextOf(n interp.Next) next {
	return next{Wait: n.Wait, Step: n.Step, Attempt: n.Attempt, Decision: n.Decision, Position: n.Position}
}

// baselinePrompter renders from baseline/ beside the config file, falling back file by file to the installed
// release baseline, and the release skills root (docs/running.md). A Director stage is rendered with the Director's
// role, as its session is (docs/director.md#start).
func baselinePrompter(cfg *config.Config, cfgDir, release, skills string) interp.PromptFunc {
	return func(stage render.Stage, inputs []render.Input) (render.Rendered, error) {
		base := filepath.Join(cfgDir, "baseline")
		b := render.Baseline{Role: baselineSource(base, release, "roles/artisan.md"), Guide: baselineSource(base, release, "stages/"+stage.Step.Type+".md")}
		if stage.Step.Actor() == workflow.DirectorActor {
			b.Role = baselineSource(base, release, "roles/director.md")
			if d := cfg.Director; d != nil && d.Role != "" {
				b.Role = render.Source{Name: d.Role, Path: filepath.Join(cfgDir, filepath.FromSlash(d.Role)), Root: "copy"}
			}
		} else if name := stage.Step.Profile(); name != "" {
			if p, err := cfg.Profile(name); err == nil && p.Role != "" {
				b.Role = render.Source{Name: p.Role, Path: filepath.Join(cfgDir, filepath.FromSlash(p.Role)), Root: "copy"}
			}
		}
		files, err := filepath.Glob(filepath.Join(base, "standards", "*.md"))
		if err != nil {
			return render.Rendered{}, err
		}
		slices.Sort(files)
		for _, f := range files {
			b.Standards = append(b.Standards, render.Source{Name: "standards/" + filepath.Base(f), Path: f, Root: "copy"})
		}
		return render.Prompt(stage, inputs, b, []render.SkillRoot{{Name: "release", Dir: skills}})
	}
}

// baselineSource is the named baseline file from the copy's baseline directory unless it does not exist there, else from
// the release's; a file in neither, or a copy file that cannot be read, is left to the renderer to refuse.
func baselineSource(copyDir, release, name string) render.Source {
	path := filepath.Join(copyDir, filepath.FromSlash(name))
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return render.Source{Name: name, Path: filepath.Join(release, filepath.FromSlash(name)), Root: "release"}
	}
	return render.Source{Name: name, Path: path, Root: "copy"}
}

// storeRecords is the engine's Records and Outbox over the store file at path, opened for each operation
// and closed before the next, so an attempt's launcher can write the store while an advance runs. Each open
// waits at most wait for the store's lock, storeWait when zero; an advance sets its copy's lock wait.
type storeRecords struct {
	path string
	wait time.Duration
}

func withStore[T any](path string, readOnly bool, fn func(*store.Store) (T, error)) (T, error) {
	return withStoreClock(path, readOnly, nil, fn)
}

// withStoreClock is withStore with the store's clock; nil is wall time.
func withStoreClock[T any](path string, readOnly bool, now func() time.Time, fn func(*store.Store) (T, error)) (T, error) {
	return withRecords(storeRecords{path: path}, readOnly, now, fn)
}

// withRecords opens p's store for fn with the store's clock, nil being wall time, waiting p's wait for its lock.
func withRecords[T any](p storeRecords, readOnly bool, now func() time.Time, fn func(*store.Store) (T, error)) (T, error) {
	wait := p.wait
	if wait == 0 {
		wait = storeWait
	}
	s, err := store.Open(p.path, store.Options{ReadOnly: readOnly, RequireStore: true, LockTimeout: wait, Now: now})
	if err != nil {
		var zero T
		return zero, err
	}
	v, err := fn(s)
	return v, errors.Join(err, s.Close())
}

func (p storeRecords) Task(repo, id string) (store.Task, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.Task, error) { return s.Task(repo, id) })
}

func (p storeRecords) TaskHistory(repo, id string) (store.TaskHistory, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory(repo, id) })
}

func (p storeRecords) Attempts(repo, id string) ([]store.StageAttempt, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts(repo, id) })
}

func (p storeRecords) Documents(repo, id string) ([]store.DocumentRef, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.DocumentRef, error) { return s.Documents(repo, id) })
}

func (p storeRecords) Decisions(repo, id string) ([]store.Decision, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.Decision, error) { return s.Decisions(repo, id) })
}

func (p storeRecords) Rulings(repo, id string) ([]store.Ruling, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.Ruling, error) { return s.Rulings(repo, id) })
}

func (p storeRecords) FullReviews(repo, id string) ([]store.FullReview, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.FullReview, error) { return s.FullReviews(repo, id) })
}

func (p storeRecords) DependencyOverride(repo, id string) (store.DependencyOverride, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.DependencyOverride, error) { return s.DependencyOverride(repo, id) })
}

func (p storeRecords) StartAttempt(m, repo, id, step string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.StartAttempt(m, repo, id, step) })
}

func (p storeRecords) StartCappedAttempt(m, repo, id, step string, caps store.AttemptCaps) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.StartCappedAttempt(m, repo, id, step, caps) })
}

func (p storeRecords) StartDirectorAttempt(m, repo, id, step string, hold bool) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.StartDirectorAttempt(m, repo, id, step, hold) })
}

func (p storeRecords) StartQueue() ([]store.QueuedStart, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.QueuedStart, error) { return s.StartQueue() })
}

func (p storeRecords) FinishAttempt(m, repo, id, step string, n int, o store.AttemptOutcome) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.FinishAttempt(m, repo, id, step, n, o) })
}

func (p storeRecords) RejectAttempt(m, repo, id, step string, n int, reason string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.RejectAttempt(m, repo, id, step, n, reason) })
}

func (p storeRecords) PutDocument(m string, d store.DocumentRef) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.PutDocument(m, d) })
}

func (p storeRecords) OpenDecision(m string, d store.Decision) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.OpenDecision(m, d) })
}

func (p storeRecords) ContestRuling(m, repo, id, doc, finding, by string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.ContestRuling(m, repo, id, doc, finding, by) })
}

func (p storeRecords) ConsumeFullReview(m, repo, id string, n int, by string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.ConsumeFullReview(m, repo, id, n, by) })
}

func (p storeRecords) SetTaskState(m, repo, id string, state store.TaskState) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.SetTaskState(m, repo, id, state) })
}

var _ interp.MergeQueue = storeRecords{}

func (p storeRecords) MergeQueue(repo string) (store.MergeQueue, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.MergeQueue, error) { return s.MergeQueue(repo) })
}

func (p storeRecords) JoinMergeQueue(m, repo, id, authorization, record, pr string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) {
		return s.JoinMergeQueue(m, repo, id, authorization, record, pr)
	})
}

func (p storeRecords) LeaveMergeQueue(m, repo, id string, keepReady bool) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.LeaveMergeQueue(m, repo, id, keepReady) })
}

func (p storeRecords) ClaimMergeFront(m, repo, id string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.ClaimMergeFront(m, repo, id) })
}

func (p storeRecords) ReleaseMergeFront(m, repo, id string) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.ReleaseMergeFront(m, repo, id) })
}

func (p storeRecords) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) {
		return (&messaging.StoreMailbox{Store: s}).Send(to, e)
	})
}

var (
	_ interp.Records = storeRecords{}
	_ interp.Outbox  = storeRecords{}
)
