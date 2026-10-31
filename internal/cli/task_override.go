package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskOverrideUsage = `usage:
  guildhall task override set --store <path> --config <file> --repo <id> --key <key> --value <value> --text <text> <task>
  guildhall task override unset --store <path> --repo <id> --key <key> --text <text> <task>
  guildhall task override list --store <path> --repo <id> <task> --json
--copy <dir>, or the copy found from the current directory, may stand in for --store and --config: the copy's
director.store and config.toml
--key names a task-scoped setting (docs/configuration.md#setting-scopes); --text quotes the Guildmaster`

// taskOverride runs a task override subcommand and prints its result as one JSON document
// (docs/tasks.md#task-overrides). set gives an open task its own value for a task-scoped setting, after the copy's
// config agrees it may (config.CheckTaskOverride), and unset removes it; each quotes the Guildmaster from --text, with
// the mutation "task-override:<repo>:<task>:<op>:<key>:<prior>", prior the number of records the task already has for
// that key, and prints its receipt. Running the same command again, while its record is still the key's latest, prints
// the original receipt and writes nothing. unset reads no config, so an override that no longer applies can still be
// removed. list prints the task's override records, read-only. It exits 0 on success, 1 when the config or the store
// refuses or fails and 2 on a usage error.
func (a *App) taskOverride(args []string) int {
	if len(args) == 0 {
		return a.taskOverrideUsage("missing task override subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("task override "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	key := flags.String("key", "", "the setting's key")
	value := flags.String("value", "", "the task's value for the setting")
	text := flags.String("text", "", "the Guildmaster's words")
	asJSON := flags.Bool("json", false, "print JSON")
	var cp copyFlags
	cp.register(flags)
	var allowed []string
	switch sub {
	case "set":
		allowed = []string{"store", "config", "copy", "repo", "key", "value", "text"}
	case "unset":
		allowed = []string{"store", "copy", "repo", "key", "text"}
	case "list":
		allowed = []string{"store", "copy", "repo", "json"}
	default:
		return a.taskOverrideUsage(fmt.Sprintf("unknown task override subcommand %q", sub))
	}
	// Flags and the task may come in any order; everything after -- is an argument.
	var positional []string
	for remaining := args[1:]; len(remaining) > 0; {
		if err := flags.Parse(remaining); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskOverrideUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskOverrideUsage(err.Error())
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
	flags.Visit(func(f *flag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			usageErr = fmt.Sprintf("--%s does not apply to task override %s", f.Name, sub)
		}
	})
	var configPtr *string
	if sub == "set" {
		configPtr = cfgPath
	}
	writes := sub != "list"
	cerr := cp.apply(a, path, configPtr, nil)
	switch {
	case usageErr != "":
	case cerr != nil:
		usageErr = cerr.Error()
	case cp.searched:
		usageErr = cp.missing(a).Error()
	case *path == "":
		usageErr = "--store is required, or --copy"
	case sub == "set" && *cfgPath == "":
		usageErr = "task override set requires --config, or --copy"
	case *repo == "":
		usageErr = fmt.Sprintf("task override %s requires --repo", sub)
	case len(positional) != 1:
		usageErr = fmt.Sprintf("task override %s takes 1 argument, the task", sub)
	case !workflow.ValidID(*repo) || !workflow.ValidID(positional[0]):
		usageErr = "ids must be lowercase letters, digits and hyphens, starting with a letter"
	case writes && *key == "":
		usageErr = fmt.Sprintf("task override %s requires --key", sub)
	case sub == "set" && *value == "":
		usageErr = "task override set requires --value"
	case writes && *text == "":
		usageErr = fmt.Sprintf("task override %s requires --text, the Guildmaster's words", sub)
	case sub == "list" && !*asJSON:
		usageErr = "task override list prints JSON only; pass --json"
	}
	if usageErr != "" {
		return a.taskOverrideUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task override %s: %v\n", sub, err)
		return 1
	}
	task := positional[0]
	if sub == "set" {
		// The copy's config judges the override before the store opens, so a refusal writes nothing; its reason is
		// printed as the registry words it.
		cfg, err := admitDirector(*cfgPath)
		if err != nil {
			return fail(fmt.Errorf("config: %w", err))
		}
		if err := cfg.CheckTaskOverride(*key, *value); err != nil {
			return fail(err)
		}
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
	case "set":
		out, err = addTaskOverride(s, *repo, task, store.TaskOverrideSet, *key, *value, *text)
	case "unset":
		out, err = addTaskOverride(s, *repo, task, store.TaskOverrideUnset, *key, "", *text)
	case "list":
		out, err = taskOverrides(s, *repo, task)
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

// taskOverrideFlags are task new's repeatable --override key=value, as given; a key given twice is refused.
type taskOverrideFlags [][2]string

func (f *taskOverrideFlags) String() string { return fmt.Sprint([][2]string(*f)) }

func (f *taskOverrideFlags) Set(v string) error {
	key, value, ok := strings.Cut(v, "=")
	switch {
	case !ok || key == "" || value == "":
		return fmt.Errorf("--override %q must be <key>=<value>", v)
	case slices.ContainsFunc(*f, func(o [2]string) bool { return o[0] == key }):
		return fmt.Errorf("--override %s is given twice", key)
	}
	*f = append(*f, [2]string{key, value})
	return nil
}

// plainOverrideKey is a key a mutation id can carry as it is.
var plainOverrideKey = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// addTaskOverride records op on key for the task. prior is the number of records the task has for key, unless the
// latest of them is this very command's, op, value and words alike, when it is one fewer, so the store replays the
// original receipt.
func addTaskOverride(s *store.Store, repo, task string, op store.TaskOverrideOp, key, value, text string) (store.Receipt, error) {
	all, err := s.TaskOverrides(repo, task)
	if err != nil {
		return store.Receipt{}, err
	}
	mine := slices.DeleteFunc(all, func(o store.TaskOverride) bool { return o.Key != key })
	prior := len(mine)
	if last := prior - 1; last >= 0 && mine[last].Op == op && mine[last].Value == value && mine[last].Text == text {
		prior--
	}
	// A key the store would refuse still needs a valid mutation id to reach the refusal that names it.
	k := key
	if !plainOverrideKey.MatchString(k) {
		k = digest.Of([]byte(key)).Hex()
	}
	m := mutationID("task-override", repo, task, string(op), k, strconv.Itoa(prior))
	return s.AddTaskOverride(m, repo, task, op, key, value, prior, text)
}

// taskOverrides lists an existing task's override records, empty rather than nil when it has none.
func taskOverrides(s *store.Store, repo, id string) ([]store.TaskOverride, error) {
	if _, err := s.Task(repo, id); err != nil {
		return nil, err
	}
	all, err := s.TaskOverrides(repo, id)
	return append([]store.TaskOverride{}, all...), err
}

func (a *App) taskOverrideUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskOverrideUsage)
	return 2
}
