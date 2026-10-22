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
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
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

// next is what `task start`, `step done`, `step finish` and `step retry` print: interp.Next as JSON.
type next struct {
	Wait     interp.Wait `json:"wait"`
	Step     string      `json:"step"`
	Attempt  int         `json:"attempt,omitempty"`
	Decision string      `json:"decision,omitempty"`
}

// taskStart runs `guildhall task start`: one advance with task-created, or with --decision,
// decision-answered (docs/running.md).
func (a *App) taskStart(args []string) int {
	fl := flag.NewFlagSet("task start", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	decision := fl.String("decision", "", "answered decision id")
	if err := parseInterspersed(fl, args); err != nil {
		return a.taskUsage("task start: " + err.Error())
	}
	if err := f.resolve(a.Getenv); err != nil {
		return a.taskUsage("task start: " + err.Error())
	}
	if fl.NArg() != 1 || !workflow.ValidID(fl.Arg(0)) {
		return a.taskUsage("task start takes one task id")
	}
	ev := interp.Event{Kind: interp.TaskCreated, Repository: f.repo, Task: fl.Arg(0)}
	if *decision != "" {
		ev.Kind, ev.Decision = interp.DecisionAnswered, *decision
	}
	if err := newWorkAdmitted(f.store, f.repo, ev.Task); err != nil {
		return a.fail(fmt.Errorf("task start: %w", err))
	}
	return a.advanced(a.advance(f, ev))
}

// newWorkAdmitted refuses to start a task with no attempt yet while the newest Director session is recovering;
// a task already under way advances (docs/director.md#ready). A missing store is left to the advance to refuse.
func newWorkAdmitted(path, repo, task string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	_, err := withStore(path, true, func(s *store.Store) (struct{}, error) {
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

// step runs `guildhall step done|finish|retry`.
func (a *App) step(args []string) int {
	switch {
	case len(args) > 0 && args[0] == "done":
		return a.stepDone(args[1:])
	case len(args) > 0 && args[0] == "finish":
		return a.stepFinish(args[1:])
	case len(args) > 0 && args[0] == "retry":
		return a.stepRetry(args[1:])
	}
	return a.usageError("step: the commands are done, finish and retry")
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
	return a.advanced(a.advance(f, interp.Event{Kind: interp.AttemptRetried, Repository: f.repo, Task: *task, Step: *step, Attempt: n, Reason: *reason}))
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
	if _, err := adapter.ReadResult(file, adapter.Identity{TaskID: k.TaskID, AttemptID: k.AttemptID()}); err != nil {
		return a.fail(fmt.Errorf("step done: %w", err))
	}
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

// storeWait bounds each record operation's wait for the store's lock, which launchers take briefly meanwhile.
const storeWait = 30 * time.Second

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
	r, ok := cfg.Repository(f.repo)
	if !ok {
		return interp.Next{}, fmt.Errorf("config %s has no repository %q", f.config, f.repo)
	}
	cfgDir := filepath.Dir(f.config)
	def, err := effectiveWorkflow(filepath.Join(cfgDir, "workflows"), r)
	if err != nil {
		return interp.Next{}, fmt.Errorf("repository %s: %w", f.repo, err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), storeWait)
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
	e := &interp.Engine{
		Records:  storeRecords(f.store),
		Launcher: &launch.Process{Executable: exe, Store: f.store, StateDir: state, Copy: copyDir, StallInterval: stall},
		Prompter: baselinePrompter(cfg, cfgDir, releaseBaseline, skills),
		Outbox:   wakingOutbox{storeRecords(f.store), a.directorPane(), a.Stderr},
		Workflow: func(workflow.Revision) (*workflow.Definition, error) { return def, nil },
		Config:   cfg, DefaultAdapter: interp.FakeAdapter, StandardsRevision: standards,
		DocumentDir: filepath.Join(state, "documents"), StateDir: state, StallInterval: stall, ChecksRefresh: refresh, Forge: fg,
		Workspaces: ws,
	}
	// An interrupt ends the advance and releases the task lock; the next advance replays it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	context.AfterFunc(ctx, stop)
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
	return next{Wait: n.Wait, Step: n.Step, Attempt: n.Attempt, Decision: n.Decision}
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

// storeRecords is the engine's Records and Outbox over the store file it names, opened for each operation
// and closed before the next, so an attempt's launcher can write the store while an advance runs.
type storeRecords string

func withStore[T any](path string, readOnly bool, fn func(*store.Store) (T, error)) (T, error) {
	s, err := store.Open(string(path), store.Options{ReadOnly: readOnly, RequireStore: true, LockTimeout: storeWait})
	if err != nil {
		var zero T
		return zero, err
	}
	v, err := fn(s)
	return v, errors.Join(err, s.Close())
}

func (p storeRecords) Task(repo, id string) (store.Task, error) {
	return withStore(string(p), true, func(s *store.Store) (store.Task, error) { return s.Task(repo, id) })
}

func (p storeRecords) Attempts(repo, id string) ([]store.StageAttempt, error) {
	return withStore(string(p), true, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts(repo, id) })
}

func (p storeRecords) Documents(repo, id string) ([]store.DocumentRef, error) {
	return withStore(string(p), true, func(s *store.Store) ([]store.DocumentRef, error) { return s.Documents(repo, id) })
}

func (p storeRecords) Decisions(repo, id string) ([]store.Decision, error) {
	return withStore(string(p), true, func(s *store.Store) ([]store.Decision, error) { return s.Decisions(repo, id) })
}

func (p storeRecords) Rulings(repo, id string) ([]store.Ruling, error) {
	return withStore(string(p), true, func(s *store.Store) ([]store.Ruling, error) { return s.Rulings(repo, id) })
}

func (p storeRecords) FullReviews(repo, id string) ([]store.FullReview, error) {
	return withStore(string(p), true, func(s *store.Store) ([]store.FullReview, error) { return s.FullReviews(repo, id) })
}

func (p storeRecords) StartAttempt(m, repo, id, step string) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.StartAttempt(m, repo, id, step) })
}

func (p storeRecords) FinishAttempt(m, repo, id, step string, n int, o store.AttemptOutcome) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.FinishAttempt(m, repo, id, step, n, o) })
}

func (p storeRecords) PutDocument(m string, d store.DocumentRef) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.PutDocument(m, d) })
}

func (p storeRecords) OpenDecision(m string, d store.Decision) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.OpenDecision(m, d) })
}

func (p storeRecords) ContestRuling(m, repo, id, doc, finding, by string) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.ContestRuling(m, repo, id, doc, finding, by) })
}

func (p storeRecords) ConsumeFullReview(m, repo, id string, n int, by string) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.ConsumeFullReview(m, repo, id, n, by) })
}

func (p storeRecords) SetTaskState(m, repo, id string, state store.TaskState) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.SetTaskState(m, repo, id, state) })
}

func (p storeRecords) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) {
		return (&messaging.StoreMailbox{Store: s}).Send(to, e)
	})
}

var (
	_ interp.Records = storeRecords("")
	_ interp.Outbox  = storeRecords("")
)
