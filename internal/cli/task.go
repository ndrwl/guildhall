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
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var taskUsage = `usage:
  guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--detail <text> | --detail-file <file|->] [--kind feature|bug|hallmark] [--depends-on <repo>/<task>]... [--fixes <repo>/<task>] [--priority <level>] [--override <key>=<value>... --override-quote <text>] <task>
  guildhall task priority --store <path> --repo <id> <task> <level>
  guildhall task hold-merge --store <path> --repo <id> <task>
  guildhall task show --store <path> [--config <director.toml>] --repo <id> <task> --json
  guildhall task list --store <path> [--repo <id>] [--workflow <name>] [--open] --json
  guildhall task timeline --store <path> --repo <id> <task> --json
  guildhall task files --store <path> --repo <id> <task> --json
  guildhall task finding rule --store <path> --repo <id> --outcome <document> --finding <id> --move out-of-scope|duplicate|defer --reason <text> <task>
  guildhall task review --full --store <path> --repo <id> <task>
  guildhall task start --store <path> --config <director.toml> --state <dir> --repo <id> [--decision <id>] [--ignore-deps --reason <text>] <task>
  guildhall task cancel --store <path> --config <director.toml> --state <dir> --repo <id> --reason <text> <task>
  guildhall task reconcile --store <path> --config <director.toml> --state <dir> --repo <id> <task>
  guildhall task grant create|revoke|list ... (guildhall task grant for its usage)
  guildhall task context add|list ... (guildhall task context for its usage)
  guildhall task link add|remove|list ... (guildhall task link for its usage)
  guildhall task override set|unset|list ... (guildhall task override for its usage)
  guildhall task design --repo <id> --task <id> --brief <file|-> ... (guildhall task design --help for its usage)
--copy <dir>, or the copy found from the current directory, may stand in for --store, --config and --state:
the copy's director.store, config.toml and state
--title is a short name of at most 100 characters; the full statement goes in --detail, or --detail-file reads it
from a file or - for standard input
--kind chooses what the task is created as, which chooses its workflow; --fixes without --kind means bug:
` + taskKindHelp

// taskKindDescriptions describe each task kind for task new's help and the Director's tasks skill, which copies them,
// keyed by kind and read from config.TaskKindTable.
var taskKindDescriptions = func() map[string]string {
	m := map[string]string{}
	for _, row := range config.TaskKindTable {
		m[row.Name] = row.Description
	}
	return m
}()

var taskKindHelp = func() string {
	var b strings.Builder
	for _, k := range config.TaskKinds {
		fmt.Fprintf(&b, "  %-8s %s\n", k, taskKindDescriptions[k])
	}
	return strings.TrimSuffix(b.String(), "\n")
}()

// taskKindChoice lists the task kinds as a choice, "feature, bug or hallmark".
var taskKindChoice = strings.Join(config.TaskKinds[:len(config.TaskKinds)-1], ", ") + " or " + config.TaskKinds[len(config.TaskKinds)-1]

// taskKind is the --kind flag: the kind a new task is created as, "" when not given.
type taskKind string

func (k *taskKind) String() string { return string(*k) }

func (k *taskKind) Set(v string) error {
	if !slices.Contains(config.TaskKinds, v) {
		return fmt.Errorf("--kind %q is not a task kind; use %s", v, taskKindChoice)
	}
	*k = taskKind(v)
	return nil
}

// resolve returns the kind a new task is created as: the --kind given, otherwise bug for a task that fixes another
// and feature for any other.
func (k taskKind) resolve(fixes *store.TaskRef) string {
	switch {
	case k != "":
		return string(k)
	case fixes != nil:
		return config.TaskKindBug
	}
	return config.TaskKindFeature
}

// dependsOn collects repeated --depends-on flags in the order given.
type dependsOn []store.TaskRef

func (d *dependsOn) String() string { return fmt.Sprint(*d) }

func (d *dependsOn) Set(v string) error {
	repo, task, ok := strings.Cut(v, "/")
	if !ok || !workflow.ValidID(repo) || !workflow.ValidID(task) {
		return fmt.Errorf("--depends-on %q must be <repository>/<task>", v)
	}
	*d = append(*d, store.TaskRef{Repository: repo, ID: task})
	return nil
}

// fixes is the --fixes flag: the earlier task whose defect the new one fixes.
type fixes struct{ ref *store.TaskRef }

func (f *fixes) String() string { return fmt.Sprint(f.ref) }

func (f *fixes) Set(v string) error {
	repo, task, ok := strings.Cut(v, "/")
	if !ok || !workflow.ValidID(repo) || !workflow.ValidID(task) {
		return fmt.Errorf("--fixes %q must be <repository>/<task>", v)
	}
	f.ref = &store.TaskRef{Repository: repo, ID: task}
	return nil
}

// taskDetail is what task show prints: one task and every record it has.
type taskDetail struct {
	Task      taskView             `json:"task"`
	Attempts  []store.StageAttempt `json:"attempts"`
	Documents []store.DocumentRef  `json:"documents"`
	Decisions []store.Decision     `json:"decisions"`
	// Context is the Guildmaster context the Director added to the task, by number.
	Context []store.TaskContext `json:"context"`
	// Links are the task's link records, adds and removes, by number.
	Links []store.TaskLink `json:"links"`
	// Overrides are the task's overrides in effect, the latest set record of each key, in key order.
	Overrides []store.TaskOverride `json:"overrides"`
	// SkippedOverrides are those of Overrides the copy's config would not apply now, with why; nil, printed null,
	// when task show was given no config, so they were not computed.
	SkippedOverrides []config.Skipped `json:"skipped_overrides"`
	// AlsoRequires are the merge requirements Overrides add to the repository's merge_requires, in documented order;
	// null, like SkippedOverrides, without a config.
	AlsoRequires []string `json:"also_requires"`
	// DesignRounds is how many design rounds the task has used, counted from their briefs; absent when none.
	DesignRounds int `json:"design_rounds,omitempty"`
}

// task runs a task subcommand against an existing store and prints its result as one JSON document (docs/tasks.md).
// task new binds the repository's effective workflow revision for the task's kind and the configuration digest,
// and records the mutation "task:<repo>:<task>" (or "task:sha256:<hex digest>" of that string if over 128 bytes),
// so a retry prints the original receipt; show, list, timeline and files only read.
// task finding rule records the Director's ruling on one finding of a review outcome document with the mutation
// "ruling:<repo>:<task>:<document>:<finding>" (hashed the same way), and prints its receipt.
// task review --full records a full-review request with the mutation "full-review:<repo>:<task>:<n>", n the request's
// number, and prints its receipt; a retry while the request is pending prints the original receipt.
// timeline and files are the Director's inspection reads, labelled with the store sequence and
// workflow revision they describe (docs/tasks.md#inspection).
// It exits 0 on success, 1 when the configuration, workflow or store refuses or fails and 2 on a usage error.
func (a *App) task(args []string) int {
	if len(args) == 0 {
		return a.taskUsage("missing task subcommand")
	}
	sub := args[0]
	if sub == "grant" {
		return a.taskGrant(args[1:])
	}
	if sub == "context" {
		return a.taskContext(args[1:])
	}
	if sub == "link" {
		return a.taskLink(args[1:])
	}
	if sub == "override" {
		return a.taskOverride(args[1:])
	}
	if sub == "design" {
		return a.taskDesign(args[1:])
	}
	if sub == "finding" {
		if len(args) < 2 || args[1] != "rule" {
			return a.taskUsage("unknown task finding subcommand; the only one is rule")
		}
		sub, args = "finding rule", args[1:]
	}
	flags := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	title := flags.String("title", "", "task title")
	detail := flags.String("detail", "", "task detail")
	detailFile := flags.String("detail-file", "", "file holding the task detail, - for standard input")
	asJSON := flags.Bool("json", false, "print JSON")
	outcome := flags.String("outcome", "", "review outcome document")
	finding := flags.String("finding", "", "finding id")
	move := flags.String("move", "", "ruling move")
	reason := flags.String("reason", "", "ruling reason")
	full := flags.Bool("full", false, "request a full review")
	wfName := flags.String("workflow", "", "list only tasks bound to this workflow name")
	open := flags.Bool("open", false, "list only open tasks")
	priority := flags.String("priority", "", "task priority, p0 to p5")
	var deps dependsOn
	flags.Var(&deps, "depends-on", "task this one depends on")
	var fixed fixes
	flags.Var(&fixed, "fixes", "earlier task whose defect this one fixes")
	var kind taskKind
	flags.Var(&kind, "kind", "task kind: "+taskKindChoice)
	var overrides taskOverrideFlags
	flags.Var(&overrides, "override", "task override key=value, repeatable")
	overrideQuote := flags.String("override-quote", "", "the Guildmaster's words for the task's overrides")
	var cp copyFlags
	cp.register(flags)
	var want int
	var allowed map[string]bool
	switch sub {
	case "new":
		want, allowed = 1, map[string]bool{"store": true, "config": true, "repo": true, "title": true, "detail": true, "detail-file": true, "kind": true, "depends-on": true, "fixes": true, "priority": true,
			"override": true, "override-quote": true}
	case "priority":
		want, allowed = 2, map[string]bool{"store": true, "repo": true}
	case "hold-merge":
		want, allowed = 1, map[string]bool{"store": true, "repo": true}
	case "show":
		want, allowed = 1, map[string]bool{"store": true, "config": true, "repo": true, "json": true}
	case "timeline", "files":
		want, allowed = 1, map[string]bool{"store": true, "repo": true, "json": true}
	case "list":
		want, allowed = 0, map[string]bool{"store": true, "repo": true, "workflow": true, "open": true, "json": true}
	case "finding rule":
		want, allowed = 1, map[string]bool{"store": true, "repo": true, "outcome": true, "finding": true, "move": true, "reason": true}
	case "review":
		want, allowed = 1, map[string]bool{"store": true, "repo": true, "full": true}
	default:
		return a.taskUsage(fmt.Sprintf("unknown task subcommand %q", sub))
	}
	var positional []string
	for remaining := args[1:]; len(remaining) > 0; {
		arg := remaining[0]
		if arg == "--" {
			positional = append(positional, remaining[1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			remaining = remaining[1:]
			continue
		}
		n := 1
		name, _, inline := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if f := flags.Lookup(name); f != nil && !inline && name != "json" && name != "full" && name != "open" && len(remaining) > 1 {
			n = 2
		}
		if err := flags.Parse(remaining[:n]); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskUsage(err.Error())
		}
		remaining = remaining[n:]
	}
	allowed["copy"] = true
	var extra string
	flags.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			extra = f.Name
		}
	})
	var copyConfig *string
	if sub == "new" || sub == "show" {
		copyConfig = cfgPath
	}
	cerr := cp.apply(a, path, copyConfig, nil)
	switch {
	case extra != "":
		return a.taskUsage(fmt.Sprintf("--%s does not apply to task %s", extra, sub))
	case cerr != nil:
		return a.taskUsage(cerr.Error())
	case cp.searched:
		return a.taskUsage(cp.missing(a).Error())
	case *path == "":
		return a.taskUsage("--store is required, or --copy")
	case sub == "new" && (*cfgPath == "" || *repo == "" || *title == ""):
		return a.taskUsage("task new requires --config, --repo and --title")
	case *detail != "" && *detailFile != "":
		return a.taskUsage("give --detail or --detail-file, not both")
	case len(overrides) > 0 && *overrideQuote == "":
		return a.taskUsage("--override requires --override-quote, the Guildmaster's words")
	case len(overrides) == 0 && *overrideQuote != "":
		return a.taskUsage("--override-quote requires --override")
	case sub != "new" && sub != "list" && *repo == "":
		return a.taskUsage(fmt.Sprintf("task %s requires --repo", sub))
	case sub == "finding rule" && (*outcome == "" || *finding == "" || *move == "" || *reason == ""):
		return a.taskUsage("task finding rule requires --outcome, --finding, --move and --reason")
	case sub == "review" && !*full:
		return a.taskUsage("task review requires --full")
	case sub != "new" && sub != "finding rule" && sub != "review" && sub != "priority" && sub != "hold-merge" && !*asJSON:
		return a.taskUsage(fmt.Sprintf("task %s prints JSON only; pass --json", sub))
	case len(positional) != want:
		return a.taskUsage(fmt.Sprintf("task %s takes %d arguments", sub, want))
	}
	level := store.DefaultPriority
	if text := *priority; sub == "priority" || text != "" {
		if sub == "priority" {
			text = positional[1]
		}
		var err error
		if level, err = store.ParsePriority(text); err != nil {
			return a.taskUsage(strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
		}
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task %s: %v\n", sub, err)
		return 1
	}
	var task store.Task
	var accepted acceptedWorkflow
	if sub == "new" {
		// A long title is refused first, naming where its detail belongs; the store enforces the same limit.
		if n := utf8.RuneCountInString(*title); n > store.MaxTitleRunes {
			return fail(fmt.Errorf("title is %d characters; the limit is %d; put the full statement in --detail or --detail-file", n, store.MaxTitleRunes))
		}
		if *detailFile != "" {
			var err error
			if *detail, err = a.readText("detail", *detailFile, store.MaxTextBytes); err != nil {
				return fail(err)
			}
		}
		// Resolve configuration and validate the effective workflow before opening the store.
		// Task fields and dependencies are validated by CreateTask in the store.
		var err error
		if task, accepted, err = acceptTask(*cfgPath, *repo, kind.resolve(fixed.ref)); err != nil {
			return fail(err)
		}
		task.ID, task.Title, task.Detail, task.DependsOn, task.Fixes = positional[0], *title, *detail, deps, fixed.ref
		// Every override is judged before the store opens, as the workflow is, so a refused one creates no task.
		if len(overrides) > 0 {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return fail(fmt.Errorf("config: %w", err))
			}
			for _, o := range overrides {
				if err := cfg.CheckTaskOverride(o[0], o[1]); err != nil {
					return fail(fmt.Errorf("--override %s: %w", o[0], err))
				}
			}
		}
	}
	// task show judges the task's overrides against the copy's config when it has one; one that does not load
	// leaves them not judged rather than refusing, so show still reads the store.
	var showConfig *config.Config
	if sub == "show" && *cfgPath != "" {
		var err error
		if showConfig, err = config.Load(*cfgPath); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: task show: config: %v; skipped_overrides not judged\n", err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub != "new" && sub != "finding rule" && sub != "review" && sub != "priority" && sub != "hold-merge", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	switch sub {
	case "new":
		if err = refuseWhileRecovering(s); err != nil {
			break
		}
		if err = admitWorkflow(s, accepted); err != nil {
			err = fmt.Errorf("%w; the Guildmaster approves this revision with guildhall workflow approve --config <director.toml> --repo %s", err, *repo)
		} else {
			out, err = s.CreateTask(mutationID("task", task.Repository, task.ID), task)
		}
		if err == nil && *priority != "" {
			_, err = s.SetTaskPriority(mutationID("priority", task.Repository, task.ID, "1"), task.Repository, task.ID, level)
		}
		// Each override is its own record with its own mutation, as task override set records it, so a rerun replays it.
		for _, o := range overrides {
			if err != nil {
				break
			}
			_, err = addTaskOverride(s, task.Repository, task.ID, store.TaskOverrideSet, o[0], o[1], *overrideQuote)
		}
	case "priority":
		out, err = setPriority(s, *repo, positional[0], level)
	case "hold-merge":
		out, err = toggleMergeHold(s, *repo, positional[0])
	case "show":
		out, err = showTask(s, showConfig, *repo, positional[0])
	case "list":
		var tasks []store.Task
		if tasks, err = listTasks(s, *repo); err == nil {
			tasks = slices.DeleteFunc(tasks, func(t store.Task) bool {
				return (*wfName != "" && t.Workflow.Name != *wfName) || (*open && t.State != store.TaskOpen)
			})
			out, err = taskViews(s, tasks)
		}
	case "finding rule":
		r := store.Ruling{Repository: *repo, TaskID: positional[0], Document: *outcome, Finding: *finding, Move: store.RulingMove(*move), Reason: *reason}
		out, err = s.RuleFinding(mutationID("ruling", r.Repository, r.TaskID, r.Document, r.Finding), r)
	case "review":
		out, err = requestFullReview(s, *repo, positional[0])
	case "timeline", "files":
		var h store.TaskHistory
		if h, err = s.TaskHistory(*repo, positional[0]); err == nil && sub == "timeline" {
			tl := timeline(h)
			tl.Merged, err = mergedMessage(s, h)
			out = tl
		} else if err == nil {
			out = files(h)
		}
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

// requestFullReview records the task's next full-review request, or replays the pending one's mutation.
func requestFullReview(s *store.Store, repo, id string) (store.Receipt, error) {
	all, err := s.FullReviews(repo, id)
	if err != nil {
		return store.Receipt{}, err
	}
	n := len(all) + 1
	if len(all) > 0 && all[len(all)-1].ConsumedBy == "" {
		n = len(all)
	}
	return s.RequestFullReview(mutationID("full-review", repo, id, strconv.Itoa(n)), repo, id)
}

// setPriority records the task's next priority change with the mutation "priority:<repo>:<task>:<n>", n the change's
// number; a retry of a level already in force prints the receipt that set it.
func setPriority(s *store.Store, repo, id string, level store.Priority) (store.Receipt, error) {
	tp, err := s.TaskPriority(repo, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Receipt{}, err
	}
	if n := len(tp.History); n > 0 && tp.Priority == level {
		return s.Receipt(mutationID("priority", repo, id, strconv.Itoa(n)))
	}
	return s.SetTaskPriority(mutationID("priority", repo, id, strconv.Itoa(len(tp.History)+1)), repo, id, level)
}

// acceptedWorkflow is a repository's effective workflow and, when it validates on its own, the file it came from.
type acceptedWorkflow struct {
	effective, file *workflow.Definition
}

// acceptTask loads the Director config at cfgPath and returns a task of kind taskKind for repository repo bound to
// the repository's effective workflow revision for that kind and the configuration digest, with the workflow
// admitWorkflow checks.
func acceptTask(cfgPath, repo, taskKind string) (store.Task, acceptedWorkflow, error) {
	cfg, r, err := loadRepository(cfgPath, repo)
	if err != nil {
		return store.Task{}, acceptedWorkflow{}, err
	}
	w, err := repositoryWorkflow(filepath.Join(filepath.Dir(cfgPath), "workflows"), cfg, r, taskKind)
	if err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	if err := cfg.CheckArtisanAdapters(w.effective); err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	var named []string
	for _, step := range w.effective.Steps {
		if name := workflow.EffectiveProfile(step); name != "" && !slices.Contains(named, name) {
			named = append(named, name)
		}
	}
	if err := checkProfileSettings(cfg, named); err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("config digest: %w", err)
	}
	return store.Task{Repository: repo, Workflow: w.effective.Revision(), ConfigDigest: string(digest.Of(data))}, w, nil
}

// loadRepository loads the Director config at cfgPath and finds repository repo in it.
func loadRepository(cfgPath, repo string) (*config.Config, config.Repository, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, config.Repository{}, fmt.Errorf("config: %w", err)
	}
	r, err := cfg.ListedRepository(repo)
	if err != nil {
		return nil, config.Repository{}, fmt.Errorf("config %s: %w", cfgPath, err)
	}
	return cfg, r, nil
}

// admitWorkflow refuses a workflow that is neither shipped, nor approved in s, nor a non-weakening change of one
// that meets the review floor (docs/configuration.md#workflow-edit-authority); the file before the repository's
// overrides is the candidate baseline besides the shipped definitions.
func admitWorkflow(s *store.Store, w acceptedWorkflow) error {
	shipped, err := shippedDefinitions()
	if err != nil {
		return err
	}
	approvals, err := s.WorkflowApprovals()
	if err != nil {
		return err
	}
	approved := map[string]bool{}
	for _, a := range approvals {
		approved[a.Digest] = true
	}
	var candidates []*workflow.Definition
	if w.file != nil {
		candidates = append(candidates, w.file)
	}
	auth := workflow.Authority{Shipped: shipped, Approved: func(d string) bool { return approved[d] }}
	_, err = auth.Admit(w.effective, candidates...)
	return err
}

// effectiveWorkflow returns the workflow a task of kind taskKind created now in repository r would bind, with its
// overrides and profile defaults applied and validated.
func effectiveWorkflow(dir string, cfg *config.Config, r config.Repository, taskKind string) (*workflow.Definition, error) {
	w, err := repositoryWorkflow(dir, cfg, r, taskKind)
	return w.effective, err
}

// repositoryWorkflow returns the workflow a task of kind taskKind created now in repository r binds: the repository's
// workflow setting for a feature, its kinds table's or the shipped workflow for a bug, resolved by resolveWorkflow.
func repositoryWorkflow(dir string, cfg *config.Config, r config.Repository, taskKind string) (acceptedWorkflow, error) {
	name, ok := r.TaskKindWorkflow(taskKind)
	if !ok {
		return acceptedWorkflow{}, fmt.Errorf("unknown task kind %q; use %s", taskKind, taskKindChoice)
	}
	return resolveWorkflow(dir, name, cfg, r)
}

// boundWorkflow returns the definition of rev, the revision a task in repository r bound: rev's workflow resolved by
// name, with the repository's current overrides, refused unless its digest is rev's.
// A task therefore keeps its workflow when the repository's workflow setting changes, and strands only when the
// file or the overrides change after it was created.
// A task bound while role profiles were written into the definition bound the legacy digest of that definition with
// every role step's profile defaulted (workflow.ApplyProfileDefaults); that digest is accepted too, and the task runs
// the defaulted definition (docs/decisions/role-profiles-outside-the-digest.md). A task could bind it only with
// every role step defaulted, since a role step left without a profile was refused, so the legacy digest is computed
// with every role profile declared.
// A shipped name also resolves to an earlier release's shipped revision of it (workflow.Retired), with the
// repository's current overrides and, again, the legacy defaulted variant, so a task survives an upgrade that changes
// the shipped bytes (docs/decisions/retired-shipped-revisions.md).
func boundWorkflow(dir string, cfg *config.Config, r config.Repository, rev workflow.Revision) (*workflow.Definition, error) {
	w, err := resolveWorkflow(dir, rev.Name, cfg, r)
	if err != nil {
		return nil, fmt.Errorf("bound %w", err)
	}
	got := w.effective.Digest()
	if def := matchBound(w.effective, rev); def != nil {
		return def, nil
	}
	shipped, err := shippedDefinitions()
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(shipped, func(d *workflow.Definition) bool { return d.Name == rev.Name }) {
		return nil, fmt.Errorf("%w: bound workflow %q is %s but now resolves to %s; its file or the repository's workflow_overrides changed after the task was created",
			interp.ErrRevision, rev.Name, rev.Digest, got)
	}
	retired, err := workflow.Retired(rev.Name)
	if err != nil {
		return nil, err
	}
	for _, old := range retired {
		effective, err := workflow.ApplyOverrides(old, overridesFor(old, r))
		if err != nil || workflow.Validate(effective) != nil {
			continue
		}
		if def := matchBound(effective, rev); def != nil {
			return def, nil
		}
	}
	return nil, fmt.Errorf("%w: bound workflow %q is %s, which is not a revision shipped with this or an earlier release (with the repository's current workflow_overrides); it now resolves to %s",
		interp.ErrRevision, rev.Name, rev.Digest, got)
}

// matchBound returns effective when its digest is rev's, or its legacy variant with every role profile defaulted when
// that validates and its digest is rev's, and otherwise nil.
func matchBound(effective *workflow.Definition, rev workflow.Revision) *workflow.Definition {
	if effective.Digest() == rev.Digest {
		return effective
	}
	legacy := workflow.ApplyProfileDefaults(effective, func(string) bool { return true })
	if legacy.Digest() == rev.Digest && workflow.Validate(legacy) == nil {
		return legacy
	}
	return nil
}

// resolveWorkflow returns workflow name with repository r's overrides applied and validated, and the definition
// before them when that validates too: the shipped definition of that name, or otherwise <dir>/<name>.json, whose
// name must match. Role profiles are not applied: a step's profile is resolved at use (workflow.EffectiveProfile),
// outside the digest the task binds.
func resolveWorkflow(dir, name string, cfg *config.Config, r config.Repository) (acceptedWorkflow, error) {
	file := filepath.Join(dir, name+".json")
	var def *workflow.Definition
	shipped, err := shippedDefinitions()
	if i := slices.IndexFunc(shipped, func(d *workflow.Definition) bool { return d.Name == name }); err == nil && i >= 0 {
		def = shipped[i]
	} else if err == nil {
		var data []byte
		if data, err = os.ReadFile(file); err == nil {
			def, err = workflow.Parse(data)
		}
		if err == nil && def.Name != name {
			err = fmt.Errorf("%s is named %q, not %q", file, def.Name, name)
		}
	}
	if err != nil {
		return acceptedWorkflow{}, fmt.Errorf("workflow %q: %w", name, err)
	}
	effective, err := workflow.ApplyOverrides(def, overridesFor(def, r))
	if err != nil {
		return acceptedWorkflow{}, fmt.Errorf("workflow %q: %w", name, err)
	}
	if err := workflow.Validate(effective); err != nil {
		return acceptedWorkflow{}, fmt.Errorf("workflow %q: %w", name, err)
	}
	w := acceptedWorkflow{effective: effective}
	if workflow.Validate(def) == nil {
		w.file = def
	}
	return w, nil
}

// overridesFor returns the workflow_overrides of repository r that def takes: every one for the repository's feature
// workflow, so an override naming a step it lacks is still refused, and only those naming a step def has for any other
// workflow, such as the hallmark workflow, which has none of the steps an override can name (workflow.OwnOverrides).
func overridesFor(def *workflow.Definition, r config.Repository) map[string]workflow.Override {
	if def.Name == r.Workflow {
		return r.WorkflowOverrides
	}
	return workflow.OwnOverrides(def, r.WorkflowOverrides)
}

// showTask reads the task and every record it has; with cfg it also judges the task's overrides in effect against it,
// for SkippedOverrides and AlsoRequires.
func showTask(s *store.Store, cfg *config.Config, repo, id string) (taskDetail, error) {
	d := taskDetail{Attempts: []store.StageAttempt{}, Documents: []store.DocumentRef{}, Decisions: []store.Decision{}, Context: []store.TaskContext{}, Links: []store.TaskLink{}, Overrides: []store.TaskOverride{}}
	var err error
	t, err := s.Task(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	views, err := taskViews(s, []store.Task{t})
	if err != nil {
		return taskDetail{}, err
	}
	d.Task = views[0]
	attempts, err := s.Attempts(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	documents, err := s.Documents(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	decisions, err := s.Decisions(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	contexts, err := s.TaskContexts(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	links, err := s.TaskLinks(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	overrides, err := s.TaskOverrides(repo, id)
	if err != nil {
		return taskDetail{}, err
	}
	d.Overrides = append(d.Overrides, store.ActiveTaskOverrides(overrides)...)
	if cfg != nil {
		_, skipped, added, err := interp.EffectiveOverrides(cfg, repo, d.Overrides)
		if err != nil {
			return taskDetail{}, fmt.Errorf("config: %w", err)
		}
		d.SkippedOverrides = append([]config.Skipped{}, skipped...)
		d.AlsoRequires = append([]string{}, added...)
	}
	d.Attempts = append(d.Attempts, attempts...)
	d.Documents = append(d.Documents, documents...)
	d.Decisions = append(d.Decisions, decisions...)
	d.Context = append(d.Context, contexts...)
	d.Links = append(d.Links, links...)
	d.DesignRounds = interp.DesignRounds(documents)
	return d, nil
}

// taskView is a task as task list and task show print it: the immutable record, its priority and, while it waits
// in its repository's merge queue, its entry there.
type taskView struct {
	store.Task
	Priority   store.Priority   `json:"priority"`
	MergeQueue *mergeQueueEntry `json:"merge_queue,omitempty"`
}

// taskViews reads each task's priority, DefaultPriority when none was set, and merge-queue entry.
func taskViews(s *store.Store, tasks []store.Task) ([]taskView, error) {
	priorities := map[string]map[string]store.Priority{}
	var repos []string
	for _, t := range tasks {
		if !slices.Contains(repos, t.Repository) {
			repos = append(repos, t.Repository)
		}
	}
	queued, err := queueEntries(s, repos)
	if err != nil {
		return nil, err
	}
	out := []taskView{}
	for _, t := range tasks {
		ps, ok := priorities[t.Repository]
		if !ok {
			var err error
			if ps, err = s.TaskPriorities(t.Repository); err != nil {
				return nil, err
			}
			priorities[t.Repository] = ps
		}
		p, ok := ps[t.ID]
		if !ok {
			p = store.DefaultPriority
		}
		out = append(out, taskView{Task: t, Priority: p, MergeQueue: queued[t.Repository+"/"+t.ID]})
	}
	return out, nil
}

// listTasks lists one repository's tasks, or every repository's in repository order when repo is empty.
func listTasks(s *store.Store, repo string) ([]store.Task, error) {
	repos := []string{repo}
	if repo == "" {
		var err error
		if repos, err = s.Repositories(); err != nil {
			return nil, err
		}
	}
	out := []store.Task{}
	for _, r := range repos {
		tasks, err := s.Tasks(r)
		if err != nil {
			return nil, err
		}
		out = append(out, tasks...)
	}
	return out, nil
}

func (a *App) taskUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskUsage)
	return 2
}
