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

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// director runs `guildhall director status|start|ready` (docs/director.md).
func (a *App) director(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return a.directorStatusCmd(args[1:])
		case "start":
			return a.directorStart(args[1:])
		case "ready":
			return a.directorReady(args[1:])
		}
	}
	return a.usageError("director: the commands are status, start and ready")
}

// fakeDirector runs the fake Director session: `guildhall fake-director --role <file> <first message>`.
func (a *App) fakeDirector(args []string) int {
	return fake.DirectorMain(args, a.Stdin, a.Stdout, a.Stderr)
}

// errStartRefused is a failed preflight check, already printed on its own line.
var errStartRefused = errors.New("director start: preflight failed")

// directorStart runs `guildhall director start [runtime] --foreground [--check] [--copy <dir>]`: the session
// supervisor (docs/director.md#start).
func (a *App) directorStart(args []string) int {
	fl := flag.NewFlagSet("director start", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	foreground := fl.Bool("foreground", false, "run the session in this terminal")
	check := fl.Bool("check", false, "run the preflight only")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("director start: " + err.Error())
	}
	var storePath, cfgPath, state string
	switch err := cp.apply(a.Getenv, &storePath, &cfgPath, &state); {
	case err != nil:
		return a.usageError("director start: " + err.Error())
	case fl.NArg() > 1:
		return a.usageError("director start takes at most one runtime")
	case !*foreground:
		return a.usageError("director start runs the session in this terminal; pass --foreground")
	case cp.dir == "":
		return a.usageError("director start: --copy is required, or GUILDHALL_COPY")
	}
	copyDir, err := filepath.Abs(cp.dir)
	if err != nil {
		return a.fail(err)
	}
	p, err := a.preflight(copyDir, cfgPath, storePath, fl.Arg(0))
	if p.cleanup != nil {
		defer p.cleanup()
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
	return a.superviseSession(p, copyDir, storePath, state)
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
	line := func(name, format string, args ...any) {
		_, _ = fmt.Fprintf(a.Stdout, "%s: %s\n", name, fmt.Sprintf(format, args...))
	}
	refuse := func(name string, err error) (startPlan, error) {
		line(name, "refused: %v", err)
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: director start: %s: %v\n", name, err)
		return p, errStartRefused
	}
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
// child and records the session stopped when the runtime ends.
func (a *App) superviseSession(p startPlan, copyDir, storePath, state string) int {
	self, err := artisan.Self()
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	data, err := json.Marshal(p.cfg)
	if err != nil {
		return a.fail(err)
	}
	session, err := withStore(storePath, false, func(s *store.Store) (store.DirectorSession, error) {
		d := store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity(self),
			Release: Version(), ConfigDigest: string(digest.Of(data))}
		latest, err := s.LatestDirectorSession()
		if err == nil {
			d.Number = latest.Number + 1
		} else if !errors.Is(err, store.ErrNotFound) {
			return d, err
		}
		_, err = s.StartDirectorSession(d)
		return d, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director start: record session: %w", err))
	}
	code := a.runSession(p, session.Number, copyDir, storePath, state)
	if err := stopSession(storePath, session.Number); err != nil {
		return a.fail(fmt.Errorf("director start: record session %d stopped: %w", session.Number, err))
	}
	return code
}

// runSession renders session n and runs its runtime to the end, returning the supervisor's exit status.
func (a *App) runSession(p startPlan, n int, copyDir, storePath, state string) int {
	status, err := readDirectorStatus(context.Background(), storePath, state)
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
	// The terminal's interrupt reaches the runtime, which handles it; the supervisor outlives it to record the end.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	err = c.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return a.fail(fmt.Errorf("director start: the runtime exited with status %d", exit.ExitCode()))
	case err != nil:
		return a.fail(fmt.Errorf("director start: run the runtime: %w", err))
	}
	return 0
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
