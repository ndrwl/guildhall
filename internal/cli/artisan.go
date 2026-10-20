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
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/herdr"
)

// Exit codes of `artisan run` beyond 0 (succeeded), 1 (error or rejected
// result) and 2 (usage).
const (
	exitNoResultYet  = 3
	exitResultFailed = 4
)

// resultPoll is how often `artisan run` checks the result file.
const resultPoll = 200 * time.Millisecond

// fakeArtisan runs the fake Artisan: `guildhall fake-artisan --task --attempt
// --prompt --result`.
func (a *App) fakeArtisan(args []string) int {
	parsed, err := fake.ParseArgs(args)
	if err != nil {
		return a.usageError("fake-artisan: " + err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := fake.Run(ctx, parsed, a.Stdout, fake.Sleep); err != nil {
		a.notef("guildhall: fake-artisan: %v\n", err)
		return 1
	}
	return 0
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// artisan dispatches `guildhall artisan <command>`.
func (a *App) artisan(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		return a.usageError("artisan: the only command is run")
	}
	return a.artisanRun(args[1:])
}

// artisanRun launches one Artisan attempt, as a direct child process or in a
// new tab of a named Herdr session, and waits for its result file.
func (a *App) artisanRun(args []string) int {
	fl := flag.NewFlagSet("artisan run", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var (
		adapterName, prompt, result, workdir string
		session, workspace, label            string
		herdrExec                            stringList
		timeout                              time.Duration
		stopOnTimeout                        bool
		id                                   adapter.Identity
	)
	fl.StringVar(&adapterName, "adapter", "", "runtime adapter (only fake)")
	fl.StringVar(&id.TaskID, "task", "", "task id")
	fl.StringVar(&id.AttemptID, "attempt", "", "attempt id")
	fl.StringVar(&prompt, "prompt", "", "prompt file")
	fl.StringVar(&result, "result", "", "result file")
	fl.StringVar(&workdir, "workdir", "", "working directory")
	fl.DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait for the result")
	fl.StringVar(&session, "herdr-session", "", "named Herdr session")
	fl.StringVar(&workspace, "herdr-workspace", "", "Herdr workspace id for the tab")
	fl.Var(&herdrExec, "herdr-exec", "Herdr program, then leading arguments (repeat per word)")
	fl.StringVar(&label, "tab-label", "", "tab label")
	fl.BoolVar(&stopOnTimeout, "stop-on-timeout", false, "stop the owned Windows worker, or request Ctrl+C on other platforms, when the wait times out")
	if err := fl.Parse(args); err != nil {
		return a.usageError("artisan run: " + err.Error())
	}
	switch {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("artisan run: unexpected argument %q", fl.Arg(0)))
	case adapterName != "fake":
		return a.usageError(fmt.Sprintf("artisan run: --adapter %q: the only adapter is fake", adapterName))
	case prompt == "" || result == "" || workdir == "":
		return a.usageError("artisan run: --task, --attempt, --prompt, --result and --workdir are required")
	case (session == "") != (workspace == ""):
		return a.usageError("artisan run: --herdr-session and --herdr-workspace go together")
	case session == "" && (len(herdrExec) > 0 || label != "" || stopOnTimeout):
		return a.usageError("artisan run: --herdr-exec, --tab-label and --stop-on-timeout need --herdr-session")
	case timeout <= 0:
		return a.usageError("artisan run: --timeout must be positive")
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
	exe, err := a.Executable()
	if err != nil {
		return a.fail(err)
	}
	cmd, err := fake.Adapter{Executable: exe}.Command(spec)
	if err != nil {
		return a.fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if session == "" {
		return a.runChild(ctx, spec, cmd)
	}
	if len(herdrExec) == 0 {
		herdrExec = stringList{"herdr"}
	}
	if label == "" {
		label = "artisan " + id.TaskID + "/" + id.AttemptID
	}
	return a.runInTab(ctx, spec, cmd, herdr.Options{Session: session, Command: herdrExec}, workspace, label, stopOnTimeout)
}

// runChild runs the Artisan as a direct child process. The child's output goes
// to stderr so stdout carries only the recorded result. On timeout or
// interruption without a result, it stops the otherwise unsupervised child.
func (a *App) runChild(ctx context.Context, spec adapter.Spec, cmd adapter.Command) int {
	p, err := adapter.StartProcess(cmd, a.Stderr, a.Stderr)
	if err != nil {
		return a.fail(err)
	}
	r, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Poll: resultPoll, Exited: p.Exited()})
	if errors.Is(err, adapter.ErrNoResultYet) {
		if stopErr := p.Stop(); stopErr != nil {
			a.notef("guildhall: stopping the artisan process: %v\n", stopErr)
		}
		<-p.Exited()
		a.notef("guildhall: no result yet (%v); the artisan process was stopped and the attempt is not finished\n", err)
		return exitNoResultYet
	}
	// Let the child finish its own output before printing the result.
	select {
	case <-p.Exited():
	case <-ctx.Done():
	}
	return a.report(r, err)
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
	var stopper adapter.WorkerStopper
	if stopOnTimeout && runtime.GOOS == "windows" {
		owned, err = adapter.NewOwnedWorker(spec, cmd.Argv[0])
		if err != nil {
			return a.fail(err)
		}
		defer func() { _ = owned.Close() }()
		stopper = owned
		cmd.Argv = append(cmd.Argv, "--worker-control", owned.Address(), "--worker-token", owned.Token())
	}
	launcher := adapter.TabLauncher{Host: client, WorkspaceID: workspace}
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
		st.ServerVersion, client.Session(), run.Tab.WorkspaceID(), run.Tab.ID(), run.Tab.PaneID())
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
			err, state, run.Tab.ID())
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

func (a *App) fail(err error) int {
	a.notef("guildhall: %v\n", err)
	return 1
}

// notef writes a diagnostic line to stderr. The exit code carries the outcome
// if stderr is unwritable.
func (a *App) notef(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format, args...)
}
