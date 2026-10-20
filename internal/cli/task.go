package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskUsage = `usage:
  guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--depends-on <repo>/<task>]... <task>
  guildhall task show --store <path> --repo <id> <task> --json
  guildhall task list --store <path> [--repo <id>] --json`

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

// taskDetail is what task show prints: one task and every record it has.
type taskDetail struct {
	Task      store.Task           `json:"task"`
	Attempts  []store.StageAttempt `json:"attempts"`
	Documents []store.DocumentRef  `json:"documents"`
	Decisions []store.Decision     `json:"decisions"`
}

// task runs a task subcommand against an existing store and prints its result as one JSON document (docs/tasks.md).
// task new binds the repository's effective workflow revision and the configuration digest, and records the
// mutation "task:<repo>:<task>" (or "task:sha256:<hex digest>" of that string if over 128 bytes),
// so a retry prints the original receipt; show and list only read.
// It exits 0 on success, 1 when the configuration, workflow or store refuses or fails and 2 on a usage error.
func (a *App) task(args []string) int {
	if len(args) == 0 {
		return a.taskUsage("missing task subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	title := flags.String("title", "", "task title")
	asJSON := flags.Bool("json", false, "print JSON")
	var deps dependsOn
	flags.Var(&deps, "depends-on", "task this one depends on")
	var want int
	var allowed map[string]bool
	switch sub {
	case "new":
		want, allowed = 1, map[string]bool{"store": true, "config": true, "repo": true, "title": true, "depends-on": true}
	case "show":
		want, allowed = 1, map[string]bool{"store": true, "repo": true, "json": true}
	case "list":
		want, allowed = 0, map[string]bool{"store": true, "repo": true, "json": true}
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
		if f := flags.Lookup(name); f != nil && !inline && name != "json" && len(remaining) > 1 {
			n = 2
		}
		if err := flags.Parse(remaining[:n]); err != nil {
			return a.taskUsage(err.Error())
		}
		remaining = remaining[n:]
	}
	var extra string
	flags.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			extra = f.Name
		}
	})
	switch {
	case extra != "":
		return a.taskUsage(fmt.Sprintf("--%s does not apply to task %s", extra, sub))
	case *path == "":
		return a.taskUsage("--store is required")
	case sub == "new" && (*cfgPath == "" || *repo == "" || *title == ""):
		return a.taskUsage("task new requires --config, --repo and --title")
	case sub == "show" && *repo == "":
		return a.taskUsage("task show requires --repo")
	case sub != "new" && !*asJSON:
		return a.taskUsage(fmt.Sprintf("task %s prints JSON only; pass --json", sub))
	case len(positional) != want:
		return a.taskUsage(fmt.Sprintf("task %s takes %d arguments", sub, want))
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task %s: %v\n", sub, err)
		return 1
	}
	var task store.Task
	if sub == "new" {
		// Resolve configuration and validate the effective workflow before opening the store.
		// Task fields and dependencies are validated by CreateTask in the store.
		var err error
		if task, err = acceptTask(*cfgPath, *repo); err != nil {
			return fail(err)
		}
		task.ID, task.Title, task.DependsOn = positional[0], *title, deps
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("no store at %s", *path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub != "new", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	switch sub {
	case "new":
		out, err = s.CreateTask(mutationID("task", task.Repository, task.ID), task)
	case "show":
		out, err = showTask(s, *repo, positional[0])
	case "list":
		out, err = listTasks(s, *repo)
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

// acceptTask loads the Director config at cfgPath and returns a task for repository repo bound to the
// repository's effective workflow revision and the configuration digest.
func acceptTask(cfgPath, repo string) (store.Task, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return store.Task{}, fmt.Errorf("config: %w", err)
	}
	r, ok := cfg.Repository(repo)
	if !ok {
		return store.Task{}, fmt.Errorf("config %s has no repository %q", cfgPath, repo)
	}
	def, err := effectiveWorkflow(filepath.Join(filepath.Dir(cfgPath), "workflows"), r)
	if err != nil {
		return store.Task{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return store.Task{}, fmt.Errorf("config digest: %w", err)
	}
	sum := sha256.Sum256(data)
	return store.Task{Repository: repo, Workflow: def.Revision(), ConfigDigest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// effectiveWorkflow returns the repository's workflow with its overrides applied and validated:
// the shipped definition for "default", otherwise <dir>/<name>.json, whose name must match.
func effectiveWorkflow(dir string, r config.Repository) (*workflow.Definition, error) {
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
		return nil, fmt.Errorf("workflow %q: %w", name, err)
	}
	if def, err = workflow.ApplyOverrides(def, r.WorkflowOverrides); err != nil {
		return nil, fmt.Errorf("workflow %q: %w", name, err)
	}
	if err := workflow.Validate(def); err != nil {
		return nil, fmt.Errorf("workflow %q: %w", name, err)
	}
	return def, nil
}

func showTask(s *store.Store, repo, id string) (taskDetail, error) {
	d := taskDetail{Attempts: []store.StageAttempt{}, Documents: []store.DocumentRef{}, Decisions: []store.Decision{}}
	var err error
	if d.Task, err = s.Task(repo, id); err != nil {
		return taskDetail{}, err
	}
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
