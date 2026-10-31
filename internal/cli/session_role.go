package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// sessionRole is what the role-neutral session core (superviseSession, runSession, the tab wrapper) asks of the role
// whose session it supervises: the Director's (directorRole) or the Steward's (stewardRole). The core owns startup,
// recovery, notes, reload, handoff, the inbox wake and the tab; the role owns its preflight and the steps each of its
// sessions runs (docs/steward.md#the-steward-session).
type sessionRole struct {
	// command is the role's command family, "director" or "steward", and title the role as the Guildmaster reads
	// it, "Director" or "Steward".
	command, title string
	// storeFile is the copy's store file name, and roleFile the baseline role prompt, relative to a baseline.
	storeFile, roleFile string
	// preflight is the role's start preflight, printing one line per check, and admit loads the copy's config and
	// refuses one this role cannot run under.
	preflight func(a *App, copyDir, cfgPath, storePath, positional string) (startPlan, error)
	admit     func(copyDir, cfgPath string) (*config.Config, error)
	// settings is the session's runtime settings in cfg, and name the label of its Herdr workspace and tab.
	settings func(cfg *config.Config) sessionSettings
	name     func(cfg *config.Config) string
	// maintenance is set for a role with the note audit and hallmark cadences (docs/director.md#maintenance).
	maintenance bool
	// readmit takes the copy's current settings into p at a reload, as the next session's preflight.
	readmit func(a *App, p *startPlan, copyDir string) error
	// begin runs the role's steps at the start of every session, the first and each reload's, before its
	// startup.md is rendered, and returns what that startup.md is told and the sweeps the session ticks.
	begin func(a *App, p startPlan, copyDir, storePath, state string) (sessionStart, error)
	// watch starts the role's watchers of session n, which the session ticks at every poll and waits for at its end.
	watch func(a *App, p startPlan, n int, dir, copyDir, storePath, state string) (sessionWatchers, error)
}

// sessionSettings is a session's runtime adapter, model and effort, and its role prompt, a config-relative path or
// "" for the baseline's.
type sessionSettings struct {
	Adapter, Model, Effort, Role string
}

// sessionStart is what a role's begin returns: the lines startup.md is told, and the workspace close sweep and
// daily prune the session ticks, nil for none.
type sessionStart struct {
	inFlight, running, parked, held, policies []string
	closes, prunes                            *workspaceCloseWatch
}

// wait waits for the session's running sweeps.
func (s sessionStart) wait() {
	s.closes.wait()
	s.prunes.wait()
}

// sessionWatchers are a role's watchers of one session, nil for none.
type sessionWatchers struct {
	checks, deps, queue, starts, holds *checksWatch
}

// wait waits for every running watcher round.
func (w sessionWatchers) wait() {
	for _, c := range []*checksWatch{w.checks, w.deps, w.queue, w.starts, w.holds} {
		c.wait()
	}
}

// sessionRole is p's role, the Director's unless p names another.
func (p startPlan) sessionRole() *sessionRole {
	if p.role == nil {
		return directorRole
	}
	return p.role
}

// directorRole is the Director's session: admitted by admitConfig, run under [director], with the workspace close
// sweep, the daily prune, the in-flight snapshot of its tasks, the repositories' policies and the checks,
// dependency, merge-queue, start-queue and holds watchers.
var directorRole = &sessionRole{
	command: "director", title: "Director", storeFile: "director.store", roleFile: "roles/director.md",
	admit: admitConfig,
	settings: func(cfg *config.Config) sessionSettings {
		if d := cfg.Director; d != nil {
			return sessionSettings{Adapter: d.Adapter, Model: d.Model, Effort: d.Effort, Role: d.Role}
		}
		return sessionSettings{}
	},
	name:        func(cfg *config.Config) string { return cfg.Name },
	maintenance: true,
}

// The role's steps are set apart from its literal, since they reach sessionRole, which reads directorRole.
func init() {
	directorRole.preflight = (*App).preflight
	directorRole.readmit = (*App).directorReadmit
	directorRole.begin = (*App).directorSessionStart
	directorRole.watch = (*App).directorSessionWatchers
}

// directorReadmit is the Director's preflight at a reload: the copy's config admitted again, its runtime and the
// skill policies.
func (a *App) directorReadmit(p *startPlan, copyDir string) error {
	var err error
	if p.cfg, err = admitConfig(copyDir, p.cfgPath); err != nil {
		return err
	}
	if _, err := a.resolveRuntime(p); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := a.preflightSkillPolicies(p); err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	return nil
}

// directorSessionStart is the Director's steps at the start of every session.
func (a *App) directorSessionStart(p startPlan, copyDir, storePath, state string) (sessionStart, error) {
	var out sessionStart
	// Every session, the first and each reload's, starts with a workspace close sweep, so a Herdr workspace that
	// became due while no supervisor ran is closed; the session's end waits for a running sweep.
	sweep, err := a.workspaceCloseSweep(p.cfg, storePath, state)
	if err != nil {
		return out, err
	}
	out.closes = &workspaceCloseWatch{every: workspaceCloseEvery, round: sweep.run}
	out.closes.tick(time.Now())
	// With prune_daily on, the session prunes at its start when the copy's last prune was a day ago or more, and
	// then once a day, off the decision path; the session's end waits for a running prune.
	daily, err := a.dailyPrune(p.cfg, copyDir)
	if err != nil {
		out.wait()
		return sessionStart{}, err
	}
	if daily != nil {
		daily.p.storePath, daily.p.state = storePath, state
		out.prunes = &workspaceCloseWatch{every: pruneDailyEvery, round: daily.run}
		out.prunes.tick(time.Now())
	}
	fail := func(err error) (sessionStart, error) {
		out.wait()
		return sessionStart{}, err
	}
	status, err := readDirectorStatusConfig(context.Background(), storePath, state, p.cfgPath, a.Now)
	if err != nil {
		return fail(fmt.Errorf("in-flight snapshot: %w", err))
	}
	for _, it := range status.NeedsMe {
		if it.Kind == checksKind {
			out.inFlight = append(out.inFlight, fmt.Sprintf("%s: %s; the session supervisor advances it", it.Kind, oneLine(it.Reason)))
			continue
		}
		out.inFlight = append(out.inFlight, fmt.Sprintf("%s: %s; run `%s`", it.Kind, oneLine(it.Reason), it.Command))
	}
	// Parked attempts come from the same status read, so what a session is told cannot drift from the feed (docs/decisions/director-start.md).
	for _, p := range status.Parked {
		line := fmt.Sprintf("parked: attempt %d of %s of %s/%s %s; reason: %s; retry with `%s`",
			p.Attempt, p.Step, p.Repository, p.TaskID, p.Outcome, oneLine(p.Reason), p.Command)
		if len(p.UntilMerged) > 0 {
			line += " until " + strings.Join(p.UntilMerged, ", ")
		}
		out.parked = append(out.parked, line)
	}
	for _, h := range status.Held {
		out.held = append(out.held, heldLine(h))
	}
	// Running attempts come from the same read too, with their ages by the supervisor's clock.
	for _, r := range status.Running {
		line := fmt.Sprintf("running: attempt %d of %s of %s/%s; state %s", r.Attempt, r.Step, r.Repository, r.TaskID, r.State)
		if r.HeartbeatAt.IsZero() {
			line += "; no heartbeat"
		} else {
			line += "; heartbeat " + runningAge(a.now().Sub(r.HeartbeatAt)) + " ago"
		}
		if !r.LastOutputAt.IsZero() {
			line += "; last output " + runningAge(a.now().Sub(r.LastOutputAt)) + " ago"
		}
		out.running = append(out.running, fmt.Sprintf("%s; %s; check with `%s`", line, oneLine(r.Reason), r.Command))
	}
	// Every session, the first and each reload's, refreshes the repositories' policies and is told them as of then.
	a.refreshPolicies(context.Background(), p.cfg.Repositories...)
	out.policies = startupPolicies(a.readPolicies(context.Background(), p.cfg.Repositories...))
	return out, nil
}

// directorSessionWatchers starts the Director's watchers of session n.
func (a *App) directorSessionWatchers(p startPlan, n int, dir, copyDir, storePath, state string) (sessionWatchers, error) {
	var w sessionWatchers
	var err error
	if w.checks, err = a.watchChecks(p, n, dir, copyDir, storePath, state); err != nil {
		return sessionWatchers{}, err
	}
	if w.deps, err = a.watchDependencies(p, n, dir, copyDir, storePath, state); err != nil {
		return sessionWatchers{}, err
	}
	if w.queue, err = a.watchMergeQueue(p, n, dir, copyDir, storePath, state); err != nil {
		return sessionWatchers{}, err
	}
	if w.starts, err = a.watchStartQueue(p, n, dir, copyDir, storePath, state); err != nil {
		return sessionWatchers{}, err
	}
	if w.holds, err = a.watchHolds(p, dir, copyDir, storePath, state); err != nil {
		return sessionWatchers{}, err
	}
	return w, nil
}

// sessionMaintenance is the maintenance policy of a session of role r under cfg: the config's cadences for a role
// with maintenance, else none.
func sessionMaintenance(r *sessionRole, cfg *config.Config) (store.MaintenancePolicy, error) {
	if !r.maintenance {
		return store.MaintenancePolicy{}, nil
	}
	return maintenancePolicy(cfg)
}
