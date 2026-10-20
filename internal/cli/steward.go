package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/steward"
)

const stewardUsage = `Usage:
  guildhall steward rollout --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir> --dry-run
  guildhall steward rollout --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir> --apply [--approve <plan digest>]

The dry run fetches each Director copy on the named enrolled machines, writes the plan and file previews to
a new --plan directory and changes nothing. --apply carries out that plan only if nothing changed since.
`

// scpTimeout bounds each scp run during a rollout.
const scpTimeout = 2 * time.Minute

type labels []string

func (l *labels) String() string     { return strings.Join(*l, ",") }
func (l *labels) Set(v string) error { *l = append(*l, v); return nil }

// steward runs the steward subcommands and returns the exit code.
func (a *App) steward(args []string) int {
	if len(args) == 0 || args[0] != "rollout" {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: steward needs the rollout subcommand\n\n%s", stewardUsage)
		return 2
	}
	fs := flag.NewFlagSet("steward rollout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var machines labels
	cfgPath := fs.String("config", "", "")
	planDir := fs.String("plan", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	apply := fs.Bool("apply", false, "")
	approve := fs.String("approve", "", "")
	fs.Var(&machines, "machine", "")
	err := fs.Parse(args[1:])
	switch {
	case err != nil:
	case fs.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case *dryRun == *apply:
		err = errors.New("give exactly one of --dry-run and --apply")
	case *dryRun && *approve != "":
		err = errors.New("--approve applies only with --apply")
	case *cfgPath == "" || *planDir == "" || len(machines) == 0:
		err = errors.New("--config, --plan and at least one --machine are required")
	}
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: steward rollout: %v\n\n%s", err, stewardUsage)
		return 2
	}

	r, err := a.loadRollout(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx := context.Background()
	if *dryRun {
		p, err := r.Plan(ctx, machines, *planDir)
		if err != nil {
			return a.fail(err)
		}
		if err := steward.WritePlan(p); err != nil {
			return a.fail(err)
		}
		if _, err := io.WriteString(a.Stdout, renderPlan(p, *cfgPath)); err != nil {
			return 1
		}
		for _, c := range p.Copies {
			if c.Status == steward.StatusRefused {
				return 1
			}
		}
		return 0
	}

	receipt, err := r.Apply(ctx, *planDir, *approve, machines...)
	if errors.Is(err, steward.ErrApprovalRequired) {
		if saved, rerr := steward.ReadPlan(*planDir); rerr == nil {
			err = fmt.Errorf("%w; once the Guildmaster approves this exact plan, add --approve %s", err, saved.Digest)
		}
	}
	if receipt != nil {
		var b strings.Builder
		for _, c := range receipt.Copies {
			fmt.Fprintf(&b, "%s/%s: %s", c.Machine, c.Director, c.Status)
			if c.Error != "" {
				fmt.Fprintf(&b, ": %s", c.Error)
			}
			b.WriteByte('\n')
		}
		if receipt.File != "" {
			fmt.Fprintf(&b, "receipt: %s\n", receipt.File)
		}
		if _, werr := io.WriteString(a.Stdout, b.String()); werr != nil {
			return 1
		}
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *App) fail(err error) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %v\n", err)
	return 1
}

// loadRollout reads the Steward copy's config and the private config directory it names.
func (a *App) loadRollout(cfgPath string) (*steward.Rollout, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	if cfg.Mode != config.ModeSteward {
		return nil, fmt.Errorf("%s is not a steward config", cfgPath)
	}
	private := cfg.PrivateConfig.Location
	if info, err := os.Stat(private); !filepath.IsAbs(private) || err != nil || !info.IsDir() {
		return nil, fmt.Errorf("private_config.location %q must be a local directory holding the private config; clone it first", private)
	}
	if a.Copier != nil {
		return steward.LoadRollout(private, a.Copier)
	}
	scp, err := a.LookPath("scp")
	if err != nil {
		return nil, fmt.Errorf("scp is needed for rollouts: %w", err)
	}
	return steward.LoadRollout(private, steward.Transport{Path: scp, Timeout: scpTimeout})
}

// renderPlan describes a dry-run plan: each copy's changes, diff and planned commands.
func renderPlan(p *steward.Plan, cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rollout plan %s\ndry run: nothing was changed\n", p.Digest)
	for _, c := range p.Copies {
		fmt.Fprintf(&b, "\n%s/%s (%s:%s): %s\n", c.Machine, c.Director, c.SSHAlias, c.Target, c.Status)
		if c.Problem != "" {
			fmt.Fprintf(&b, "  problem: %s\n", c.Problem)
		}
		for _, ch := range c.Changes {
			switch ch.Kind {
			case steward.Removed:
				fmt.Fprintf(&b, "  removed %s", ch.Key)
			case steward.Added:
				fmt.Fprintf(&b, "  added %s = %s", ch.Key, describe(ch.After))
			default:
				fmt.Fprintf(&b, "  changed %s: %s -> %s", ch.Key, describe(ch.Before), describe(ch.After))
			}
			if ch.NeedsApproval {
				fmt.Fprintf(&b, " (needs Guildmaster approval: %s)", ch.Reason)
			}
			b.WriteByte('\n')
		}
		if c.Diff != "" {
			b.WriteString("  diff:\n")
			for line := range strings.Lines(c.Diff) {
				b.WriteString("    " + line)
			}
		}
		if len(c.Commands) > 0 {
			b.WriteString("  commands:\n")
			for _, argv := range c.Commands {
				b.WriteString("    " + shellQuote(argv) + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "\nplan and previews: %s\n", p.StageDir)
	apply := fmt.Sprintf("guildhall steward rollout --config %s", shellQuote([]string{cfgPath}))
	for _, m := range p.Machines {
		apply += " --machine " + m
	}
	apply += " --plan " + shellQuote([]string{p.StageDir}) + " --apply"
	if p.NeedsApproval() {
		apply += " --approve " + p.Digest
		b.WriteString("this plan needs Guildmaster approval of its exact digest before it is applied\n")
	}
	fmt.Fprintf(&b, "apply with: %s\n", apply)
	return b.String()
}

func describe(m *config.ManagedSetting) string {
	if m == nil {
		return "(none)"
	}
	s := fmt.Sprintf("%q", m.Value)
	if m.Overridable {
		s += " (overridable)"
	}
	return s
}

// plainArg also admits Windows path characters, which need no quoting to be read.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./\\~_-]+$`)

// shellQuote shows a command for reading; it is never run through a shell.
func shellQuote(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if plainArg.MatchString(arg) {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
