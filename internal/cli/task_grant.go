package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskGrantUsage = `usage:
  guildhall task grant create --store <path> --config <director.toml> --repo <id> --task <id> --step <id|all> --tool <rule> [--tool <rule>]... --reason <text> [--until attempt|done]
  guildhall task grant revoke --store <path> --repo <id> --task <id> --grant <id> --reason <text>
  guildhall task grant list --store <path> [--repo <id>] [--task <id>]
--copy <dir>, or the copy found from the current directory, may stand in for --store and --config: the copy's
director.store and config.toml`

// taskGrant runs a task grant subcommand and prints its result as one JSON document (docs/tasks.md#task-grants).
// create records a grant of extra allowed_tools rules to one task's attempts of one step, or of every claude step,
// and opens its decision grant-<id>, with the mutation "task-grant:<repo>:<task>:<id>", printing the receipt; a retry
// of the same grant while it is pending or active prints the original receipt. revoke records a revoke once, with the
// mutation "task-grant-revoke:<repo>:<task>:<id>", printing its receipt; list prints every grant with its derived
// state, read-only. It exits 0 on success, 1 when the configuration or the store refuses or fails and 2 on a usage
// error.
func (a *App) taskGrant(args []string) int {
	if len(args) == 0 {
		return a.taskGrantUsage("missing task grant subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("task grant "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	task := flags.String("task", "", "task id")
	step := flags.String("step", "", "step id, or all")
	grant := flags.String("grant", "", "grant id")
	reason := flags.String("reason", "", "reason")
	until := flags.String("until", string(store.GrantUntilAttempt), "attempt or done")
	var tools stringList
	flags.Var(&tools, "tool", "allowed_tools rule; repeat for more")
	var cp copyFlags
	cp.register(flags)
	var required, allowed []string
	var copyConfig *string
	switch sub {
	case "create":
		required = []string{"store", "config", "repo", "task", "step", "tool", "reason"}
		allowed = append(required, "copy", "until")
		copyConfig = cfgPath
	case "revoke":
		required = []string{"store", "repo", "task", "grant", "reason"}
		allowed = append(required, "copy")
	case "list":
		required, allowed = []string{"store"}, []string{"store", "copy", "repo", "task"}
	default:
		return a.taskGrantUsage(fmt.Sprintf("unknown task grant subcommand %q", sub))
	}
	if err := flags.Parse(args[1:]); err != nil {
		return a.taskGrantUsage(err.Error())
	}
	if flags.NArg() > 0 {
		return a.taskGrantUsage(fmt.Sprintf("task grant %s takes no arguments", sub))
	}
	var usageErr string
	flags.Visit(func(f *flag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			usageErr = fmt.Sprintf("--%s does not apply to task grant %s", f.Name, sub)
		}
	})
	if err := cp.apply(a, path, copyConfig, nil); err != nil {
		return a.taskGrantUsage(err.Error())
	}
	if cp.searched && usageErr == "" {
		return a.taskGrantUsage(cp.missing(a).Error())
	}
	for _, name := range required {
		if flags.Lookup(name).Value.String() == "" {
			usageErr = fmt.Sprintf("task grant %s requires --%s", sub, name)
		}
	}
	for _, v := range []string{*repo, *task} {
		if v != "" && !workflow.ValidID(v) {
			usageErr = fmt.Sprintf("id %q must be lowercase letters, digits and hyphens, starting with a letter", v)
		}
	}
	switch {
	case usageErr != "":
	case sub == "list" && *task != "" && *repo == "":
		usageErr = "task grant list --task needs --repo"
	case *until != string(store.GrantUntilAttempt) && *until != string(store.GrantUntilDone):
		usageErr = fmt.Sprintf("--until %q must be %s or %s", *until, store.GrantUntilAttempt, store.GrantUntilDone)
	case sub == "create" && *step == store.GrantAllSteps && *until != string(store.GrantUntilDone):
		usageErr = "--step all needs --until done: the next attempt of every step has no clear meaning"
	}
	if usageErr != "" {
		return a.taskGrantUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task grant %s: %v\n", sub, err)
		return 1
	}
	var cfg *config.Config
	var r config.Repository
	if sub == "create" {
		var err error
		if cfg, r, err = loadRepository(*cfgPath, *repo); err != nil {
			return fail(err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub == "list", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	switch sub {
	case "create":
		g := store.TaskGrant{Repository: *repo, TaskID: *task, Step: *step, Rules: tools, Reason: *reason, Until: store.GrantUntil(*until)}
		out, err = createTaskGrant(s, filepath.Join(filepath.Dir(*cfgPath), "workflows"), cfg, r, g)
	case "revoke":
		out, err = s.RevokeTaskGrant(mutationID("task-grant-revoke", *repo, *task, *grant), *repo, *task, *grant, *reason)
	case "list":
		out, err = s.TaskGrantStatuses(*repo, *task)
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}

// createTaskGrant records g on its task, whose bound workflow is resolved from dir under cfg and repository r.
// A pending or active grant of the task with the same step, rules, reason and until is a retry, recorded
// again under its own id so the store returns its original receipt; any other grant takes the task's next id.
// The same grant proposed by a Director question is refused instead, naming it: the Guildmaster's answer choosing
// its option approves it, so it needs no create.
func createTaskGrant(s *store.Store, dir string, cfg *config.Config, r config.Repository, g store.TaskGrant) (store.Receipt, error) {
	t, err := s.Task(g.Repository, g.TaskID)
	if err != nil {
		return store.Receipt{}, err
	}
	steps, err := grantSteps(s, dir, cfg, r, t)
	if err != nil {
		return store.Receipt{}, err
	}
	existing, err := s.TaskGrantStatuses(g.Repository, g.TaskID)
	if err != nil {
		return store.Receipt{}, err
	}
	for _, e := range existing {
		if (e.State == store.GrantPending || e.State == store.GrantActive) && sameGrant(e.TaskGrant, g) {
			if e.ApprovingOption != "" {
				return store.Receipt{}, fmt.Errorf("%w: grant %s of %s/%s is %s, proposed by question %s's option %q; the Guildmaster's answer choosing it approves the grant, so it needs no create",
					store.ErrExists, e.ID, g.Repository, g.TaskID, e.State, e.Decision, e.ApprovingOption)
			}
			g.ID = e.ID
		}
	}
	if g.ID == "" {
		g.ID = nextGrantID(existing)
	}
	return s.CreateTaskGrant(mutationID("task-grant", g.Repository, g.TaskID, g.ID), g, steps)
}

// sameGrant reports whether grants a and b grant the same: step, rules in order, reason and until.
func sameGrant(a, b store.TaskGrant) bool {
	return a.Step == b.Step && slices.Equal(a.Rules, b.Rules) && a.Reason == b.Reason && a.Until == b.Until
}

// nextGrantID is the id after the highest of a task's grants existing, g1 for none.
func nextGrantID(existing []store.TaskGrantStatus) string {
	next := 0
	for _, e := range existing {
		if n, err := strconv.Atoi(strings.TrimPrefix(e.ID, "g")); err == nil && n > next {
			next = n
		}
	}
	return store.TaskGrantID(next + 1)
}

// grantSteps are the steps of task t a grant may name, the store's steps argument: the Artisan steps of its bound
// workflow, resolved from dir under cfg and repository r, whose profile runs an adapter that takes granted tools.
// task grant create and decide open with a grant share it.
func grantSteps(s *store.Store, dir string, cfg *config.Config, r config.Repository, t store.Task) ([]string, error) {
	def, err := boundWorkflow(dir, cfg, r, t.Workflow)
	if err != nil {
		return nil, fmt.Errorf("task %s/%s: %w; its steps cannot be checked", t.Repository, t.ID, err)
	}
	// An unattended grill runs as an Artisan, so it is granted like any other Artisan step.
	if def, err = withGrillMode(s, t.Repository, t.ID, def); err != nil {
		return nil, err
	}
	var steps []string
	for _, step := range def.Steps {
		if step.Actor() != workflow.ArtisanActor {
			continue
		}
		if p, err := cfg.Profile(workflow.EffectiveProfile(step)); err == nil && adapter.GrantsTools(p.Adapter) {
			steps = append(steps, step.ID)
		}
	}
	return steps, nil
}

func (a *App) taskGrantUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskGrantUsage)
	return 2
}
