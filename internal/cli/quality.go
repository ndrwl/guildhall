package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// qualityCmd runs `guildhall quality judge|judged|agreement` (docs/quality.md#running-it).
func (a *App) qualityCmd(args []string) int {
	switch {
	case len(args) > 0 && args[0] == "judge":
		return a.qualityJudge(args[1:])
	case len(args) > 0 && args[0] == "judged":
		return a.qualityJudged(args[1:])
	case len(args) > 0 && args[0] == "agreement":
		return a.qualityAgreement(args[1:])
	}
	return a.usageError("quality: the commands are judge, judged and agreement")
}

// qualityJudge runs `guildhall quality judge --repo <id> --review <n> <task>`: it starts the judge of a delivered
// task's self-review (quality.Judge.Start) and prints the attempt and the judge chosen.
func (a *App) qualityJudge(args []string) int {
	fl := flag.NewFlagSet("quality judge", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	review := fl.String("review", "", "self-review number")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("quality judge: " + err.Error())
	}
	n, err := strconv.Atoi(*review)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("quality judge: " + ferr.Error())
	case f.copy.dir == "":
		return a.usageError("quality judge: a copy is required, --copy or " + copyEnvVar + ", for its quality directory")
	case fl.NArg() != 1 || !workflow.ValidID(fl.Arg(0)):
		return a.usageError("quality judge takes one task id")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("quality judge: --review %q must be a positive self-review number", *review))
	}
	var started quality.Started
	err = a.withJudge(f, fl.Arg(0), func(j *quality.Judge) error {
		started, err = j.Start(context.Background(), f.repo, fl.Arg(0), n)
		return err
	})
	if errors.Is(err, store.ErrDirectorHeld) {
		return a.printJSON(map[string]string{"wait": "paused"})
	}
	if err != nil {
		return a.fail(fmt.Errorf("quality judge: %w", err))
	}
	return a.printJSON(started)
}

// qualityJudged runs `guildhall quality judged --repo <id> --task <id> --attempt <n>`, the judge attempt's launcher's
// advance: it takes the finished attempt (quality.Judge.Take) and prints what was recorded.
func (a *App) qualityJudged(args []string) int {
	fl := flag.NewFlagSet("quality judged", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	attempt := fl.String("attempt", "", "judge attempt number")
	if err := fl.Parse(args); err != nil {
		return a.usageError("quality judged: " + err.Error())
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("quality judged: " + ferr.Error())
	case f.copy.dir == "":
		return a.usageError("quality judged: a copy is required, --copy or " + copyEnvVar)
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("quality judged: unexpected argument %q", fl.Arg(0)))
	case !workflow.ValidID(*task):
		return a.usageError("quality judged: --task must be a task id")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("quality judged: --attempt %q must be a positive attempt number", *attempt))
	}
	var taken quality.Taken
	err = a.withJudge(f, *task, func(j *quality.Judge) error {
		taken, err = j.Take(f.repo, *task, n)
		return err
	})
	if err != nil {
		return a.fail(fmt.Errorf("quality judged: %w", err))
	}
	return a.printJSON(taken)
}

// withJudge runs fn with the copy's judge for the task, holding the task's lock, and wakes the Director for what it
// sent once the lock is released, as an advance does.
func (a *App) withJudge(f advanceFlags, task string, fn func(*quality.Judge) error) error {
	cfg, err := config.Load(f.config)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := cfg.ListedRepository(f.repo); err != nil {
		return err
	}
	if err := f.resolveWait(cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	stall, err := cfg.StallInterval()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	rails, err := cfg.GuardRails()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	state, err := filepath.Abs(f.state)
	if err != nil {
		return err
	}
	copyDir, err := filepath.Abs(f.copy.dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(f.store); err != nil {
		return noStore(f.store)
	}
	exe, err := a.Executable()
	if err != nil {
		return err
	}
	if a.InstallBaseline == nil {
		return errors.New("the release baseline installer is not configured")
	}
	release, err := os.MkdirTemp("", "guildhall-baseline-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(release) }()
	if err := a.InstallBaseline(release); err != nil {
		return err
	}
	cfgDir := filepath.Dir(f.config)
	taskDir := filepath.Join(state, f.repo, task)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return err
	}
	records := storeRecords{path: f.store, wait: f.wait}
	outbox := &deferredOutbox{records: records}
	defer outbox.wakeAll(wakingOutbox{records, a.directorPane(), a.Stderr})
	ctx, cancel := context.WithTimeout(context.Background(), f.wait)
	unlock, err := artisan.LockAttempt(ctx, taskDir)
	cancel()
	if err != nil {
		return fmt.Errorf("lock task %s/%s: %w", f.repo, task, err)
	}
	defer unlock()
	return fn(&quality.Judge{
		Records:  records,
		Outbox:   outbox,
		Launcher: &launch.Process{Executable: exe, Store: f.store, StateDir: state, Copy: copyDir, StallInterval: stall, LockWait: f.wait},
		Config:   cfg,
		StateDir: state, DocumentDir: filepath.Join(state, "documents"), QualityDir: filepath.Join(copyDir, "quality"),
		Role: baselineSource(filepath.Join(cfgDir, "baseline"), release, "roles/judge.md").Path,
		ProfileRole: func(name string) string {
			if p, err := cfg.Profile(name); err == nil && p.Role != "" {
				return filepath.Join(cfgDir, filepath.FromSlash(p.Role))
			}
			return ""
		},
		Caps: store.AttemptCaps{MaxRunning: rails.MaxRunningAttempts, MaxPerDay: rails.MaxAttemptsPerDay, Hold: true},
	})
}

// qualityAgreement runs `guildhall quality agreement [--repo <id>]`: the judge's agreement with the Guildmaster's
// audit over the config's repositories, or one (quality.Agreement).
func (a *App) qualityAgreement(args []string) int {
	fl := flag.NewFlagSet("quality agreement", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	if err := fl.Parse(args); err != nil {
		return a.usageError("quality agreement: " + err.Error())
	}
	if err := f.copy.apply(a.Getenv, &f.store, &f.config, &f.state); err != nil {
		return a.usageError("quality agreement: " + err.Error())
	}
	switch {
	case f.store == "" || f.config == "" || f.state == "":
		return a.usageError("quality agreement: --store, --config and --state are required, or --copy")
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("quality agreement: unexpected argument %q", fl.Arg(0)))
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return a.fail(fmt.Errorf("quality agreement: config: %w", err))
	}
	var repos []string
	for _, r := range cfg.Repositories {
		repos = append(repos, r.ID)
	}
	if f.repo != "" {
		if _, err := cfg.ListedRepository(f.repo); err != nil {
			return a.fail(fmt.Errorf("quality agreement: %w", err))
		}
		repos = []string{f.repo}
	}
	state, err := filepath.Abs(f.state)
	if err != nil {
		return a.fail(err)
	}
	rep, err := withStore(f.store, true, func(s *store.Store) (quality.Report, error) {
		return quality.Agreement(s, filepath.Join(state, "documents"), repos)
	})
	if err != nil {
		return a.fail(fmt.Errorf("quality agreement: %w", err))
	}
	return a.printJSON(rep)
}
