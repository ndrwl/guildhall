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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// director runs `guildhall director status|start|ready|pause|resume|stop` (docs/director.md).
func (a *App) director(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return a.directorStatusCmd(args[1:])
		case "start":
			return a.directorStart(args[1:])
		case "ready":
			return a.directorReady(args[1:])
		case "pause":
			return a.directorPause(args[1:])
		case "resume":
			return a.directorResume(args[1:])
		case "stop":
			return a.directorStop(args[1:])
		}
	}
	return a.usageError("director: the commands are status, start, ready, pause, resume and stop")
}

// fakeDirector runs the fake Director session: `guildhall fake-director --role <file> <first message>`.
func (a *App) fakeDirector(args []string) int {
	return fake.DirectorMain(args, a.Stdin, a.Stdout, a.Stderr)
}

// errStartRefused is a failed preflight check, already printed on its own line.
var errStartRefused = errors.New("director start: preflight failed")

// directorStart runs `guildhall director start [runtime] [--tab] [--check] [--copy <dir>]`: the session
// supervisor in this terminal (docs/director.md#start), or with --tab the wrapper that opens the Director's
// Herdr tab and runs the supervisor in it (docs/director.md#the-directors-tab).
func (a *App) directorStart(args []string) int {
	fl := flag.NewFlagSet("director start", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	tab := fl.Bool("tab", false, "open the Director's Herdr tab and run the session there")
	check := fl.Bool("check", false, "run the preflight only")
	var pane store.DirectorPane
	fl.StringVar(&pane.Session, "herdr-session", "", "the Herdr session of the tab the session runs in")
	fl.StringVar(&pane.Workspace, "herdr-workspace", "", "the Herdr workspace of that tab")
	fl.StringVar(&pane.Tab, "herdr-tab", "", "the Herdr tab")
	fl.StringVar(&pane.Pane, "herdr-pane", "", "the Herdr pane")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("director start: " + err.Error())
	}
	var storePath, cfgPath, state string
	switch err := cp.apply(a.Getenv, &storePath, &cfgPath, &state); {
	case err != nil:
		return a.usageError("director start: " + err.Error())
	case fl.NArg() > 1:
		return a.usageError("director start takes at most one runtime")
	case cp.dir == "":
		return a.usageError("director start: --copy is required, or GUILDHALL_COPY")
	case pane != (store.DirectorPane{}) && (pane.Session == "" || pane.Workspace == "" || pane.Tab == "" || pane.Pane == ""):
		return a.usageError("director start: --herdr-session, --herdr-workspace, --herdr-tab and --herdr-pane go together")
	case pane != (store.DirectorPane{}) && *tab:
		return a.usageError("director start: --herdr-session, --herdr-workspace, --herdr-tab and --herdr-pane bind a session in this terminal to its tab, so they do not go with --tab")
	}
	copyDir, err := filepath.Abs(cp.dir)
	if err != nil {
		return a.fail(err)
	}
	p, err := a.preflight(copyDir, cfgPath, storePath, fl.Arg(0))
	if p.cleanup != nil {
		defer p.cleanup()
	}
	var host adapter.TabHost
	if err == nil && *tab {
		host, err = a.herdrPreflight(p)
	}
	if err != nil {
		if !errors.Is(err, errStartRefused) {
			return a.fail(fmt.Errorf("director start: %w", err))
		}
		return 1
	}
	if *check {
		return 0
	}
	if *tab {
		return a.openDirectorTab(host, p, copyDir, storePath, fl.Arg(0))
	}
	return a.superviseSession(p, copyDir, storePath, state, pane)
}

// startPlan is what the preflight resolved for a session.
type startPlan struct {
	cfg      *config.Config
	cfgPath  string
	name     string
	runtime  runtimes.Session
	skills   string
	baseline string
	cleanup  func()
}

// preflight checks, in order, that the config is admitted, the runtime is known and on PATH, the store opens and
// the release skills install, printing one doctor-style line per check. A failed check prints its line, then
// errStartRefused; nothing is recorded.
func (a *App) preflight(copyDir, cfgPath, storePath, positional string) (startPlan, error) {
	p := startPlan{cfgPath: cfgPath}
	line := a.preflightLine
	refuse := func(name string, err error) (startPlan, error) { return p, a.refuseCheck(name, err) }
	cfg, err := admitConfig(copyDir, cfgPath)
	if err != nil {
		return refuse("config", err)
	}
	p.cfg = cfg
	line("config", "admitted (%s)", cfgPath)

	p.name = positional
	if p.name == "" && cfg.Director != nil {
		p.name = cfg.Director.Adapter
	}
	exe, err := a.Executable()
	if err != nil {
		return p, err
	}
	if p.runtime, err = runtimes.LookupSession(p.name, exe); err != nil {
		if p.name == "" {
			err = fmt.Errorf("no runtime: name one, or set [director] adapter; the adapters are %s", strings.Join(runtimes.Names(), ", "))
		}
		return refuse("runtime", err)
	}
	program := exe
	if prog := p.runtime.Program(); prog != "" {
		if program, err = a.LookPath(prog); err != nil {
			return refuse("runtime", fmt.Errorf("%s: not found on PATH", prog))
		}
	}
	line("runtime", "%s (%s)", p.name, program)

	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return refuse("store", noStore(storePath))
	}
	if _, err := withStore(storePath, true, func(*store.Store) (struct{}, error) { return struct{}{}, nil }); err != nil {
		return refuse("store", err)
	}
	line("store", "opens (%s)", storePath)

	if a.InstallSkills == nil || a.InstallBaseline == nil {
		return p, errors.New("release skills and baseline installers are not configured")
	}
	tmp, err := os.MkdirTemp("", "guildhall-session-*")
	if err != nil {
		return p, err
	}
	p.cleanup = func() { _ = os.RemoveAll(tmp) }
	p.skills, p.baseline = filepath.Join(tmp, "skills"), filepath.Join(tmp, "baseline")
	if err := a.InstallSkills(p.skills); err != nil {
		return refuse("skills", err)
	}
	if err := a.InstallBaseline(p.baseline); err != nil {
		return refuse("skills", err)
	}
	entries, err := os.ReadDir(p.skills)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return refuse("skills", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	line("skills", "%d release skills install", n)
	return p, nil
}

// preflightLine prints one preflight check's line.
func (a *App) preflightLine(name, format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stdout, "%s: %s\n", name, fmt.Sprintf(format, args...))
}

// refuseCheck prints the line and the error of a failed preflight check and returns errStartRefused.
func (a *App) refuseCheck(name string, err error) error {
	a.preflightLine(name, "refused: %v", err)
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: director start: %s: %v\n", name, err)
	return errStartRefused
}

// herdrTimeout bounds the Herdr status check of the wrapper's preflight.
const herdrTimeout = 30 * time.Second

// herdrPreflight is the wrapper's last preflight check: the config names a Herdr session, its server runs and
// is new enough, and the terminal host over it is returned.
func (a *App) herdrPreflight(p startPlan) (adapter.TabHost, error) {
	session, err := p.cfg.HerdrSession()
	switch {
	case err != nil:
		return nil, a.refuseCheck("herdr", err)
	case session == "":
		return nil, a.refuseCheck("herdr", fmt.Errorf("no [local] %s names the Herdr session the Director's tab opens in; set it, or run the session in this terminal without --tab", config.HerdrSessionKey))
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrTimeout)
	defer cancel()
	open := a.DirectorTabs
	if open == nil {
		open = openHerdrTabs
	}
	host, version, err := open(ctx, session)
	if err != nil {
		return nil, a.refuseCheck("herdr", err)
	}
	a.preflightLine("herdr", "session %q (server %s)", session, version)
	return host, nil
}

// openHerdrTabs is the Herdr host, found on PATH as herdr, for the named session once its status shows the
// server running and new enough.
func openHerdrTabs(ctx context.Context, session string) (adapter.TabHost, string, error) {
	c, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		return nil, "", err
	}
	st, err := c.Status(ctx)
	if err != nil {
		return nil, "", err
	}
	return adapter.NewHerdrTabs(c), st.ServerVersion, nil
}

// errDirectorLive refuses a start while the newest session's supervisor is alive.
var errDirectorLive = errors.New("director: live")

// checkSupersede returns the newest session's number, 0 when there is none, refusing with errDirectorLive while
// that session is not stopped and its supervisor is alive or cannot be shown gone. A session whose supervisor is
// gone is superseded when the next one is recorded.
func checkSupersede(s *store.Store) (int, error) {
	d, err := s.LatestDirectorSession()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	case d.State == store.DirectorStopped:
		return d.Number, nil
	}
	switch live, err := artisan.Probe(artisan.Identity(d.Identity)); live {
	case artisan.Gone:
		return d.Number, nil
	case artisan.Alive:
		return 0, fmt.Errorf("%w: session %d is %s under its supervisor, pid %d", errDirectorLive, d.Number, d.State, d.Identity.PID)
	default:
		return 0, fmt.Errorf("%w: session %d is %s and its supervisor, pid %d, cannot be shown gone: %v", errDirectorLive, d.Number, d.State, d.Identity.PID, err)
	}
}

// directorStartWait bounds the wrapper's wait for the session record; directorStartPoll is the interval
// between its reads.
var directorStartWait, directorStartPoll = 2 * time.Minute, 200 * time.Millisecond

// openDirectorTab is the wrapper: it opens the Director's tab, labelled with its name, in the workspace labelled
// with its name, types the supervisor's command into it, waits for the session the supervisor records with
// that tab's pane and prints it. The tab is never closed here.
func (a *App) openDirectorTab(host adapter.TabHost, p startPlan, copyDir, storePath, runtime string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	newest, err := withStore(storePath, true, checkSupersede)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	exe, err := a.Executable()
	if err != nil {
		return a.fail(err)
	}
	name := p.cfg.Name
	ws, err := adapter.EnsureWorkspace(ctx, host, name, copyDir)
	if err != nil {
		return a.fail(fmt.Errorf("director start: workspace %s: %w", name, err))
	}
	l := adapter.TabLauncher{Host: host, WorkspaceID: ws}
	tab, shell, err := l.Open(ctx, "director "+name, copyDir, nil)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	argv := []string{exe, "director", "start"}
	if runtime != "" {
		argv = append(argv, runtime)
	}
	argv = append(argv, "--copy", copyDir,
		"--herdr-session", tab.Session, "--herdr-workspace", tab.WorkspaceID, "--herdr-tab", tab.TabID, "--herdr-pane", tab.PaneID)
	if err := l.Type(ctx, tab, shell, argv); err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	a.preflightLine("tab", "director %s (workspace %s, tab %s, pane %s)", name, tab.WorkspaceID, tab.TabID, tab.PaneID)
	d, err := waitTabSession(ctx, host, tab, storePath, newest)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	return a.printJSON(d)
}

// waitTabSession waits for a session after the one numbered after that records tab's pane, as launch.Process
// waits for ownership. It fails once the tab's shell, having been seen running the typed command, is idle again
// with no such session, and after directorStartWait.
func waitTabSession(ctx context.Context, host adapter.TabHost, tab adapter.TabRef, storePath string, after int) (store.DirectorSession, error) {
	want := store.DirectorPane{Session: tab.Session, Workspace: tab.WorkspaceID, Tab: tab.TabID, Pane: tab.PaneID}
	deadline := time.Now().Add(directorStartWait)
	busy := false
	for {
		d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) {
			latest, err := s.LatestDirectorSession()
			if err != nil {
				return store.DirectorSession{}, err
			}
			for n := after + 1; n <= latest.Number; n++ {
				if d, err := s.DirectorSession(n); err != nil || d.Pane == want {
					return d, err
				}
			}
			return store.DirectorSession{}, nil
		})
		switch {
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return d, err
		case d.Number > 0:
			return d, nil
		}
		info, err := host.PaneProcessInfo(ctx, tab)
		switch {
		case err != nil:
			return store.DirectorSession{}, fmt.Errorf("tab %s: %w", tab.TabID, err)
		case !info.ShellIdle():
			busy = true
		case busy:
			return store.DirectorSession{}, fmt.Errorf("the command typed into tab %s ended before it recorded a session; the tab is left open with its output", tab.TabID)
		}
		if time.Now().After(deadline) {
			return store.DirectorSession{}, fmt.Errorf("no session recorded tab %s within %s; the tab is left open", tab.TabID, directorStartWait)
		}
		select {
		case <-ctx.Done():
			return store.DirectorSession{}, ctx.Err()
		case <-time.After(directorStartPoll):
		}
	}
}

// admitConfig loads the copy's config for a standalone Director: a managed copy, with managed.toml beside the
// config or inline [managed] settings, is refused until managed mode lands.
func admitConfig(copyDir, cfgPath string) (*config.Config, error) {
	if _, err := os.Stat(filepath.Join(copyDir, "managed.toml")); err == nil {
		return nil, errors.New("managed.toml: managed mode is not built yet, so a managed copy cannot start (guildhall director enrol)")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.Mode != config.ModeDirector:
		return nil, fmt.Errorf("%s is a %s config, not a director's", cfgPath, cfg.Mode)
	case len(cfg.Managed) > 0:
		return nil, errors.New("inline [managed] settings: managed mode is not built yet, so a managed copy cannot start (guildhall director enrol)")
	}
	return cfg, nil
}

// superviseSession records a new session as recovering, renders it, runs the runtime in it as this process's
// child and records the session stopped when the runtime ends. From before the session is recorded until it is
// recorded stopped the supervisor handles the session signals rather than dying of them.
func (a *App) superviseSession(p startPlan, copyDir, storePath, state string, pane store.DirectorPane) int {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, sessionSignals...)
	defer signal.Stop(signals)
	self, err := artisan.Self()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	data, err := json.Marshal(p.cfg)
	if err != nil {
		return a.fail(err)
	}
	session, err := withStore(storePath, false, func(s *store.Store) (store.DirectorSession, error) {
		d := store.DirectorSession{State: store.DirectorRecovering, Identity: store.ProcessIdentity(self),
			Release: Version(), ConfigDigest: string(digest.Of(data)), Pane: pane}
		newest, err := checkSupersede(s)
		if err != nil {
			return d, err
		}
		d.Number = newest + 1
		_, err = s.StartDirectorSession(d)
		return d, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director start: record session: %w", err))
	}
	code := a.runSession(p, session.Number, copyDir, storePath, state, signals)
	if err := stopSession(storePath, session.Number); err != nil {
		return a.fail(fmt.Errorf("director start: record session %d stopped: %w", session.Number, err))
	}
	return code
}

// runSession renders session n and runs its runtime to the end, returning the supervisor's exit status. A signal
// received before the runtime starts ends the session with no runtime started.
func (a *App) runSession(p startPlan, n int, copyDir, storePath, state string, signals <-chan os.Signal) int {
	status, err := readDirectorStatus(context.Background(), storePath, state, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director start: in-flight snapshot: %w", err))
	}
	var inFlight []string
	for _, it := range status.NeedsMe {
		inFlight = append(inFlight, fmt.Sprintf("%s: %s; run `%s`", it.Kind, oneLine(it.Reason), it.Command))
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
		RuntimeFiles: p.runtime.SessionFiles(copyDir), InFlight: inFlight}, []render.SkillRoot{{Name: "release", Dir: p.skills}})
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	cmd, err := p.runtime.Session(adapter.SessionSpec{Dir: dir, Copy: copyDir, RolePrompt: r.RolePath,
		FirstMessage: render.StartupMessage, Model: model, Effort: effort})
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	_, _ = fmt.Fprintf(a.Stdout, "session: %d (%s)\n", n, dir)
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	c.Stdin, c.Stdout, c.Stderr = a.Stdin, a.Stdout, a.Stderr
	select {
	case s := <-signals:
		return a.fail(fmt.Errorf("director start: ended by %v before the runtime started", s))
	default:
	}
	// The terminal's interrupt reaches the runtime, which handles it; a termination is passed on to it. Either
	// way the supervisor outlives the runtime to record the end.
	if err := c.Start(); err != nil {
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err))
	}
	ended, stopped, err := a.waitSession(c, signals, storePath, n)
	var exit *exec.ExitError
	switch {
	case stopped:
		_, _ = fmt.Fprintf(a.Stdout, "session: %d stopped by director stop\n", n)
		return 0
	case ended != nil:
		return a.fail(fmt.Errorf("director start: ended by %v", ended))
	case errors.As(err, &exit):
		return a.fail(fmt.Errorf("director start: the runtime exited with status %d", exit.ExitCode()))
	case err != nil:
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err))
	}
	return 0
}

// stopKill is how long a runtime the supervisor ended for a stop has to exit before it is killed.
const stopKill = 10 * time.Second

// waitSession waits for the started runtime c of session n to end, passing it each termination among signals,
// and returns the last termination passed on, if any, and whether it ended the runtime for a stop, with c's end.
// Every App.SessionPoll it reads the session from the store at storePath: once `director stop` has recorded it
// stopping, it ends the runtime after App.StopGrace, or at once for stop --now, and kills it if it has not exited
// stopKill later.
func (a *App) waitSession(c *exec.Cmd, signals <-chan os.Signal, storePath string, n int) (os.Signal, bool, error) {
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	poll, grace := a.SessionPoll, a.StopGrace
	if poll <= 0 {
		poll = time.Second
	}
	if grace <= 0 {
		grace = 60 * time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var ended os.Signal
	var stopping, stopped time.Time
	for {
		select {
		case s := <-signals:
			if terminate(c.Process, s) {
				ended = s
			}
		case <-tick.C:
			if !stopped.IsZero() {
				if time.Since(stopped) >= stopKill {
					_ = c.Process.Kill()
				}
				continue
			}
			d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.DirectorSession(n) })
			if err != nil || d.State != store.DirectorStopping {
				continue
			}
			if stopping.IsZero() {
				stopping = time.Now()
			}
			// A plain stop followed by stop --now shortens the grace.
			if d.StopNow || time.Since(stopping) >= grace {
				endRuntime(c.Process)
				stopped = time.Now()
			}
		case err := <-waited:
			return ended, !stopped.IsZero(), err
		}
	}
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

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// directorReady runs `guildhall director ready [--copy <dir> | --store <path>]`: the newest session moves from
// recovering to running and is printed; a running one is printed unchanged (docs/director.md#ready).
func (a *App) directorReady(args []string) int {
	fl := flag.NewFlagSet("director ready", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "Director store file")
	if err := fl.Parse(args); err != nil {
		return a.usageError("director ready: " + err.Error())
	}
	switch err := cp.apply(a.Getenv, path, nil, nil); {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("director ready: unexpected argument %q", fl.Arg(0)))
	case err != nil:
		return a.usageError("director ready: " + err.Error())
	case *path == "":
		return a.usageError("director ready: --store is required, or --copy")
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("director ready: %w", noStore(*path)))
	}
	d, err := withStore(*path, false, func(s *store.Store) (store.DirectorSession, error) {
		d, err := s.LatestDirectorSession()
		switch {
		case errors.Is(err, store.ErrNotFound):
			return d, errors.New("no Director session; start one with guildhall director start")
		case err != nil, d.State == store.DirectorRunning:
			return d, err
		case d.State != store.DirectorRecovering:
			return d, fmt.Errorf("the Director session %d is %s, not recovering", d.Number, d.State)
		}
		if _, err := s.SetDirectorState(d.Number, store.DirectorRunning, d.StateSequence); err != nil {
			return d, err
		}
		return s.DirectorSession(d.Number)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director ready: %w", err))
	}
	return a.printJSON(d)
}

// refuseWhileRecovering refuses new work while the newest Director session is recovering: it starts no new work
// until the Director says it is ready (docs/director.md#ready).
func refuseWhileRecovering(s *store.Store) error {
	d, err := s.LatestDirectorSession()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return err
	case d.State == store.DirectorRecovering:
		return fmt.Errorf("the Director session %d is recovering and starts no new work; run guildhall director ready first", d.Number)
	}
	return nil
}
