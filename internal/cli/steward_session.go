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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// stewardStoreFile is a Steward copy's store, beside its config.toml (docs/steward.md#the-steward-session).
const stewardStoreFile = "steward.store"

// The Steward store's own records live under one reserved repository: the task its rollout approval decisions
// belong to and the task the Guildmaster's messages to the Steward name, since every decision and message names a
// task (docs/steward.md#approval-in-a-steward-session).
const (
	stewardRepository    = "steward"
	stewardRolloutsTask  = "rollouts"
	stewardGuildmasterTk = "guildmaster"
)

// stewardApprovalOptions are a rollout approval decision's options; only stewardApprove approves.
const (
	stewardApprove = "approve"
	stewardDecline = "decline"
)

// stewardRollouts is the directory under a Steward copy that holds its rollout plans, each in its own directory,
// where steward status reads their receipts.
const stewardRollouts = "rollouts"

// stewardRole is the Steward's session: admitted by admitStewardConfig, run under [steward], with no task steps;
// its startup.md lists the rollout approvals waiting for the Guildmaster or approved and not yet applied.
var stewardRole = &sessionRole{
	command: "steward", title: "Steward", storeFile: stewardStoreFile, roleFile: "roles/steward.md",
	admit: admitStewardConfig,
	settings: func(cfg *config.Config) sessionSettings {
		if s := cfg.Steward; s != nil {
			return sessionSettings{Adapter: s.Adapter, Model: s.Model, Effort: s.Effort, Role: s.Role}
		}
		return sessionSettings{}
	},
	name: func(cfg *config.Config) string {
		if cfg.Steward == nil {
			return ""
		}
		return cfg.Steward.Name
	},
}

func init() {
	stewardRole.preflight = (*App).stewardPreflight
	stewardRole.readmit = (*App).stewardReadmit
	stewardRole.begin = (*App).stewardSessionStart
	stewardRole.watch = func(*App, startPlan, int, string, string, string, string) (sessionWatchers, error) {
		return sessionWatchers{}, nil
	}
}

// admitStewardConfig loads a Steward copy's config for a Steward session: a steward-mode config whose [steward]
// table names the session. A Steward copy is never managed, so a managed.toml beside it is refused.
func admitStewardConfig(copyDir, cfgPath string) (*config.Config, error) {
	if _, err := os.Stat(filepath.Join(copyDir, "managed.toml")); err == nil {
		return nil, errors.New("managed.toml: a Steward copy is never managed; remove it")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.Mode != config.ModeSteward:
		return nil, fmt.Errorf("%s is a %s config, not the steward's", cfgPath, cfg.Mode)
	case len(cfg.Managed) > 0:
		return nil, errors.New("inline [managed] settings: a Steward copy is never managed")
	case cfg.Steward == nil || cfg.Steward.Name == "":
		return nil, fmt.Errorf("%s has no [steward] name, which labels the Steward's Herdr workspace; set it", cfgPath)
	}
	return cfg, nil
}

// stewardPreflight checks, in order, that the config is admitted, the runtime is known and on PATH, guildhall is on
// PATH for the session's bare commands, the store opens, the private config checkout is an absolute directory that
// is a Git work tree, its roster is valid, ssh and scp are on PATH and the release skills install, printing one
// doctor-style line per check. A failed check prints its line, then errStartRefused; nothing is recorded.
func (a *App) stewardPreflight(copyDir, cfgPath, storePath, positional string) (startPlan, error) {
	p := startPlan{role: stewardRole, cfgPath: cfgPath, positional: positional}
	line := a.preflightLine
	refuse := func(name string, err error) (startPlan, error) { return p, a.refuseRoleCheck("steward", name, err) }
	cfg, err := admitStewardConfig(copyDir, cfgPath)
	if err != nil {
		return refuse("config", err)
	}
	p.cfg = cfg
	line("config", "admitted (%s)", cfgPath)
	for _, w := range cfg.Deprecations() {
		line("config", "warning: %s", w)
	}
	program, err := a.resolveRuntime(&p)
	if err != nil {
		return refuse("runtime", err)
	}
	line("runtime", "%s (%s)", p.name, program)
	guildhall, err := a.LookPath("guildhall")
	if err != nil {
		return refuse("guildhall", fmt.Errorf("guildhall must be on PATH for session hooks and Steward commands; add the running executable's directory to PATH: %w", err))
	}
	line("guildhall", "%s", guildhall)
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return refuse("store", noStore(storePath))
	}
	if err := ensureStewardRecords(storePath); err != nil {
		return refuse("store", err)
	}
	line("store", "opens (%s)", storePath)
	private, err := a.privateCheckout(cfg)
	if err != nil {
		return refuse("private config", err)
	}
	line("private config", "%s (a Git work tree)", private)
	roster, err := readRoster(private)
	if err != nil {
		return refuse("roster", err)
	}
	directors := 0
	for _, m := range roster.Machines {
		directors += len(m.Directors)
	}
	line("roster", "%d machines, %d Director copies", len(roster.Machines), directors)
	for _, prog := range []string{"ssh", "scp"} {
		path, err := a.LookPath(prog)
		if err != nil {
			return refuse(prog, fmt.Errorf("%s must be on PATH, as rollouts reach Director copies over SSH: %w", prog, err))
		}
		line(prog, "%s", path)
	}
	if err := a.installSessionSkills(&p); err != nil {
		return refuse("skills", err)
	}
	if err := a.preflightSkillPolicies(&p); err != nil {
		return refuse("skills", err)
	}
	return p, nil
}

// installSessionSkills installs the release skills and baseline into a new temporary directory for p, which
// p.cleanup removes, and prints how many skills installed.
func (a *App) installSessionSkills(p *startPlan) error {
	if a.InstallSkills == nil || a.InstallBaseline == nil {
		return errors.New("release skills and baseline installers are not configured")
	}
	tmp, err := os.MkdirTemp("", "guildhall-session-*")
	if err != nil {
		return err
	}
	p.cleanup = func() { _ = os.RemoveAll(tmp) }
	p.skills, p.baseline = filepath.Join(tmp, "skills"), filepath.Join(tmp, "baseline")
	if err := a.InstallSkills(p.skills); err != nil {
		return err
	}
	if err := a.InstallBaseline(p.baseline); err != nil {
		return err
	}
	entries, err := os.ReadDir(p.skills)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	a.preflightLine("skills", "%d release skills install", n)
	return nil
}

// stewardReadmit is the Steward's preflight at a reload: the copy's config admitted again, its runtime and the
// skill policies.
func (a *App) stewardReadmit(p *startPlan, copyDir string) error {
	var err error
	if p.cfg, err = admitStewardConfig(copyDir, p.cfgPath); err != nil {
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

// privateCheckout is cfg's private config location, refused unless it is an absolute local directory that is a Git
// work tree, the checkout of the private config repository (R1.3).
func (a *App) privateCheckout(cfg *config.Config) (string, error) {
	private := cfg.PrivateConfig.Location
	if info, err := os.Stat(private); !filepath.IsAbs(private) || err != nil || !info.IsDir() {
		return "", fmt.Errorf("private_config.location %q must be an absolute local directory, the checkout of the private config repository; clone it first", private)
	}
	git, err := a.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git must be on PATH to check the private config checkout: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := a.Output(ctx, git, "-C", private, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", fmt.Errorf("private_config.location %s is not a Git work tree; clone the private config repository there: %v", private, errors.Join(err, errors.New(oneLine(string(out)))))
	}
	return private, nil
}

// readRoster reads and validates roster.toml in the private config directory.
func readRoster(private string) (*steward.Roster, error) {
	data, err := os.ReadFile(filepath.Join(private, "roster.toml"))
	if err != nil {
		return nil, err
	}
	return steward.ParseRoster(data)
}

// stewardTaskDigest stands in for the workflow and config digests of the Steward store's reserved tasks, which run
// no workflow, so recording them again is the same request.
var stewardTaskDigest = string(digest.Of([]byte("guildhall steward reserved task")))

// ensureStewardRecords records, once, the Steward store's reserved tasks under stewardRepository; recording them
// again changes nothing.
func ensureStewardRecords(storePath string) error {
	_, err := withStore(storePath, false, func(s *store.Store) (struct{}, error) {
		for _, t := range []store.Task{
			{ID: stewardRolloutsTask, Title: "Rollout approvals", Detail: "The Guildmaster's approvals of rollout plan digests, one decision per plan."},
			{ID: stewardGuildmasterTk, Title: "Messages from the Guildmaster", Detail: "The task the Guildmaster's messages to the Steward name."},
		} {
			t.Repository = stewardRepository
			t.Workflow = workflow.Revision{Name: "steward", Digest: stewardTaskDigest}
			t.ConfigDigest = stewardTaskDigest
			if _, err := s.CreateTask("steward-task:"+t.ID, t); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// stewardSessionStart is the Steward's steps at the start of every session: its reserved records, then startup.md's
// in-flight items, each rollout approval the Guildmaster has not answered and each one approved.
func (a *App) stewardSessionStart(p startPlan, copyDir, storePath, state string) (sessionStart, error) {
	if err := ensureStewardRecords(storePath); err != nil {
		return sessionStart{}, err
	}
	approvals, err := withStore(storePath, true, func(s *store.Store) ([]store.Decision, error) {
		return s.Decisions(stewardRepository, stewardRolloutsTask)
	})
	if err != nil {
		return sessionStart{}, fmt.Errorf("rollout approvals: %w", err)
	}
	var out sessionStart
	for _, d := range approvals {
		plan := ""
		if d.Subject != nil {
			plan = d.Subject.Digest
		}
		switch {
		case d.Open():
			out.inFlight = append(out.inFlight, fmt.Sprintf("rollout approval %s: plan %s waits for the Guildmaster's answer; show them the plan", d.ID, plan))
		case d.Answered && d.Choice == stewardApprove:
			out.inFlight = append(out.inFlight, fmt.Sprintf("rollout approval %s: the Guildmaster approved plan %s; apply it with --approve %s unless `guildhall steward status` shows it applied", d.ID, plan, plan))
		}
	}
	return out, nil
}

// approvalDecisionID is the id of the approval decision of plan digest d: "rollout-" and its first 12 hex digits.
func approvalDecisionID(d string) string {
	hex := strings.TrimPrefix(d, "sha256:")
	return "rollout-" + hex[:min(12, len(hex))]
}

// stewardApproved reports whether the Steward store at storePath holds the Guildmaster's approval of plan digest d:
// its approval decision answered "approve" and bound to d.
func stewardApproved(storePath, d string) (bool, error) {
	if _, err := os.Stat(storePath); err != nil {
		return false, fmt.Errorf("no Steward store at %s to hold the Guildmaster's approval: %w", storePath, err)
	}
	return withStore(storePath, true, func(s *store.Store) (bool, error) {
		ds, err := s.Decisions(stewardRepository, stewardRolloutsTask)
		if err != nil {
			return false, err
		}
		for _, x := range ds {
			if x.ID == approvalDecisionID(d) {
				return x.Answered && x.Choice == stewardApprove && x.Subject != nil && x.Subject.Digest == d, nil
			}
		}
		return false, nil
	})
}

// inSession reports whether this command runs inside a Guildhall session or an Artisan attempt, where an agent, not
// the Guildmaster, runs it.
func (a *App) inSession() bool {
	return a.getenv(session.NumberEnv) != "" || a.getenv(artisan.AttemptEnv) != ""
}

func (a *App) getenv(name string) string {
	if a.Getenv == nil {
		return ""
	}
	return a.Getenv(name)
}

// stewardLifecycle runs `guildhall steward pause|resume|ready|stop|reload|handoff`: the Director's command of the
// same name on the Steward copy's store, refused for any copy that is not a Steward's. handoff is reload --now: a
// fresh session picking up from this one (docs/steward.md#lifecycle).
func (a *App) stewardLifecycle(sub string, args []string) int {
	name := "steward " + sub
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "Steward store file")
	state := fl.String("state", "", "state directory")
	fl.Bool("now", false, "")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError(name + ": " + err.Error())
	}
	if err := cp.apply(a, path, nil, state); err != nil {
		return a.usageError(name + ": " + err.Error())
	}
	switch {
	case *path == "":
		return a.usageError(name + ": " + cp.missing(a).Error())
	case filepath.Base(*path) != stewardStoreFile:
		return a.fail(fmt.Errorf("%s: %s is not a Steward copy's store, %s; run it on the Steward's copy", name, *path, stewardStoreFile))
	}
	// The Director's command finds the same store again, from --copy or by discovery.
	switch sub {
	case "pause":
		return a.directorPause(args)
	case "resume":
		return a.directorResume(args)
	case "ready":
		return a.directorReady(args)
	case "stop":
		return a.directorStop(args)
	case "reload":
		return a.directorReload(args)
	case "handoff":
		return a.directorReload(append(slices.Clone(args), "--now"))
	}
	return a.usageError(name + ": unknown command")
}

// stewardStatus is what `steward status` prints.
type stewardStatus struct {
	Session   *store.DirectorSession `json:"session"`
	Roster    []stewardMachine       `json:"roster"`
	Approvals []stewardApproval      `json:"approvals"`
	Problems  []string               `json:"problems,omitempty"`
}

// stewardMachine is one roster machine in steward status.
type stewardMachine struct {
	Label     string            `json:"label"`
	SSHAlias  string            `json:"ssh_alias"`
	OS        steward.OS        `json:"os"`
	Directors []stewardDirector `json:"directors"`
}

// stewardDirector is one roster Director copy in steward status, with its last rollout receipt, if any.
type stewardDirector struct {
	Name         string          `json:"name"`
	CopyDir      string          `json:"copy_dir"`
	Repositories []string        `json:"repositories"`
	LastRollout  *stewardReceipt `json:"last_rollout"`
}

// stewardReceipt is a copy's part of its newest rollout receipt.
type stewardReceipt struct {
	PlanDigest string    `json:"plan_digest"`
	Status     string    `json:"status"`
	FinishedAt time.Time `json:"finished_at"`
	Receipt    string    `json:"receipt"`
	Error      string    `json:"error,omitempty"`
}

// stewardApproval is one rollout approval decision in steward status.
type stewardApproval struct {
	Decision string `json:"decision"`
	Plan     string `json:"plan"`
	State    string `json:"state"`
}

// stewardStatusCmd runs `guildhall steward status [--copy <dir>]`: the newest Steward session, every roster machine
// and Director copy with its last rollout receipt from the plans under the copy's rollouts/, and the rollout
// approvals. It reads only local files: nothing reaches a Director.
func (a *App) stewardStatusCmd(args []string) int {
	fl := flag.NewFlagSet("steward status", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if err := fl.Parse(args); err != nil {
		return a.usageError("steward status: " + err.Error())
	}
	var storePath, cfgPath string
	switch err := cp.apply(a, &storePath, &cfgPath, nil); {
	case err != nil:
		return a.usageError("steward status: " + err.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("steward status: unexpected argument %q", fl.Arg(0)))
	case cp.dir == "":
		return a.usageError("steward status: " + cp.missing(a).Error())
	}
	storePath = filepath.Join(cp.dir, stewardStoreFile)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("steward status: %w", err))
	}
	if cfg.Mode != config.ModeSteward {
		return a.fail(fmt.Errorf("steward status: %s is a %s config, not the steward's", cfgPath, cfg.Mode))
	}
	out := stewardStatus{Roster: []stewardMachine{}, Approvals: []stewardApproval{}}
	if _, err := os.Stat(storePath); err == nil {
		type read struct {
			session   *store.DirectorSession
			decisions []store.Decision
		}
		r, err := withStore(storePath, true, func(s *store.Store) (read, error) {
			var r read
			switch d, err := s.LatestDirectorSession(); {
			case err == nil:
				r.session = &d
			case !errors.Is(err, store.ErrNotFound):
				return r, err
			}
			ds, err := s.Decisions(stewardRepository, stewardRolloutsTask)
			r.decisions = ds
			return r, err
		})
		if err != nil {
			return a.fail(fmt.Errorf("steward status: %w", err))
		}
		out.Session = r.session
		for _, d := range r.decisions {
			ap := stewardApproval{Decision: d.ID, State: "waiting"}
			if d.Subject != nil {
				ap.Plan = d.Subject.Digest
			}
			switch {
			case d.Void:
				ap.State = "void"
			case d.Answered && d.Choice == stewardApprove:
				ap.State = "approved"
			case d.Answered:
				ap.State = "declined"
			}
			out.Approvals = append(out.Approvals, ap)
		}
	} else {
		out.Problems = append(out.Problems, noStore(storePath).Error())
	}
	receipts, problems := latestReceipts(filepath.Join(cp.dir, stewardRollouts))
	out.Problems = append(out.Problems, problems...)
	roster, err := readRoster(cfg.PrivateConfig.Location)
	if err != nil {
		out.Problems = append(out.Problems, fmt.Sprintf("roster: %v", err))
	} else {
		for _, m := range roster.Machines {
			sm := stewardMachine{Label: m.Label, SSHAlias: m.SSHAlias, OS: m.OS, Directors: []stewardDirector{}}
			for _, d := range m.Directors {
				sd := stewardDirector{Name: d.Name, CopyDir: d.CopyDir, Repositories: d.Repositories}
				if r, ok := receipts[m.Label+"/"+d.Name]; ok {
					sd.LastRollout = &r
				}
				sm.Directors = append(sm.Directors, sd)
			}
			out.Roster = append(out.Roster, sm)
		}
	}
	return a.printJSON(out)
}

// latestReceipts reads every rollout receipt in the plan directories under root and returns each copy's newest
// part, keyed "<machine>/<director>", with a line for each receipt that cannot be read. A missing root has none.
func latestReceipts(root string) (map[string]stewardReceipt, []string) {
	out := map[string]stewardReceipt{}
	files, err := filepath.Glob(filepath.Join(root, "*", "receipt-*.json"))
	if err != nil {
		return out, []string{err.Error()}
	}
	var problems []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		var r steward.Receipt
		if err == nil {
			err = json.Unmarshal(data, &r)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("receipt %s: %v", f, err))
			continue
		}
		for _, c := range r.Copies {
			key := c.Machine + "/" + c.Director
			if prev, ok := out[key]; ok && !r.FinishedAt.After(prev.FinishedAt) {
				continue
			}
			out[key] = stewardReceipt{PlanDigest: r.PlanDigest, Status: c.Status, FinishedAt: r.FinishedAt, Receipt: f, Error: c.Error}
		}
	}
	return out, problems
}

// stewardApprovalCmd runs `guildhall steward approval request --plan <dir> [--copy <dir>]`: it opens, once, the
// decision through which the Guildmaster approves the dry-run plan in --plan by its exact digest, and prints it with
// the command the Guildmaster answers it with. Only an answer of "approve" to that decision lets a Steward session
// apply the plan with --approve (docs/steward.md#approval-in-a-steward-session).
func (a *App) stewardApprovalCmd(args []string) int {
	if len(args) == 0 || args[0] != "request" {
		return a.usageError("steward approval: the command is request")
	}
	fl := flag.NewFlagSet("steward approval request", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	planDir := fl.String("plan", "", "the dry run's plan directory")
	if err := parseInterspersed(fl, args[1:]); err != nil {
		return a.usageError("steward approval request: " + err.Error())
	}
	var storePath string
	switch err := cp.apply(a, &storePath, nil, nil); {
	case err != nil:
		return a.usageError("steward approval request: " + err.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("steward approval request: unexpected argument %q", fl.Arg(0)))
	case cp.dir == "":
		return a.usageError("steward approval request: " + cp.missing(a).Error())
	case *planDir == "":
		return a.usageError("steward approval request: --plan is required")
	}
	storePath = filepath.Join(cp.dir, stewardStoreFile)
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("steward approval request: %w", noStore(storePath)))
	}
	plan, err := steward.ReadPlan(*planDir)
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	if !plan.NeedsApproval() {
		return a.fail(fmt.Errorf("steward approval request: plan %s changes nothing that needs the Guildmaster's approval; apply it without --approve", plan.Digest))
	}
	if err := ensureStewardRecords(storePath); err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	var changes []string
	for _, c := range plan.Copies {
		for _, ch := range c.Changes {
			if ch.NeedsApproval {
				changes = append(changes, fmt.Sprintf("%s/%s %s %s (%s)", c.Machine, c.Director, ch.Kind, ch.Key, ch.Reason))
			}
		}
	}
	id := approvalDecisionID(plan.Digest)
	d := store.Decision{Repository: stewardRepository, TaskID: stewardRolloutsTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: plan.Digest}, Options: []string{stewardApprove, stewardDecline},
		Question: truncateUTF8(fmt.Sprintf("Approve rollout plan %s to %s? It needs your approval for: %s. Plan and previews: %s",
			plan.Digest, strings.Join(plan.Machines, ", "), strings.Join(changes, "; "), plan.StageDir), store.MaxTextBytes)}
	out, err := withStore(storePath, false, func(s *store.Store) (store.Decision, error) {
		_, err := s.OpenDecision("steward-approval:"+plan.Digest, d)
		if err != nil && !errors.Is(err, store.ErrExists) {
			return store.Decision{}, err
		}
		return findDecision(s, stewardRepository, stewardRolloutsTask, id)
	})
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	copyDir, _ := filepath.Abs(cp.dir)
	return a.printJSON(struct {
		Decision store.Decision `json:"decision"`
		Answer   string         `json:"answer"`
	}{out, fmt.Sprintf("guildhall decide answer --copy %s --repo %s --task %s --decision %s --answer-file -", shellQuote([]string{copyDir}), stewardRepository, stewardRolloutsTask, id)})
}

// StewardDecisionOptions returns the option sets of the decisions the Steward opens, by decision kind: a rollout
// plan's approval, approve or decline, so a reader such as the task page's option-meanings table can check it covers
// every one (docs/contracts/web.md). The Steward opens them in its own store, which a Director's page never reads.
func StewardDecisionOptions() map[store.DecisionKind][][]string {
	return map[store.DecisionKind][][]string{store.DecisionApproval: {{stewardApprove, stewardDecline}}}
}
