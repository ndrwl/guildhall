package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskDependsUsage = `usage:
  guildhall task depends add --store <path> [--config <director.toml>] --repo <id> --reason <text> <task> <repo>/<dependency>
  guildhall task depends remove --store <path> [--config <director.toml>] --repo <id> --reason <text> <task> <repo>/<dependency>
--copy <dir>, or the copy found from the current directory, may stand in for --store and --config: the copy's
director.store and config.toml
--config, when given, resolves a workflow the copy defines, to tell whether the task is past the dependency gate`

// Notes a task depends add receipt carries when the dependency it added holds nothing.
const (
	dependsNotePastGate = "past the dependency gate; this change holds nothing until the task is recreated"
	dependsNoteOverride = "dependency override in force; this task is not held"
)

// dependsReceipt is task depends' output: the receipt, and a note when an add holds nothing.
type dependsReceipt struct {
	store.Receipt
	Note string `json:"note,omitempty"`
}

// taskDepends runs a task depends subcommand and prints its receipt as one JSON document
// (docs/tasks.md#changing-dependencies). add makes <repo>/<dependency> one of an open task's effective dependencies and
// remove drops it, a dependency the task was created with included; each records --reason with the mutation
// "task-dependency:<repo>:<task>:<op>:<dep repo>:<dep task>:<prior>", hashed when over 128 bytes, prior the number of
// records the task already has for that dependency. Running the same command again, while its record is still the
// dependency's latest, prints the original receipt and writes nothing. An add to a task past the dependency gate, or
// with a dependency override, carries a note saying it holds nothing. It exits 0 on success, 1 when the store refuses
// or fails and 2 on a usage error.
func (a *App) taskDepends(args []string) int {
	if len(args) == 0 {
		return a.taskDependsUsage("missing task depends subcommand")
	}
	sub := args[0]
	var op store.TaskDependencyOp
	switch sub {
	case "add":
		op = store.TaskDependencyAdd
	case "remove":
		op = store.TaskDependencyRemove
	default:
		return a.taskDependsUsage(fmt.Sprintf("unknown task depends subcommand %q", sub))
	}
	flags := flag.NewFlagSet("task depends "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	reason := flags.String("reason", "", "why the dependency changes")
	var cp copyFlags
	cp.register(flags)
	// Flags and arguments may come in any order; everything after -- is an argument.
	var positional []string
	for remaining := args[1:]; len(remaining) > 0; {
		if err := flags.Parse(remaining); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskDependsUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskDependsUsage(err.Error())
		}
		rest := flags.Args()
		if used := len(remaining) - len(rest); used > 0 && remaining[used-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		if remaining = rest; len(remaining) > 0 {
			positional, remaining = append(positional, remaining[0]), remaining[1:]
		}
	}
	var dep store.TaskRef
	depOK := false
	if len(positional) == 2 {
		r, id, ok := strings.Cut(positional[1], "/")
		dep, depOK = store.TaskRef{Repository: r, ID: id}, ok && workflow.ValidID(r) && workflow.ValidID(id)
	}
	cerr := cp.apply(a, path, cfgPath, nil)
	var usageErr string
	switch {
	case cerr != nil:
		usageErr = cerr.Error()
	case cp.searched:
		usageErr = cp.missing(a).Error()
	case *path == "":
		usageErr = "--store is required, or --copy"
	case *repo == "":
		usageErr = fmt.Sprintf("task depends %s requires --repo", sub)
	case len(positional) != 2:
		usageErr = fmt.Sprintf("task depends %s takes 2 arguments, the task and <repo>/<dependency>", sub)
	case !workflow.ValidID(*repo) || !workflow.ValidID(positional[0]):
		usageErr = "ids must be lowercase letters, digits and hyphens, starting with a letter"
	case !depOK:
		usageErr = fmt.Sprintf("dependency %q must be <repository>/<task>", positional[1])
	case strings.TrimSpace(*reason) == "":
		usageErr = fmt.Sprintf("task depends %s needs a nonempty --reason", sub)
	case len(*reason) > store.MaxTextBytes:
		usageErr = fmt.Sprintf("task depends %s: --reason is %d bytes; the limit is %d", sub, len(*reason), store.MaxTextBytes)
	}
	if usageErr != "" {
		return a.taskDependsUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task depends %s: %v\n", sub, err)
		return 1
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{RequireStore: true})
	if err != nil {
		return fail(err)
	}
	out, err := addTaskDependency(s, statusWorkflows(*cfgPath), *repo, positional[0], op, dep, *reason)
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

// addTaskDependency records op on dep for the task. prior is the number of records the task has for dep, unless the
// latest of them is this very command's, op and reason alike, when it is one fewer, so the store replays the original
// receipt. An add is noted when it holds nothing: the task has a dependency override, or is past the dependency gate
// of its bound workflow, which workflows resolves.
func addTaskDependency(s *store.Store, workflows func(repo string) func(workflow.Revision) (*workflow.Definition, error), repo, task string, op store.TaskDependencyOp, dep store.TaskRef, reason string) (dependsReceipt, error) {
	all, err := s.TaskDependencies(repo, task)
	if err != nil {
		return dependsReceipt{}, err
	}
	mine := slices.DeleteFunc(all, func(d store.TaskDependency) bool { return d.Dependency != dep })
	prior := len(mine)
	if prior > 0 && mine[prior-1].Op == op && mine[prior-1].Text == reason {
		prior--
	}
	m := mutationID("task-dependency", repo, task, string(op), dep.Repository, dep.ID, strconv.Itoa(prior))
	rc, err := s.AddTaskDependency(m, repo, task, op, dep, prior, reason)
	if err != nil || op != store.TaskDependencyAdd {
		return dependsReceipt{Receipt: rc}, err
	}
	out := dependsReceipt{Receipt: rc}
	switch _, err := s.DependencyOverride(repo, task); {
	case err == nil:
		out.Note = dependsNoteOverride
		return out, nil
	case !errors.Is(err, store.ErrNotFound):
		return out, err
	}
	t, err := s.Task(repo, task)
	if err != nil {
		return out, err
	}
	attempts, err := s.Attempts(repo, task)
	if err != nil {
		return out, err
	}
	if len(attempts) == 0 {
		return out, nil
	}
	def, err := workflows(repo)(t.Workflow)
	if err != nil {
		out.Note = fmt.Sprintf("workflow %s does not resolve (%v); if the task is %s", t.Workflow.Name, err, dependsNotePastGate)
		return out, nil
	}
	if interp.PastDependencyGate(def, attempts) {
		out.Note = dependsNotePastGate
	}
	return out, nil
}

func (a *App) taskDependsUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskDependsUsage)
	return 2
}
