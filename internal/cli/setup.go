package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/setup"
)

const setupUsage = `Usage:
  guildhall setup doctor [--json] [--fix [--only <ids>]] [--steps <file|built-in> [--digest <hex>]] [--set <name>=<value>]... [--setup-dir <dir>]
  guildhall setup wizard --steps <file|built-in> [--digest <hex>] [--only <ids>] [--set <name>=<value>]... [--setup-dir <dir>] [--dry-run]
  guildhall setup run --steps <file|built-in> [--digest <hex>] [--only <ids>] [--set <name>=<value>]... [--setup-dir <dir>]

--steps names a built-in step file (` + "herdr, go, git-and-claude, sign-in, wsl-settings" + `) or a step file's path, which needs
--digest, its sha256. Without --steps the doctor checks every built-in. --only takes comma-separated step ids.
`

// listFlag collects a repeated flag; --only also splits on commas.
type listFlag struct {
	vals  []string
	split bool
}

func (l *listFlag) String() string { return strings.Join(l.vals, ",") }

func (l *listFlag) Set(v string) error {
	if !l.split {
		l.vals = append(l.vals, v)
		return nil
	}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			l.vals = append(l.vals, p)
		}
	}
	return nil
}

// setupFlags are the flags every setup command shares.
type setupFlags struct {
	fs       *flag.FlagSet
	steps    *string
	digest   *string
	setupDir *string
	only     listFlag
	set      listFlag
}

func newSetupFlags(name string) *setupFlags {
	f := &setupFlags{fs: flag.NewFlagSet("setup "+name, flag.ContinueOnError), only: listFlag{split: true}}
	f.fs.SetOutput(io.Discard)
	f.steps = f.fs.String("steps", "", "")
	f.digest = f.fs.String("digest", "", "")
	f.setupDir = f.fs.String("setup-dir", "", "")
	f.fs.Var(&f.only, "only", "")
	f.fs.Var(&f.set, "set", "")
	return f
}

// setupCmd runs `guildhall setup run|wizard|doctor` (docs/setup.md).
func (a *App) setupCmd(args []string) int {
	if len(args) == 0 {
		return a.setupUsageError(errors.New("missing subcommand"))
	}
	switch args[0] {
	case "doctor":
		return a.setupDoctor(args[1:])
	case "wizard":
		return a.setupWizard(args[1:])
	case "run":
		return a.setupRun(args[1:])
	case "help", "-h", "--help":
		_, _ = io.WriteString(a.Stdout, setupUsage)
		return 0
	}
	return a.setupUsageError(fmt.Errorf("unknown subcommand %q", args[0]))
}

func (a *App) setupUsageError(err error) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: setup: %v\n\n%s", err, setupUsage)
	return 2
}

// parse parses args, refusing positional arguments, and returns the values of --set.
func (f *setupFlags) parse(args []string) (map[string]string, error) {
	if err := f.fs.Parse(args); err != nil {
		return nil, err
	}
	if f.fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", f.fs.Arg(0))
	}
	vars := map[string]string{}
	for _, kv := range f.set.vals {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q is not <name>=<value>", kv)
		}
		vars[k] = v
	}
	if *f.digest != "" && *f.steps == "" {
		return nil, errors.New("--digest needs --steps")
	}
	return vars, nil
}

// loadStepFiles resolves --steps: a built-in's name, or a path that must come with its --digest. No --steps means
// every built-in.
func loadStepFiles(ref, digest string) ([]setup.Steps, error) {
	if ref == "" {
		var out []setup.Steps
		for _, name := range setup.Builtins() {
			s, err := setup.LoadBuiltin(name, "")
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	if !strings.ContainsAny(ref, `/\`) && !strings.HasSuffix(ref, ".toml") {
		s, err := setup.LoadBuiltin(ref, digest)
		return []setup.Steps{s}, err
	}
	if digest == "" {
		return nil, fmt.Errorf("step file %s needs --digest <its sha256>; only a built-in runs without one", ref)
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return nil, err
	}
	s, err := setup.LoadSteps(abs, digest)
	return []setup.Steps{s}, err
}

// facts builds the engine's facts for s: the setup directory (--setup-dir, else the Guildhall home's setup), and
// the --set values s declares. A value no file declares is refused.
func (a *App) setupFacts(f *setupFlags, files []setup.Steps, vars map[string]string) ([]setup.Facts, error) {
	dir := *f.setupDir
	if dir == "" {
		home, err := a.releaseHome()
		if err != nil {
			return nil, fmt.Errorf("no --setup-dir and %w", err)
		}
		dir = filepath.Join(home.Dir, "setup")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	out := make([]setup.Facts, len(files))
	for i, s := range files {
		mine := map[string]string{}
		for k, v := range vars {
			if _, ok := s.Params[k]; ok {
				mine[k], used[k] = v, true
			}
		}
		out[i] = setup.Facts{SetupDir: dir, Vars: mine, Stdin: a.Stdin, Stdout: a.Stdout, Stderr: a.Stderr}
	}
	for k := range vars {
		if !used[k] {
			return nil, fmt.Errorf("--set %s: no step file here declares that param", k)
		}
	}
	return out, nil
}

func (a *App) setupDoctor(args []string) int {
	f := newSetupFlags("doctor")
	asJSON := f.fs.Bool("json", false, "")
	fix := f.fs.Bool("fix", false, "")
	vars, err := f.parse(args)
	switch {
	case err != nil:
	case len(f.only.vals) > 0 && !*fix:
		err = errors.New("--only belongs to --fix")
	case *asJSON && *f.steps == "":
		err = errors.New("--json reports one step file; name it with --steps")
	}
	if err != nil {
		return a.setupUsageError(fmt.Errorf("doctor: %w", err))
	}
	files, err := loadStepFiles(*f.steps, *f.digest)
	if err != nil {
		return a.fail(fmt.Errorf("setup doctor: %w", err))
	}
	facts, err := a.setupFacts(f, files, vars)
	if err != nil {
		return a.fail(fmt.Errorf("setup doctor: %w", err))
	}
	ctx := context.Background()
	gaps, failed := 0, false
	for i, s := range files {
		var r setup.Report
		var err error
		if *fix {
			r, err = setup.Fix(ctx, s, facts[i], f.only.vals, &setup.FileJournal{Dir: facts[i].SetupDir})
			if r.Schema == "" && err != nil {
				return a.fail(fmt.Errorf("setup doctor --fix: %w", err))
			}
		} else if r, err = setup.Doctor(ctx, s, facts[i]); err != nil {
			return a.fail(fmt.Errorf("setup doctor: %w", err))
		}
		if err != nil {
			failed = true
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: setup doctor --fix: %v\n", err)
		}
		gaps += len(r.Gaps)
		if *asJSON {
			data, _ := json.MarshalIndent(r, "", "  ")
			if _, err := fmt.Fprintf(a.Stdout, "%s\n", data); err != nil {
				return 1
			}
			continue
		}
		if err := writeReport(a.Stdout, s, r); err != nil {
			return 1
		}
	}
	switch {
	case failed:
		return 1
	case *asJSON:
		return 0
	}
	if gaps == 0 {
		_, _ = fmt.Fprintln(a.Stdout, "no gaps")
		return 0
	}
	_, _ = fmt.Fprintf(a.Stdout, "%d gap(s); run each action line above\n", gaps)
	return 1
}

// writeReport prints a report as the doctor's text: one line per check, each gap's action line under it.
func writeReport(w io.Writer, s setup.Steps, r setup.Report) error {
	kind := "file"
	if s.Builtin {
		kind = "built-in"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s %s, sha256 %s)\n", s.Title, kind, s.Ref, s.Digest)
	actions := map[string]setup.Gap{}
	for _, g := range r.Gaps {
		actions[g.ID] = g
	}
	for _, c := range r.Checks {
		if c.OK {
			fmt.Fprintf(&b, "  ok   %s: %s\n", c.ID, c.Title)
			continue
		}
		g := actions[c.ID]
		fmt.Fprintf(&b, "  gap  %s [%s]: %s\n", c.ID, c.Tag, c.Title)
		if g.Detail != "" {
			fmt.Fprintf(&b, "       saw: %s\n", firstLine(g.Detail))
		}
		fmt.Fprintf(&b, "       do:  %s\n", g.Action)
	}
	for _, id := range r.Skipped {
		fmt.Fprintf(&b, "  skip %s: not for %s\n", id, r.OS)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (a *App) setupWizard(args []string) int {
	f := newSetupFlags("wizard")
	dryRun := f.fs.Bool("dry-run", false, "")
	vars, err := f.parse(args)
	if err == nil && *f.steps == "" {
		err = errors.New("--steps is required")
	}
	if err != nil {
		return a.setupUsageError(fmt.Errorf("wizard: %w", err))
	}
	files, err := loadStepFiles(*f.steps, *f.digest)
	if err != nil {
		return a.fail(fmt.Errorf("setup wizard: %w", err))
	}
	facts, err := a.setupFacts(f, files, vars)
	if err != nil {
		return a.fail(fmt.Errorf("setup wizard: %w", err))
	}
	p := &setup.TermPrompter{In: a.Stdin, Out: a.Stdout}
	facts[0].Stdin = nil // the prompter reads the terminal; a run command gets none of its buffered input
	_, err = setup.Wizard(context.Background(), files[0], facts[0], setup.WizardOptions{IDs: f.only.vals, DryRun: *dryRun},
		p, &setup.FileJournal{Dir: facts[0].SetupDir})
	if err != nil {
		return a.fail(fmt.Errorf("setup wizard: %w", err))
	}
	return 0
}

func (a *App) setupRun(args []string) int {
	f := newSetupFlags("run")
	vars, err := f.parse(args)
	if err == nil && *f.steps == "" {
		err = errors.New("--steps is required")
	}
	if err != nil {
		return a.setupUsageError(fmt.Errorf("run: %w", err))
	}
	files, err := loadStepFiles(*f.steps, *f.digest)
	if err != nil {
		return a.fail(fmt.Errorf("setup run: %w", err))
	}
	facts, err := a.setupFacts(f, files, vars)
	if err != nil {
		return a.fail(fmt.Errorf("setup run: %w", err))
	}
	out, err := setup.Run(context.Background(), files[0], facts[0], f.only.vals, &setup.FileJournal{Dir: facts[0].SetupDir})
	if err != nil {
		return a.fail(fmt.Errorf("setup run: %w", err))
	}
	if out.Waiting != "" {
		_, err = fmt.Fprintf(a.Stdout, "waiting %s\nrun: %s\n", out.Waiting, out.Action)
	} else {
		_, err = fmt.Fprintln(a.Stdout, "done")
	}
	if err != nil {
		return 1
	}
	return 0
}
