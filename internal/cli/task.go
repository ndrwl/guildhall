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

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskUsage = `usage:
  guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--depends-on <repo>/<task>]... [--fixes <repo>/<task>] [--priority <level>] <task>
  guildhall task priority --store <path> --repo <id> <task> <level>
  guildhall task hold-merge --store <path> --repo <id> <task>
  guildhall task show --store <path> --repo <id> <task> --json
  guildhall task list --store <path> [--repo <id>] --json
  guildhall task timeline --store <path> --repo <id> <task> --json
  guildhall task files --store <path> --repo <id> <task> --json
  guildhall task finding rule --store <path> --repo <id> --outcome <document> --finding <id> --move out-of-scope|duplicate|defer --reason <text> <task>
  guildhall task review --full --store <path> --repo <id> <task>
  guildhall task start --store <path> --config <director.toml> --state <dir> --repo <id> [--decision <id>] [--ignore-deps --reason <text>] <task>
--copy <dir>, or GUILDHALL_COPY, may stand in for --store, --config and --state: the copy's director.store,
config.toml and state`

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
}

// task runs a task subcommand against an existing store and prints its result as one JSON document (docs/tasks.md).
// task new binds the repository's effective workflow revision and the configuration digest, and records the
// mutation "task:<repo>:<task>" (or "task:sha256:<hex digest>" of that string if over 128 bytes),
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
	asJSON := flags.Bool("json", false, "print JSON")
	outcome := flags.String("outcome", "", "review outcome document")
	finding := flags.String("finding", "", "finding id")
	move := flags.String("move", "", "ruling move")
	reason := flags.String("reason", "", "ruling reason")
	full := flags.Bool("full", false, "request a full review")
	priority := flags.String("priority", "", "task priority, p0 to p5")
	var deps dependsOn
	flags.Var(&deps, "depends-on", "task this one depends on")
	var fixed fixes
	flags.Var(&fixed, "fixes", "earlier task whose defect this one fixes")
	var cp copyFlags
	cp.register(flags)
	var want int
	var allowed map[string]bool
	switch sub {
	case "new":
		want, allowed = 1, map[string]bool{"store": true, "config": true, "repo": true, "title": true, "depends-on": true, "fixes": true, "priority": true}
	case "priority":
		want, allowed = 2, map[string]bool{"store": true, "repo": true}
	case "hold-merge":
		want, allowed = 1, map[string]bool{"store": true, "repo": true}
	case "show", "timeline", "files":
		want, allowed = 1, map[string]bool{"store": true, "repo": true, "json": true}
	case "list":
		want, allowed = 0, map[string]bool{"store": true, "repo": true, "json": true}
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
		if f := flags.Lookup(name); f != nil && !inline && name != "json" && name != "full" && len(remaining) > 1 {
			n = 2
		}
		if err := flags.Parse(remaining[:n]); err != nil {
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
	if sub == "new" {
		copyConfig = cfgPath
	}
	cerr := cp.apply(a.Getenv, path, copyConfig, nil)
	switch {
	case extra != "":
		return a.taskUsage(fmt.Sprintf("--%s does not apply to task %s", extra, sub))
	case cerr != nil:
		return a.taskUsage(cerr.Error())
	case *path == "":
		return a.taskUsage("--store is required, or --copy")
	case sub == "new" && (*cfgPath == "" || *repo == "" || *title == ""):
		return a.taskUsage("task new requires --config, --repo and --title")
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
		// Resolve configuration and validate the effective workflow before opening the store.
		// Task fields and dependencies are validated by CreateTask in the store.
		var err error
		if task, accepted, err = acceptTask(*cfgPath, *repo); err != nil {
			return fail(err)
		}
		task.ID, task.Title, task.DependsOn, task.Fixes = positional[0], *title, deps, fixed.ref
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
	case "priority":
		out, err = setPriority(s, *repo, positional[0], level)
	case "hold-merge":
		out, err = toggleMergeHold(s, *repo, positional[0])
	case "show":
		out, err = showTask(s, *repo, positional[0])
	case "list":
		var tasks []store.Task
		if tasks, err = listTasks(s, *repo); err == nil {
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

// acceptTask loads the Director config at cfgPath and returns a task for repository repo bound to the
// repository's effective workflow revision and the configuration digest, with the workflow admitWorkflow checks.
func acceptTask(cfgPath, repo string) (store.Task, acceptedWorkflow, error) {
	cfg, r, err := loadRepository(cfgPath, repo)
	if err != nil {
		return store.Task{}, acceptedWorkflow{}, err
	}
	w, err := repositoryWorkflow(filepath.Join(filepath.Dir(cfgPath), "workflows"), r)
	if err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	if err := cfg.CheckArtisanAdapters(w.effective); err != nil {
		return store.Task{}, acceptedWorkflow{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	var named []string
	for _, step := range w.effective.Steps {
		if name := step.Profile(); name != "" && !slices.Contains(named, name) {
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

// effectiveWorkflow returns the repository's workflow with its overrides applied and validated.
func effectiveWorkflow(dir string, r config.Repository) (*workflow.Definition, error) {
	w, err := repositoryWorkflow(dir, r)
	return w.effective, err
}

// repositoryWorkflow returns the repository's workflow with its overrides applied and validated, and the
// definition before its overrides when that validates too: the shipped definition for "default",
// otherwise <dir>/<name>.json, whose name must match.
func repositoryWorkflow(dir string, r config.Repository) (acceptedWorkflow, error) {
	name := r.Workflow
	file := filepath.Join(dir, name+".json")
	var def *workflow.Definition
	var err error
	if name == config.DefaultWorkflow {
		def, err = workflow.Default()
	} else {
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
	effective, err := workflow.ApplyOverrides(def, r.WorkflowOverrides)
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

func showTask(s *store.Store, repo, id string) (taskDetail, error) {
	d := taskDetail{Attempts: []store.StageAttempt{}, Documents: []store.DocumentRef{}, Decisions: []store.Decision{}}
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
	d.Attempts = append(d.Attempts, attempts...)
	d.Documents = append(d.Documents, documents...)
	d.Decisions = append(d.Decisions, decisions...)
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
