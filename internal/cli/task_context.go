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

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskContextUsage = `usage:
  guildhall task context add --store <path> --repo <id> (--text <text> | --file <path>) <task>
  guildhall task context list --store <path> --repo <id> <task> --json
--copy <dir>, or GUILDHALL_COPY, may stand in for --store: the copy's director.store`

// taskContext runs a task context subcommand and prints its result as one JSON document (docs/tasks.md#task-context).
// add records the next entry of Guildmaster context on an open task, its text from --text or from the file --file
// names, with the mutation "task-context:<repo>:<task>:<sha256 of the text>", and prints its receipt, so running the
// same command again prints the original receipt and writes nothing. list prints the task's entries, read-only.
// It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) taskContext(args []string) int {
	if len(args) == 0 {
		return a.taskContextUsage("missing task context subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("task context "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	repo := flags.String("repo", "", "repository id")
	text := flags.String("text", "", "context text")
	file := flags.String("file", "", "file holding the context text")
	asJSON := flags.Bool("json", false, "print JSON")
	var cp copyFlags
	cp.register(flags)
	var allowed []string
	switch sub {
	case "add":
		allowed = []string{"store", "copy", "repo", "text", "file"}
	case "list":
		allowed = []string{"store", "copy", "repo", "json"}
	default:
		return a.taskContextUsage(fmt.Sprintf("unknown task context subcommand %q", sub))
	}
	// Flags and the task may come in any order; everything after -- is an argument.
	var positional []string
	for remaining := args[1:]; len(remaining) > 0; {
		if err := flags.Parse(remaining); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskContextUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskContextUsage(err.Error())
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
	var usageErr string
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) {
		given[f.Name] = true
		if !slices.Contains(allowed, f.Name) {
			usageErr = fmt.Sprintf("--%s does not apply to task context %s", f.Name, sub)
		}
	})
	cerr := cp.apply(a.Getenv, path, nil, nil)
	switch {
	case usageErr != "":
	case cerr != nil:
		usageErr = cerr.Error()
	case *path == "":
		usageErr = "--store is required, or --copy"
	case *repo == "":
		usageErr = fmt.Sprintf("task context %s requires --repo", sub)
	case len(positional) != 1:
		usageErr = fmt.Sprintf("task context %s takes 1 argument, the task", sub)
	case !workflow.ValidID(*repo) || !workflow.ValidID(positional[0]):
		usageErr = "ids must be lowercase letters, digits and hyphens, starting with a letter"
	case sub == "add" && given["text"] == given["file"]:
		usageErr = "task context add takes exactly one of --text and --file"
	case sub == "add" && *file == "-":
		usageErr = "task context add --file - is not supported; name a file"
	case sub == "list" && !*asJSON:
		usageErr = "task context list prints JSON only; pass --json"
	}
	if usageErr != "" {
		return a.taskContextUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task context %s: %v\n", sub, err)
		return 1
	}
	task := positional[0]
	if sub == "add" && given["file"] {
		data, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		*text = string(data)
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
	case "add":
		out, err = s.AddTaskContext(mutationID("task-context", *repo, task, digest.Of([]byte(*text)).Hex()), *repo, task, *text)
	case "list":
		out, err = taskContexts(s, *repo, task)
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

// taskContexts lists an existing task's context entries, empty rather than nil when it has none.
func taskContexts(s *store.Store, repo, id string) ([]store.TaskContext, error) {
	if _, err := s.Task(repo, id); err != nil {
		return nil, err
	}
	all, err := s.TaskContexts(repo, id)
	return append([]store.TaskContext{}, all...), err
}

func (a *App) taskContextUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskContextUsage)
	return 2
}
