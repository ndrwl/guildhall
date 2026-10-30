package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// director runs `guildhall director status|start|ready|pause|resume|stop|note|follow-up|reload|turn-started|turn-ended|
// wait-wake|context-report`
// (docs/director.md).
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
		case "note":
			return a.directorNote(args[1:])
		case "follow-up":
			return a.directorFollowUp(args[1:])
		case "reload":
			return a.directorReload(args[1:])
		case "turn-started":
			return a.directorTurnSignal(session.TurnStarted, args[1:])
		case "turn-ended":
			return a.directorTurnSignal(session.TurnEnded, args[1:])
		case "wait-wake":
			return a.directorWaitWake(args[1:])
		case "context-report":
			return a.directorContextReport(args[1:])
		}
	}
	return a.usageError("director: the commands are status, start, ready, pause, resume, stop, note, follow-up, reload, turn-started, turn-ended, wait-wake and context-report")
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
	if pane == (store.DirectorPane{}) {
		var reason string
		if pane, reason = ambientPane(a.Getenv); reason != "" {
			a.notef("director start: warning: this session has no Herdr pane (%s), so messages will not wake the Director; start it with --tab or inside a Herdr pane\n", reason)
		} else {
			a.preflightLine("pane", "%s (tab %s, workspace %s, from HERDR_*)", pane.Pane, pane.Tab, pane.Workspace)
		}
	}
	return a.superviseSession(p, copyDir, storePath, state, pane)
}

// ambientPanes are the HERDR_* variables, in the order they are checked, that name the Herdr pane a process runs
// in, beside the field of the pane each fills.
var ambientPanes = []struct {
	name  string
	field func(*store.DirectorPane) *string
}{
	{"HERDR_SESSION", func(p *store.DirectorPane) *string { return &p.Session }},
	{"HERDR_WORKSPACE_ID", func(p *store.DirectorPane) *string { return &p.Workspace }},
	{"HERDR_TAB_ID", func(p *store.DirectorPane) *string { return &p.Tab }},
	{"HERDR_PANE_ID", func(p *store.DirectorPane) *string { return &p.Pane }},
}

// ambientPane is the Herdr pane getenv's HERDR_* variables name, adopted, which a plain director start binds
// (docs/director.md#start), or no pane and the reason it binds none: a variable is unset, the session is Herdr's
// default one or a value is not the shape Herdr's client accepts. A nil getenv is an empty environment.
func ambientPane(getenv func(string) string) (store.DirectorPane, string) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	p := store.DirectorPane{Adopted: true}
	for _, v := range ambientPanes {
		val := getenv(v.name)
		switch {
		case val == "":
			return store.DirectorPane{}, v.name + " not set"
		case v.name == "HERDR_SESSION" && val == "default":
			return store.DirectorPane{}, `HERDR_SESSION is "default"`
		case v.name == "HERDR_SESSION" && !herdr.ValidSession(val):
			return store.DirectorPane{}, fmt.Sprintf("HERDR_SESSION %q is not a Herdr session name", val)
		case v.name != "HERDR_SESSION" && !herdr.ValidID(val):
			return store.DirectorPane{}, fmt.Sprintf("%s %q is not a Herdr id", v.name, val)
		}
		*v.field(&p) = val
	}
	return p, ""
}

// startPlan is what the preflight resolved for a session.
type startPlan struct {
	cfg     *config.Config
	cfgPath string
	// positional is the runtime named on the command line, if any; name and runtime are resolved from it or
	// from [director] adapter.
	positional string
	name       string
	runtime    runtimes.Session
	skills     string
	baseline   string
	cleanup    func()
}

// preflight checks, in order, that the config is admitted, the runtime is known and on PATH, guildhall is on
// PATH for the session's bare commands, the store opens and
// the release skills install, printing one doctor-style line per check, then names the Artisan permissions the
// claude profiles depend on, which refuses nothing. A failed check prints its line, then
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

	p.positional = positional
	program, err := a.resolveRuntime(&p)
	if err != nil {
		return refuse("runtime", err)
	}
	line("runtime", "%s (%s)", p.name, program)

	guildhall, err := a.LookPath("guildhall")
	if err != nil {
		return refuse("guildhall", fmt.Errorf("guildhall must be on PATH for session hooks, status line and Director commands; add the running executable's directory to PATH: %w", err))
	}
	line("guildhall", "%s", guildhall)

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
	perms, err := artisanPermissions(cfg, cfgPath)
	if err != nil {
		return refuse("artisans", err)
	}
	line("artisans", "%s", perms)
	if err := a.preflightSkillPolicies(&p); err != nil {
		return refuse("skills", err)
	}
	return p, nil
}

// artisanPermissions says what this Director's config at cfgPath grants each profile that runs the claude adapter,
// its effective grant (claude.EffectiveGrant) grouped by source: the copy's allowed_tools, or else the shipped
// default, or a note that the profile gets none; then each repository's additions, labelled with the repository id.
// acceptEdits grants edits only but inherited settings, hooks and skill-scoped grants may allow more, so a build's
// checks and commits depend on Bash permission (docs/adapters.md#artisan-permissions). A profile of another adapter
// with allowed_tools or a repository grant is refused, since only the claude adapter grants them, and so is a
// profile whose model or effort its runtime cannot honour (checkProfileSettings).
func artisanPermissions(cfg *config.Config, cfgPath string) (string, error) {
	if err := checkProfileSettings(cfg, slices.Sorted(maps.Keys(cfg.Profiles))); err != nil {
		return "", err
	}
	var grants []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Profiles)) {
		p := cfg.Profiles[name]
		if p.Adapter != adapter.ClaudeName {
			if len(p.AllowedToolsList()) > 0 {
				return "", fmt.Errorf("profile %s runs the %s adapter, which grants no allowed_tools; only the %s adapter does (docs/adapters.md#artisan-permissions)", name, p.Adapter, adapter.ClaudeName)
			}
			for _, r := range cfg.Repositories {
				if _, ok := r.Profiles[name]; ok {
					return "", fmt.Errorf("repository %s grants profile %s, which runs the %s adapter, which grants no allowed_tools; only the %s adapter does (docs/adapters.md#artisan-permissions)", r.ID, name, p.Adapter, adapter.ClaudeName)
				}
			}
			continue
		}
		base, err := claude.EffectiveGrant(cfg, name, "")
		if err != nil {
			return "", err
		}
		// The rule Guildhall gives every claude profile is named apart, after the profile's own grant.
		var guildhall []string
		base = slices.DeleteFunc(base, func(g claude.Grant) bool {
			if g.Source == claude.SourceGuildhall {
				guildhall = append(guildhall, g.Rule)
			}
			return g.Source == claude.SourceGuildhall
		})
		var additions []string
		for _, r := range cfg.Repositories {
			grant, err := claude.EffectiveGrant(cfg, name, r.ID)
			if err != nil {
				return "", err
			}
			var added []string
			for _, g := range grant {
				if g.Source == claude.SourceRepository {
					added = append(added, g.Rule)
				}
			}
			if len(added) > 0 {
				additions = append(additions, fmt.Sprintf(", plus for repository %s %s", r.ID, strings.Join(added, ", ")))
			}
		}
		if len(guildhall) > 0 {
			additions = append(additions, ", plus from Guildhall "+strings.Join(guildhall, ", "))
		}
		var s string
		switch rules := strings.Join(claude.GrantRules(base), ", "); {
		case p.AllowedToolsSet() && rules == "":
			s = fmt.Sprintf("profile %s grants nothing from the copy", name)
		case p.AllowedToolsSet():
			s = fmt.Sprintf("profile %s grants from the copy %s", name, rules)
		case rules == "" && len(additions) == 0:
			s = fmt.Sprintf("profile %s gets no shipped default and grants nothing", name)
		case rules == "":
			s = fmt.Sprintf("profile %s gets no shipped default", name)
		default:
			s = fmt.Sprintf("profile %s grants the shipped default %s", name, rules)
		}
		grants = append(grants, s+strings.Join(additions, ""))
	}
	whose := fmt.Sprintf("Director %s's config %s: ", cfg.Name, cfgPath)
	if len(grants) == 0 {
		return whose + "no profile runs the claude adapter", nil
	}
	return whose + strings.Join(grants, "; ") + "; acceptEdits grants edits only, while inherited settings, hooks and skill-scoped grants may allow more; a build's checks and commits need Bash permission (docs/adapters.md#artisan-permissions)" +
		"; wrappers load grants by copy/profile/repository reference, never raw rule flags", nil
}

// checkProfileSettings refuses the first of the named profiles whose runtime cannot honour its model and effort
// (runtimes.Runtime.CheckSettings), naming the profile; a profile of an adapter Guildhall does not know is left to
// the checks that refuse that adapter.
func checkProfileSettings(cfg *config.Config, names []string) error {
	for _, name := range names {
		p := cfg.Profiles[name]
		if err := runtimeSettings(p.Adapter, adapter.Settings{Model: p.Model, Effort: cfg.EffectiveEffort(p.Effort)}); err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
	}
	return nil
}

// runtimeSettings is interp.Engine.CheckSettings over package runtimes: the named runtime's CheckSettings, or no
// refusal for a name that is not a runtime, which the adapter checks refuse.
func runtimeSettings(name string, s adapter.Settings) error {
	rt, err := runtimes.Lookup(name, "")
	if err != nil {
		return nil
	}
	return rt.CheckSettings(s)
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

// errStopSweeping refuses a start while the newest session's stop has not finished stopping its attempts.
var errStopSweeping = errors.New("director: stop sweeping")

// superseded is what checkSupersede found: the newest session's number, 0 when there is none, its recorded runtime
// unit, and, when that unit was proven empty but could not be removed, the removal's error.
type superseded struct {
	Number  int
	Unit    store.Containment
	Removal error
}

// warnUnremoved prints a warning for sp's failed removal, if any.
func (a *App) warnUnremoved(sp superseded) {
	if sp.Removal != nil {
		a.notef("director start: warning: %v\n", sp.Removal)
	}
}

// checkSupersede returns the newest session's number, 0 when there is none, refusing with errStopSweeping while
// that session's stop has not finished its sweep, it is sweeping or stopping with its supervisor gone, and with
// errDirectorLive while it is not stopped and its supervisor is alive or cannot be shown gone, or while its recorded
// runtime unit is not proven empty (runtimeEnded). A session whose supervisor is gone is superseded when the next one
// is recorded. Once a stopped or gone session's unit is proven empty, it is removed (removeRuntime); a failed removal
// is returned as Removal, never as the refusal, so the caller warns naming the unit and the error and starts anyway.
// A unit already removed is removed.
func checkSupersede(s *store.Store) (superseded, error) {
	d, err := s.LatestDirectorSession()
	sweeping := func() error {
		return fmt.Errorf("%w: session %d's stop has not finished stopping its attempts; wait for director stop to finish, or run guildhall director stop to finish it", errStopSweeping, d.Number)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return superseded{}, nil
	case err != nil:
		return superseded{}, err
	case d.Sweeping:
		return superseded{}, sweeping()
	case d.State == store.DirectorStopped:
		if err := runtimeEnded(d); err != nil {
			return superseded{}, err
		}
		return superseded{Number: d.Number, Unit: d.Runtime, Removal: removeRuntime(d)}, nil
	}
	switch live, err := artisan.Probe(artisan.Identity(d.Identity)); live {
	case artisan.Gone:
		if d.State == store.DirectorStopping {
			return superseded{}, sweeping()
		}
		if err := runtimeEnded(d); err != nil {
			return superseded{}, err
		}
		return superseded{Number: d.Number, Unit: d.Runtime, Removal: removeRuntime(d)}, nil
	case artisan.Alive:
		return superseded{}, fmt.Errorf("%w: session %d is %s under its supervisor, pid %d", errDirectorLive, d.Number, d.State, d.Identity.PID)
	default:
		return superseded{}, fmt.Errorf("%w: session %d is %s and its supervisor, pid %d, cannot be shown gone: %v", errDirectorLive, d.Number, d.State, d.Identity.PID, err)
	}
}

// sweepRuntimeUnit is the removal sweep removeRuntime runs; tests replace it.
var sweepRuntimeUnit = artisan.Sweep

// removeRuntime removes session d's recorded runtime unit, which runtimeEnded has proved empty, with one more sweep,
// returning an error naming the unit when it is not removed. A session with no runtime recorded has none.
// A cgroup is then checked gone, since the sweep reports one that Rmdir refuses as busy as removed.
func removeRuntime(d store.DirectorSession) error {
	c := d.Runtime
	if c == (store.Containment{}) {
		return nil
	}
	failed := func(err error) error {
		return fmt.Errorf("session %d's runtime unit %s was not removed: %w", d.Number, unitName(c), err)
	}
	if _, err := sweepRuntimeUnit(artisan.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason}, artisan.Identity(c.Worker)); err != nil {
		return failed(err)
	}
	if c.Kind != artisan.KindCgroup {
		return nil
	}
	switch _, err := os.Stat(c.Name); {
	case err == nil:
		return failed(errors.New("still present after removal"))
	case !errors.Is(err, fs.ErrNotExist):
		return failed(err)
	}
	return nil
}

// unitName names the recorded runtime unit c: its name, or, for a process group, which has none, its kind and
// worker.
func unitName(c store.Containment) string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("%s of pid %d", c.Kind, c.Worker.PID)
}

// runtimeEnded refuses with errDirectorLive while session d's recorded runtime unit is not proven empty, such as
// when its supervisor died and left the runtime's processes running, naming them and director stop, which ends them.
// A session with no runtime recorded has none to wait for.
func runtimeEnded(d store.DirectorSession) error {
	c := d.Runtime
	if c == (store.Containment{}) {
		return nil
	}
	left, err := artisan.Contained(artisan.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason}, artisan.Identity(c.Worker))
	switch {
	case err != nil:
		return fmt.Errorf("%w: session %d's runtime unit cannot be shown empty: %v; run guildhall director stop to end it", errDirectorLive, d.Number, err)
	case len(left) > 0:
		pids := make([]string, len(left))
		for i, id := range left {
			pids[i] = strconv.Itoa(id.PID)
		}
		return fmt.Errorf("%w: session %d's runtime left %d processes running, pid %s; run guildhall director stop to end them",
			errDirectorLive, d.Number, len(left), strings.Join(pids, ", "))
	}
	return nil
}

// directorStartWait bounds the wrapper's wait for the session record; directorStartPoll is the interval
// between its reads.
var directorStartWait, directorStartPoll = 2 * time.Minute, 200 * time.Millisecond

// openDirectorTab is the wrapper: it opens the Director's tab, labelled with its name, in the workspace labelled
// with its name, types the supervisor's command into it, waits for the session the supervisor records with
// that tab's pane and prints it. Once recorded, it retires only older recorded,
// stopped Director tabs whose runtime is empty and pane is an idle shell.
func (a *App) openDirectorTab(host adapter.TabHost, p startPlan, copyDir, storePath, runtime string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sp, err := withStore(storePath, true, checkSupersede)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	a.warnUnremoved(sp)
	newest := sp.Number
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
	d, err := waitTabSession(ctx, host, tab, exe, storePath, newest)
	if err != nil {
		return a.fail(fmt.Errorf("director start: %w", err))
	}
	a.retireDirectorTabs(ctx, host, storePath, newest, tab)
	return a.printJSON(d)
}

// retireDirectorTabs is best-effort presentation cleanup after a successful start.
// Identity comes only from persisted session bindings, never labels or tab order.
// A busy, ambiguous or unproven record leaves its tab untouched; no store write
// belongs to this operation. Earlier records may share the pane after a reload.
func (a *App) retireDirectorTabs(ctx context.Context, host adapter.TabHost, storePath string, through int, replacement adapter.TabRef) {
	closer, ok := host.(adapter.IdleTabCloser)
	if !ok {
		return
	}
	seen := map[adapter.TabRef]bool{}
	for n := through; n > 0; n-- {
		d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.DirectorSession(n) })
		if err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "director tab cleanup: session %d: %v\n", n, err)
			continue
		}
		p := d.Pane
		ref := adapter.TabRef{Session: p.Session, WorkspaceID: p.Workspace, TabID: p.Tab, PaneID: p.Pane}
		// An adopted pane's tab is one Guildhall did not create, so it is never retired.
		if p == (store.DirectorPane{}) || p.Adopted || ref.Session != replacement.Session || ref.WorkspaceID != replacement.WorkspaceID || ref == replacement || seen[ref] {
			continue
		}
		seen[ref] = true
		if d.State != store.DirectorStopped || d.Sweeping {
			continue
		}
		if err := runtimeEnded(d); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "director tab cleanup: tab %s left open: %v\n", ref.TabID, err)
			continue
		}
		err = host.AdoptTab(ctx, ref)
		if err == nil {
			info, probeErr := host.PaneProcessInfo(ctx, ref)
			err = probeErr
			if err == nil && !info.ShellIdle() {
				err = errors.New("pane is not an idle shell")
			}
			if err == nil {
				err = closer.CloseTab(ctx, ref)
			}
		}
		if err != nil && !herdr.IsNotFound(err) {
			_, _ = fmt.Fprintf(a.Stderr, "director tab cleanup: tab %s left open: %v\n", ref.TabID, err)
		}
	}
}

// waitTabSession waits for a session after the one numbered after that records tab's pane, as launch.Process
// waits for ownership. It fails once the tab's shell, having been seen running exe, the typed program, is idle
// again with no such session, and after directorStartWait. Another foreground process, such as one the shell's
// startup files run before the typed line is read, is not the typed command.
func waitTabSession(ctx context.Context, host adapter.TabHost, tab adapter.TabRef, exe, storePath string, after int) (store.DirectorSession, error) {
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
		case runsProgram(info, exe):
			busy = true
		case busy && info.ShellIdle():
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

// runsProgram reports whether a foreground process of the pane other than its shell is exe, matched by its
// process name or argv[0] against exe's base name, ignoring case and a .exe suffix.
func runsProgram(info herdr.ProcessInfo, exe string) bool {
	base := func(s string) string {
		s = s[strings.LastIndexAny(s, `/\`)+1:]
		return strings.TrimSuffix(strings.ToLower(s), ".exe")
	}
	want := base(exe)
	for _, p := range info.Foreground {
		if p.PID != info.ShellPID && (base(p.Name) == want || len(p.Argv) > 0 && base(p.Argv[0]) == want) {
			return true
		}
	}
	return false
}

// resolveRuntime resolves p's runtime from p.positional or else p.cfg's [director] adapter, and returns its
// program, found on PATH, or the guildhall executable for a runtime with none.
func (a *App) resolveRuntime(p *startPlan) (string, error) {
	p.name = p.positional
	if p.name == "" && p.cfg.Director != nil {
		p.name = p.cfg.Director.Adapter
	}
	exe, err := a.Executable()
	if err != nil {
		return "", err
	}
	if p.runtime, err = runtimes.LookupSession(p.name, exe); err != nil {
		if p.name == "" {
			err = fmt.Errorf("no runtime: name one, or set [director] adapter; the adapters are %s", strings.Join(runtimes.Names(), ", "))
		}
		return "", err
	}
	settings := adapter.Settings{}
	if d := p.cfg.Director; d != nil {
		settings.Model, settings.Effort = d.Model, d.Effort
	}
	settings.Effort = p.cfg.EffectiveEffort(settings.Effort)
	if err := runtimeSettings(p.name, settings); err != nil {
		return "", err
	}
	if prog := p.runtime.Program(); prog != "" {
		if exe, err = a.LookPath(prog); err != nil {
			return "", fmt.Errorf("%s: not found on PATH", prog)
		}
	}
	return exe, nil
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

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// runningAge is a running attempt's heartbeat or output age for startup.md: seconds under a minute, as heartbeats
// are seconds apart, and otherwise as a note's age.
func runningAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(d, 0)/time.Second)
	}
	return noteAge(d)
}

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
