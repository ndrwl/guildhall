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

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskGrillUsage = `usage:
  guildhall task grill --store <path> --config <director.toml> <repo>/<task> live|unattended --reason <text> [--by director|guildmaster]
--copy <dir>, or the copy found from the current directory, may stand in for --store and --config: the copy's
director.store and config.toml
--reason is the one-line reason for the mode; --by guildmaster records that the Director acts on the Guildmaster's
words, and director, the default, on its own judgement
The mode changes until research, or whatever step follows the grill, has its first attempt, and never while a grill
attempt runs; a grill that already succeeded runs again in the new mode at the task's next advance`

// taskGrill runs task grill: it records the task's next grill-mode record (docs/tasks.md#grill-mode), switching its
// grill between live and unattended, with the mutation "grill-mode:<repo>:<task>:<prior>", prior the number of
// records the task already has, and prints its receipt; running the same command again while its record is the
// latest prints the original receipt. It refuses, writing nothing, a task whose bound workflow has no Director grill,
// an unattended grill the config has no Artisan adapter for, a switch while a grill attempt runs or once a later step
// of the workflow has an attempt, a switch to unattended once the task has had a design round, and the mode already in
// effect. It exits 0 on success, 1 when the config or the store refuses or fails and 2 on a usage error.
func (a *App) taskGrill(args []string) int {
	flags := flag.NewFlagSet("task grill", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	reason := flags.String("reason", "", "the one-line reason for the mode")
	by := flags.String("by", store.GrillModeByDirector, "who the mode is set for: director or guildmaster")
	var cp copyFlags
	cp.register(flags)
	// Flags and arguments may come in any order; everything after -- is an argument.
	var positional []string
	for remaining := args; len(remaining) > 0; {
		if err := flags.Parse(remaining); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, taskGrillUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.taskGrillUsage(err.Error())
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
	cerr := cp.apply(a, path, cfgPath, nil)
	var repo, task string
	var ok bool
	if len(positional) == 2 {
		repo, task, ok = strings.Cut(positional[0], "/")
	}
	mode := workflow.GrillMode("")
	if len(positional) == 2 {
		mode = workflow.GrillMode(positional[1])
	}
	var usageErr string
	switch {
	case cerr != nil:
		usageErr = cerr.Error()
	case cp.searched:
		usageErr = cp.missing(a).Error()
	case *path == "":
		usageErr = "--store is required, or --copy"
	case *cfgPath == "":
		usageErr = "task grill requires --config, or --copy"
	case len(positional) != 2:
		usageErr = "task grill takes 2 arguments, <repo>/<task> and the mode"
	case !ok || !workflow.ValidID(repo) || !workflow.ValidID(task):
		usageErr = fmt.Sprintf("%q must be <repo>/<task>, each lowercase letters, digits and hyphens, starting with a letter", positional[0])
	case !slices.Contains(workflow.GrillModes, mode):
		usageErr = fmt.Sprintf("grill mode %q must be live or unattended", mode)
	case strings.TrimSpace(*reason) == "":
		usageErr = "task grill requires --reason, the one-line reason"
	case strings.ContainsAny(*reason, "\r\n"):
		usageErr = "--reason is one line"
	case *by != store.GrillModeByDirector && *by != store.GrillModeByGuildmaster:
		usageErr = fmt.Sprintf("--by %q must be director or guildmaster", *by)
	}
	if usageErr != "" {
		return a.taskGrillUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: task grill: %v\n", err)
		return 1
	}
	cfg, r, err := loadRepository(*cfgPath, repo)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{RequireStore: true})
	if err != nil {
		return fail(err)
	}
	out, err := func() (store.Receipt, error) {
		if err := refuseWhileRecovering(s); err != nil {
			return store.Receipt{}, err
		}
		t, err := s.Task(repo, task)
		if err != nil {
			return store.Receipt{}, err
		}
		def, err := boundWorkflow(filepath.Join(filepath.Dir(*cfgPath), "workflows"), cfg, r, t.Workflow)
		if err != nil {
			return store.Receipt{}, err
		}
		if err := checkGrillModeOn(cfg.CheckArtisanAdapters, def, mode); err != nil {
			return store.Receipt{}, err
		}
		records, err := s.GrillModes(repo, task)
		if err != nil {
			return store.Receipt{}, err
		}
		// A rerun of the command that recorded the latest record replays it before any check of what has moved on.
		if n := len(records); n > 0 && records[n-1].Mode == mode && records[n-1].Reason == *reason && records[n-1].By == *by {
			return setGrillMode(s, repo, task, mode, *reason, *by)
		}
		if current := store.CurrentGrillMode(records); current.Mode == mode {
			return store.Receipt{}, fmt.Errorf("task %s/%s's grill is already %s", repo, task, mode)
		}
		attempts, err := s.Attempts(repo, task)
		if err != nil {
			return store.Receipt{}, err
		}
		grill := def.Steps[workflow.DirectorGrill(def)].ID
		for _, at := range attempts {
			switch {
			case at.Step == grill && at.Outcome == "":
				return store.Receipt{}, fmt.Errorf("grill attempt %d of task %s/%s is running; switch the mode once it finishes", at.Number, repo, task)
			case at.Step != grill && slices.ContainsFunc(def.Steps, func(st workflow.Step) bool { return st.ID == at.Step }):
				return store.Receipt{}, fmt.Errorf("task %s/%s's %s has started, attempt %d; the grill mode changes only until then", repo, task, at.Step, at.Number)
			}
		}
		if mode == workflow.GrillUnattended {
			docs, err := s.Documents(repo, task)
			if err != nil {
				return store.Receipt{}, err
			}
			if n := interp.DesignRounds(docs); n > 0 {
				return store.Receipt{}, fmt.Errorf("task %s/%s has had %d design rounds, which follow a live grill; it stays live", repo, task, n)
			}
		}
		return setGrillMode(s, repo, task, mode, *reason, *by)
	}()
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

// checkGrillMode loads the Director config at cfgPath and refuses mode for a task bound to def (checkGrillModeOn).
func checkGrillMode(cfgPath string, def *workflow.Definition, mode workflow.GrillMode) error {
	cfg, err := admitDirector(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return checkGrillModeOn(cfg.CheckArtisanAdapters, def, mode)
}

// checkGrillModeOn refuses a grill mode for a task bound to def when def has no Director grill, such as a bug's
// workflow or one whose repository overrides the grill's actor, and an unattended mode whose Artisan grill the
// config's adapters cannot run, as admission judges the effective definition.
func checkGrillModeOn(checkAdapters func(*workflow.Definition) error, def *workflow.Definition, mode workflow.GrillMode) error {
	if workflow.DirectorGrill(def) < 0 {
		return fmt.Errorf("workflow %s has no grill run by the Director, so its tasks take no grill mode; a bug has no grill", def.Name)
	}
	applied, _ := workflow.ApplyGrillMode(def, mode)
	if err := checkAdapters(applied); err != nil {
		return fmt.Errorf("an unattended grill runs as an Artisan: %w; keep the grill live (--grill live), or give the profile an adapter", err)
	}
	return nil
}

// withGrillMode returns def, a task's bound workflow, with the task's grill mode in effect applied, as the engine runs
// it: commands that judge the task's steps, such as task depends's dependency gate and task grant's Artisan steps, read
// the same definition the engine advances.
func withGrillMode(s *store.Store, repo, task string, def *workflow.Definition) (*workflow.Definition, error) {
	records, err := s.GrillModes(repo, task)
	if err != nil {
		return nil, err
	}
	applied, _ := workflow.ApplyGrillMode(def, store.CurrentGrillMode(records).Mode)
	return applied, nil
}

// setGrillMode records the task's next grill-mode record. prior is the number of records the task has, unless the
// latest of them is this very command's, mode, reason and setter alike, when it is one fewer, so the store replays the
// original receipt.
func setGrillMode(s *store.Store, repo, task string, mode workflow.GrillMode, reason, by string) (store.Receipt, error) {
	all, err := s.GrillModes(repo, task)
	if err != nil {
		return store.Receipt{}, err
	}
	prior := len(all)
	if last := prior - 1; last >= 0 && all[last].Mode == mode && all[last].Reason == reason && all[last].By == by {
		prior--
	}
	return s.SetGrillMode(mutationID("grill-mode", repo, task, strconv.Itoa(prior)), repo, task, mode, reason, by, prior)
}

func (a *App) taskGrillUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskGrillUsage)
	return 2
}
