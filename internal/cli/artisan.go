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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// Exit codes of `artisan start` beyond 0 (succeeded), 1 (error or rejected
// result) and 2 (usage).
const (
	exitNoResultYet  = 3
	exitResultFailed = 4
	exitStopped      = 5
)

// defaultStopTimeout bounds `artisan stop`'s wait for the stop ladder.
const defaultStopTimeout = time.Minute

// resultPoll is how often `artisan start` checks the result file.
const resultPoll = 200 * time.Millisecond

// fakeArtisan runs the fake Artisan: `guildhall fake-artisan --task --attempt
// --prompt --result`.
func (a *App) fakeArtisan(args []string) int {
	if _, err := fake.ParseArgs(args); err != nil {
		return a.usageError("fake-artisan: " + err.Error())
	}
	return fake.Main(args, a.Stdin, a.Stdout, a.Stderr)
}

// claudeArtisan runs the Claude adapter's wrapper: `guildhall claude-artisan
// --task --attempt --prompt --result [--claude]`.
func (a *App) claudeArtisan(args []string) int {
	if _, err := claude.ParseArgs(args); err != nil {
		return a.usageError("claude-artisan: " + err.Error())
	}
	return claude.Main(args, a.Stdout, a.Stderr)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// artisan dispatches `guildhall artisan <command>`.
func (a *App) artisan(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "start":
			return a.artisanStart(args[1:])
		case "run":
			// Guildhall is pre-release, so the old name gets no alias.
			return a.usageError("artisan run is now artisan start")
		case "stop", "status", "resume":
			return a.artisanOwned(args[0], args[1:])
		case "harness":
			return a.artisanHarness(args[1:])
		}
	}
	return a.usageError("artisan: the commands are start, stop, status, resume and harness")
}

// attemptFlags name an owned attempt (docs/artisan.md#commands); a copy defaults the store and state.
type attemptFlags struct {
	store, state, repo, task, step, attempt string
	copy                                    copyFlags
}

func (f *attemptFlags) register(fl *flag.FlagSet) {
	f.copy.register(fl)
	fl.StringVar(&f.store, "store", "", "Director store file")
	fl.StringVar(&f.state, "state", "", "state directory")
	fl.StringVar(&f.repo, "repo", "", "repository id")
	// Guildhall is pre-release, so the old spelling gets no alias.
	fl.Func("repository", "renamed --repo", func(string) error { return errors.New("--repository is now --repo") })
	fl.StringVar(&f.task, "task", "", "task id")
	fl.StringVar(&f.step, "step", "", "step id")
	fl.StringVar(&f.attempt, "attempt", "", "attempt number")
}

// parse fills the paths from the copy, if one is selected, leaving it in f.copy, and returns the attempt the flags name.
func (f *attemptFlags) parse(getenv func(string) string) (launch.Attempt, error) {
	if err := f.copy.apply(getenv, &f.store, nil, &f.state); err != nil {
		return launch.Attempt{}, err
	}
	n, err := strconv.Atoi(f.attempt)
	switch {
	case f.store == "" || f.state == "" || f.repo == "" || f.task == "" || f.step == "":
		return launch.Attempt{}, errors.New("--store, --state, --repo, --task, --step and --attempt are required, or --copy, --repo, --task, --step and --attempt")
	case err != nil || n < 1:
		return launch.Attempt{}, fmt.Errorf("--attempt %q must be a positive attempt number", f.attempt)
	}
	state, err := filepath.Abs(f.state)
	return launch.Attempt{Store: f.store, StateDir: state, Key: artisan.AttemptKey{Repository: f.repo, TaskID: f.task, Step: f.step, Attempt: n}}, err
}

// artisanOwned runs `artisan stop`, `status` or `resume` and prints the
// result as one JSON line: 0 when stop proves the attempt not running, when
// resume finds nothing to resume or settles the attempt, and whenever status
// classifies it; 1 on an error or an attempt that may still run.
func (a *App) artisanOwned(cmd string, args []string) int {
	fl := flag.NewFlagSet("artisan "+cmd, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f attemptFlags
	f.register(fl)
	timeout := fl.Duration("timeout", defaultStopTimeout, "how long stop waits for the stop ladder")
	if err := fl.Parse(args); err != nil {
		return a.usageError("artisan " + cmd + ": " + err.Error())
	}
	at, err := f.parse(a.Getenv)
	switch {
	case err != nil:
		return a.usageError("artisan " + cmd + ": " + err.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("artisan %s: unexpected argument %q", cmd, fl.Arg(0)))
	case *timeout <= 0:
		return a.usageError("artisan " + cmd + ": --timeout must be positive")
	}
	if _, err := os.Stat(at.Store); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("artisan %s: %w", cmd, noStore(at.Store)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var out any
	switch cmd {
	case "stop":
		out, err = launch.Stop(ctx, at)
	case "status":
		out, err = launch.Status(ctx, at)
	case "resume":
		out, err = launch.Resume(ctx, at)
	}
	if !errors.Is(err, launch.ErrNotStopped) && err != nil && cmd != "resume" {
		return a.fail(err)
	}
	if code := a.printJSON(out); code != 0 {
		return code
	}
	if err != nil {
		a.notef("guildhall: %v\n", err)
		return 1
	}
	return 0
}

// printJSON prints v as one JSON line.
func (a *App) printJSON(v any) int {
	line, err := json.Marshal(v)
	if err != nil {
		return a.fail(err)
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s\n", line); err != nil {
		return 1
	}
	return 0
}

// artisanStart launches one Artisan attempt: owned by this process as its
// launcher, or with --herdr-session in a new tab of a named Herdr session,
// waiting for its result file.
func (a *App) artisanStart(args []string) int {
	fl := flag.NewFlagSet("artisan start", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var (
		adapterName, prompt, result, workdir string
		session, workspace, label            string
		herdrExec                            stringList
		profile                              string
		settings                             adapter.Settings
		timeout, stall                       time.Duration
		stopOnTimeout, advance, paneLaunch   bool
		id                                   adapter.Identity
		owned                                attemptFlags
	)
	owned.register(fl)
	fl.StringVar(&adapterName, "adapter", "", "runtime adapter: fake, or claude for an owned attempt")
	fl.StringVar(&prompt, "prompt", "", "prompt file")
	fl.StringVar(&result, "result", "", "result file")
	fl.StringVar(&workdir, "workdir", "", "working directory")
	fl.DurationVar(&timeout, "timeout", 0, "how long to wait for the result; zero is no limit for an owned attempt and 30m in a tab")
	fl.StringVar(&session, "herdr-session", "", "named Herdr session")
	fl.StringVar(&workspace, "herdr-workspace", "", "Herdr workspace id for the tab")
	fl.Var(&herdrExec, "herdr-exec", "Herdr program, then leading arguments (repeat per word)")
	fl.StringVar(&label, "tab-label", "", "tab label")
	fl.BoolVar(&paneLaunch, "pane-launch", false, "internal pane bridge: detach ownership and stream the launcher log")
	fl.BoolVar(&advance, "advance", false, "advance the task over the copy once the attempt is recorded")
	fl.DurationVar(&stall, "stall-interval", 0, "send the Director worker-stalled when the worker is silent this long; zero observes nothing")
	fl.StringVar(&profile, "profile", "", "profile declared by the Director copy")
	fl.StringVar(&settings.Model, "model", "", "model the runtime runs the attempt on; empty is its default")
	fl.StringVar(&settings.Effort, "effort", "", "effort the runtime runs the attempt at; empty is its default")
	fl.BoolVar(&stopOnTimeout, "stop-on-timeout", false, "stop the owned Windows worker, or request Ctrl+C on other platforms, when the wait times out")
	if err := fl.Parse(args); err != nil {
		return a.usageError("artisan start: " + err.Error())
	}
	id.TaskID, id.AttemptID = owned.task, owned.attempt
	switch {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("artisan start: unexpected argument %q", fl.Arg(0)))
	case !slices.Contains(runtimes.Names(), adapterName):
		return a.usageError(fmt.Sprintf("artisan start: --adapter %q: the adapters are %s", adapterName, strings.Join(runtimes.Names(), ", ")))
	case adapterName != adapter.FakeName && session != "":
		return a.usageError(fmt.Sprintf("artisan start: --adapter %s runs only as an owned attempt, not in a Herdr tab", adapterName))
	case timeout < 0:
		return a.usageError("artisan start: --timeout must not be negative")
	case stall < 0:
		return a.usageError("artisan start: --stall-interval must not be negative")
	case session == "":
		if result != "" || workspace != "" || len(herdrExec) > 0 || label != "" || stopOnTimeout {
			return a.usageError("artisan start: --result, --herdr-workspace, --herdr-exec, --tab-label and --stop-on-timeout need --herdr-session")
		}
		at, err := owned.parse(a.Getenv)
		if err != nil || prompt == "" || workdir == "" {
			return a.usageError(fmt.Sprintf("artisan start: --prompt and --workdir are required: %v", err))
		}
		if advance && owned.copy.dir == "" {
			return a.usageError("artisan start: --advance advances the task over a copy, so it needs --copy or " + copyEnvVar)
		}
		if _, err := os.Stat(at.Store); errors.Is(err, fs.ErrNotExist) {
			return a.fail(fmt.Errorf("artisan start: %w", noStore(at.Store)))
		}
		var advanceArgv []string
		if advance {
			exe, err := a.Executable()
			if err != nil {
				return a.fail(err)
			}
			dir, err := filepath.Abs(owned.copy.dir)
			if err != nil {
				return a.fail(err)
			}
			advanceArgv = []string{exe, "--copy", dir}
		}
		if paneLaunch {
			exe, err := a.Executable()
			if err != nil {
				return a.fail(err)
			}
			argv := []string{exe, "artisan", "start"}
			for _, arg := range args {
				if arg != "--pane-launch" && !strings.HasPrefix(arg, "--pane-launch=") {
					argv = append(argv, arg)
				}
			}
			if err := launch.Present(argv, os.Environ(), filepath.Join(at.Dir(), launch.LogFile), a.Stderr); err != nil {
				return a.fail(err)
			}
			return 0
		}
		return a.runOwned(at, adapterName, prompt, workdir, owned.copy.dir, profile, settings, timeout, stall, advanceArgv)
	case paneLaunch || owned.store != "" || owned.state != "" || owned.repo != "" || owned.step != "" || owned.copy.dir != "" || advance || stall != 0 || profile != "" || settings != adapter.Settings{}:
		return a.usageError("artisan start: --store, --state, --repo, --step, --copy, --advance, --stall-interval, --profile, --model and --effort do not apply in a Herdr tab")
	case prompt == "" || result == "" || workdir == "":
		return a.usageError("artisan start: --task, --attempt, --prompt, --result and --workdir are required")
	case (session == "") != (workspace == ""):
		return a.usageError("artisan start: --herdr-session and --herdr-workspace go together")
	case timeout == 0:
		timeout = 30 * time.Minute
	}

	spec := adapter.Spec{Identity: id}
	for _, p := range []struct {
		dst *string
		src string
	}{{&spec.PromptFile, prompt}, {&spec.ResultFile, result}, {&spec.WorkDir, workdir}} {
		abs, err := filepath.Abs(p.src)
		if err != nil {
			return a.fail(err)
		}
		*p.dst = abs
	}
	if err := spec.Validate(); err != nil {
		return a.fail(err)
	}
	if st, err := os.Stat(spec.PromptFile); err != nil || !st.Mode().IsRegular() {
		return a.fail(fmt.Errorf("prompt file %s is not a readable file", spec.PromptFile))
	}
	if _, err := os.Lstat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("result file %s already exists; a result is never replaced, so use a new attempt", spec.ResultFile))
	}
	cmd, err := a.runtimeCommand(adapterName, spec)
	if err != nil {
		return a.fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if len(herdrExec) == 0 {
		herdrExec = stringList{"herdr"}
	}
	if label == "" {
		label = "artisan " + id.TaskID + "/" + id.AttemptID
	}
	return a.runInTab(ctx, spec, cmd, herdr.Options{Session: session, Command: herdrExec}, workspace, label, stopOnTimeout)
}

// runOwned runs attempt at as its launcher (launch.Run): the named adapter's
// Artisan runs in a containment unit with this process's terminal, its output on
// stderr so stdout carries only the report, and the exit code is the store
// outcome: 0 succeeded, 4 failed, 5 stopped, and 1 on any error. A non-nil
// advance is launch.Config.Advance, the task's advance once the attempt is recorded. A positive stall is
// launch.Config.StallInterval, its sends going through the store and waking the Director as the engine's outbox does.
// The runtime runs the attempt on settings, and one it cannot honour (adapter.ErrUnsupportedSetting) is exit 1
// before the attempt is owned.
func (a *App) runOwned(at launch.Attempt, adapterName, prompt, workdir, copyDir, profile string, settings adapter.Settings, timeout, stall time.Duration, advance []string) int {
	spec := adapter.Spec{Identity: at.Identity(), ResultFile: at.Files().ResultFile, Copy: copyDir, Profile: profile, Settings: settings}
	for _, p := range []struct {
		dst *string
		src string
	}{{&spec.PromptFile, prompt}, {&spec.WorkDir, workdir}} {
		abs, err := filepath.Abs(p.src)
		if err != nil {
			return a.fail(err)
		}
		*p.dst = abs
	}
	if spec.Copy != "" {
		abs, err := filepath.Abs(spec.Copy)
		if err != nil {
			return a.fail(err)
		}
		spec.Copy = abs
	}
	cmd, err := a.runtimeCommand(adapterName, spec)
	if err != nil {
		return a.fail(err)
	}
	ctx := context.Background()
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, artisan.StopSignals...)
	defer signal.Stop(signals)
	rep, err := launch.Run(ctx, launch.Config{Attempt: at, Command: cmd, Stdin: a.Stdin, Stdout: a.Stderr, Stderr: a.Stderr, Signals: signals, Advance: advance, ResultTimeout: timeout,
		// A failed wake is not written to stderr: that is the worker's output file, so the line would restart its
		// stall clock. The message stays stored and the next advance's replay tries the wake again.
		StallInterval: stall, Outbox: wakingOutbox{storeRecords{path: at.Store}, a.directorPane(), io.Discard}})
	if rep.Outcome.State != "" {
		if code := a.printJSON(rep); code != 0 {
			return code
		}
	}
	if err != nil {
		return a.fail(err)
	}
	switch rep.AttemptOutcome {
	case store.AttemptSucceeded:
		return 0
	case store.AttemptStopped:
		return exitStopped
	}
	return exitResultFailed
}

// runtimeCommand is the command of the runtime called name (runtimes.Lookup) over this guildhall executable.
func (a *App) runtimeCommand(name string, spec adapter.Spec) (adapter.Command, error) {
	exe, err := a.Executable()
	if err != nil {
		return adapter.Command{}, err
	}
	rt, err := runtimes.Lookup(name, exe)
	if err != nil {
		return adapter.Command{}, err
	}
	return rt.Command(spec)
}

// runInTab runs the Artisan in a new tab of the named Herdr session. The tab is
// presentation only: its closing never counts as the attempt finishing, so the
// wait ends only on a result file or the timeout.
func (a *App) runInTab(ctx context.Context, spec adapter.Spec, cmd adapter.Command, opts herdr.Options, workspace, label string, stopOnTimeout bool) int {
	client, err := herdr.New(opts)
	if err != nil {
		return a.fail(err)
	}
	st, err := client.Status(ctx)
	if err != nil {
		return a.fail(err)
	}
	var owned *adapter.OwnedWorker
	var stopper workerStopper
	if stopOnTimeout && runtime.GOOS == "windows" {
		owned, err = adapter.NewOwnedWorker(spec, cmd.Argv[0])
		if err != nil {
			return a.fail(err)
		}
		defer func() { _ = owned.Close() }()
		stopper = owned
		cmd.Argv = append(cmd.Argv, "--worker-control", owned.Address(), "--worker-token", owned.Token())
	}
	launcher := adapter.TabLauncher{Host: adapter.NewHerdrTabs(client), WorkspaceID: workspace}
	run, err := launcher.Start(ctx, label, cmd)
	if err != nil {
		return a.fail(err)
	}
	if owned != nil {
		if err := owned.Accept(ctx); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			stopErr := stopper.Stop(stopCtx)
			return a.fail(fmt.Errorf("worker ownership handshake failed: %w; stop verification: %v", err, stopErr))
		}
	}
	a.notef("guildhall: artisan started in Herdr %s session %q, workspace %s, tab %s, pane %s\n",
		st.ServerVersion, client.Session(), run.Tab.WorkspaceID, run.Tab.TabID, run.Tab.PaneID)
	r, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Poll: resultPoll})
	if errors.Is(err, adapter.ErrNoResultYet) {
		// Whether the Artisan still runs is unknown here: the tab may have been
		// closed by someone else, which proves nothing about the attempt.
		state := "guildhall did not stop the artisan"
		if stopOnTimeout {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if stopper != nil {
				if stopErr := stopper.Stop(stopCtx); stopErr != nil {
					state = fmt.Sprintf("stopping the owned artisan failed; termination is unverified: %v", stopErr)
				} else {
					state = "guildhall verified termination of the owned artisan; late results are rejected"
				}
			} else if stopErr := run.Stop(stopCtx); stopErr != nil {
				state = fmt.Sprintf("interrupting the artisan failed: %v", stopErr)
			} else {
				state = "guildhall sent Ctrl+C to the artisan; termination is unverified"
			}
		}
		a.notef("guildhall: no result yet (%v); %s; guildhall did not close tab %s; the attempt is not finished\n",
			err, state, run.Tab.TabID)
		return exitNoResultYet
	}
	return a.report(r, err)
}

// report prints a validated result as one JSON line, or the rejection.
func (a *App) report(r adapter.Result, err error) int {
	if err != nil {
		a.notef("guildhall: artisan result rejected: %v\n", err)
		return 1
	}
	line, err := json.Marshal(r)
	if err != nil {
		return a.fail(err)
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s\n", line); err != nil {
		return 1
	}
	if r.Status != adapter.StatusSucceeded {
		return exitResultFailed
	}
	return 0
}

// notef writes a diagnostic line to stderr. The exit code carries the outcome
// if stderr is unwritable.
func (a *App) notef(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format, args...)
}

// workerStopper stops an owned Artisan process. A nil error proves the process
// terminated, not merely that an interrupt was delivered; it neither deletes
// work nor finishes the task.
type workerStopper interface {
	Stop(context.Context) error
}
