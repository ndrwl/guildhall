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

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskLinkUsage = `usage:
  guildhall task link add --store <path> --state <dir> --repo <id> --document <repo>/<task>/<name> (--text <text> | --file <path>) <task>
  guildhall task link remove --store <path> --repo <id> --document <repo>/<task>/<name> (--text <text> | --file <path>) <task>
  guildhall task link list --store <path> --repo <id> <task> --json
--copy <dir>, or the copy found from the current directory, may stand in for --store and --state: the copy's
director.store and state
--document names one recorded document exactly, by its repository, task and record name, such as guildhall/d14/plan-5-spec`

// taskLink runs a task link subcommand and prints its result as one JSON document (docs/tasks.md#task-links).
// add links another task's recorded document to an open task, after checking its file under --state's documents
// against the recorded digest, and remove unlinks it; each quotes the Guildmaster from --text or the file --file
// names, with the mutation "task-link:<repo>:<task>:<op>:<src repo>:<src task>:<name>:<prior>", prior the number of
// records the task already has for that source, and prints its receipt. Running the same command again, while its
// record is still the source's latest, prints the original receipt and writes nothing. list prints the task's link
// records, read-only. It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) taskLink(args []string) int {
	if len(args) == 0 {
		return a.taskLinkUsage("missing task link subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("task link "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	state := flags.String("state", "", "state directory, holding the documents")
	repo := flags.String("repo", "", "repository id")
	document := flags.String("document", "", "the linked document, <repo>/<task>/<name>")
	text := flags.String("text", "", "the Guildmaster's words")
	file := flags.String("file", "", "file holding the Guildmaster's words")
	asJSON := flags.Bool("json", false, "print JSON")
	var cp copyFlags
	cp.register(flags)
	var allowed []string
	switch sub {
	case "add":
		allowed = []string{"store", "state", "copy", "repo", "document", "text", "file"}
	case "remove":
		allowed = []string{"store", "copy", "repo", "document", "text", "file"}
	case "list":
		allowed = []string{"store", "copy", "repo", "json"}
	default:
		return a.taskLinkUsage(fmt.Sprintf("unknown task link subcommand %q", sub))
	}
	// Flags and the task may come in any order; everything after -- is an argument.
	var positional []string
	for remaining := args[1:]; len(remaining) > 0; {
		if err := flags.Parse(remaining); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskLinkUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskLinkUsage(err.Error())
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
			usageErr = fmt.Sprintf("--%s does not apply to task link %s", f.Name, sub)
		}
	})
	var statePtr *string
	if sub == "add" {
		statePtr = state
	}
	writes := sub != "list"
	source, sourceOK := parseLinkSource(*document)
	cerr := cp.apply(a, path, nil, statePtr)
	switch {
	case usageErr != "":
	case cerr != nil:
		usageErr = cerr.Error()
	case cp.searched:
		usageErr = cp.missing(a).Error()
	case *path == "":
		usageErr = "--store is required, or --copy"
	case sub == "add" && *state == "":
		usageErr = "task link add requires --state, or --copy"
	case *repo == "":
		usageErr = fmt.Sprintf("task link %s requires --repo", sub)
	case len(positional) != 1:
		usageErr = fmt.Sprintf("task link %s takes 1 argument, the task", sub)
	case !workflow.ValidID(*repo) || !workflow.ValidID(positional[0]):
		usageErr = "ids must be lowercase letters, digits and hyphens, starting with a letter"
	case writes && *document == "":
		usageErr = fmt.Sprintf("task link %s requires --document <repo>/<task>/<name>", sub)
	case writes && !sourceOK:
		usageErr = fmt.Sprintf("--document %q must be <repo>/<task>/<name>, three ids, naming one recorded document exactly", *document)
	case writes && given["text"] == given["file"]:
		usageErr = fmt.Sprintf("task link %s takes exactly one of --text and --file", sub)
	case writes && *file == "-":
		usageErr = fmt.Sprintf("task link %s --file - is not supported; name a file", sub)
	case sub == "list" && !*asJSON:
		usageErr = "task link list prints JSON only; pass --json"
	}
	if usageErr != "" {
		return a.taskLinkUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task link %s: %v\n", sub, err)
		return 1
	}
	task := positional[0]
	if writes && given["file"] {
		data, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		*text = string(data)
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: !writes, RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	switch sub {
	case "add":
		out, err = addTaskLink(s, filepath.Join(*state, "documents"), *repo, task, store.TaskLinkAdd, source, *text)
	case "remove":
		out, err = addTaskLink(s, "", *repo, task, store.TaskLinkRemove, source, *text)
	case "list":
		out, err = taskLinks(s, *repo, task)
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

// parseLinkSource reads --document's <repo>/<task>/<name>.
func parseLinkSource(v string) (store.DocumentRef, bool) {
	parts := strings.Split(v, "/")
	if len(parts) != 3 || !workflow.ValidID(parts[0]) || !workflow.ValidID(parts[1]) || !workflow.ValidID(parts[2]) {
		return store.DocumentRef{}, false
	}
	return store.DocumentRef{Repository: parts[0], TaskID: parts[1], Name: parts[2]}, true
}

// addTaskLink records op on source for the task. prior is the number of records the task has for source, unless the
// latest of them is this very command's, op and words alike, when it is one fewer, so the store replays the original
// receipt. An add first checks the source's file under dir against its recorded digest.
func addTaskLink(s *store.Store, dir, repo, task string, op store.TaskLinkOp, source store.DocumentRef, text string) (store.Receipt, error) {
	src := store.TaskLinkSource(source)
	// The store refuses a task's own document, naming the reason, before any file could matter.
	if op == store.TaskLinkAdd && (source.Repository != repo || source.TaskID != task) {
		if err := checkLinkSource(s, dir, source); err != nil {
			return store.Receipt{}, err
		}
	}
	all, err := s.TaskLinks(repo, task)
	if err != nil {
		return store.Receipt{}, err
	}
	mine := slices.DeleteFunc(all, func(l store.TaskLink) bool { return store.TaskLinkSource(l.Source) != src })
	prior := len(mine)
	if prior > 0 && mine[prior-1].Op == op && mine[prior-1].Text == text {
		prior--
	}
	m := mutationID("task-link", repo, task, string(op), source.Repository, source.TaskID, source.Name, strconv.Itoa(prior))
	return s.AddTaskLink(m, repo, task, op, source, prior, text)
}

// checkLinkSource refuses a source document that is not recorded, or whose file under dir is missing or no longer
// holds its recorded bytes, naming it.
func checkLinkSource(s *store.Store, dir string, source store.DocumentRef) error {
	src := store.TaskLinkSource(source)
	docs, err := s.Documents(source.Repository, source.TaskID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(docs, func(d store.DocumentRef) bool { return d.Name == source.Name })
	if i < 0 {
		return fmt.Errorf("source document %s: %w", src, store.ErrNotFound)
	}
	d := docs[i]
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.Path)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("source document %s: file %s is missing", src, d.Path)
	case err != nil:
		return fmt.Errorf("source document %s: %w", src, err)
	case digest.Of(data).Hex() != d.SHA256:
		return fmt.Errorf("source document %s: file %s holds sha256:%s, not the recorded sha256:%s", src, d.Path, digest.Of(data).Hex(), d.SHA256)
	}
	return nil
}

// taskLinks lists an existing task's link records, empty rather than nil when it has none.
func taskLinks(s *store.Store, repo, id string) ([]store.TaskLink, error) {
	if _, err := s.Task(repo, id); err != nil {
		return nil, err
	}
	all, err := s.TaskLinks(repo, id)
	return append([]store.TaskLink{}, all...), err
}

func (a *App) taskLinkUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskLinkUsage)
	return 2
}
