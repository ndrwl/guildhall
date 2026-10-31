package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/backup"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
)

const backupUsage = "usage: guildhall backup now|status [--copy <dir>], or backup restore --copy <dir> [--repo <remote>] <branch|commit>"

// backupCmd runs `guildhall backup now|status|restore` (docs/running.md#backups).
func (a *App) backupCmd(args []string) int {
	if len(args) == 0 {
		return a.usageError(backupUsage)
	}
	switch args[0] {
	case "now", "status":
		return a.backupCopy(args[0], args[1:])
	case "restore":
		return a.backupRestore(args[1:])
	}
	return a.usageError(backupUsage)
}

// backupTarget is a copy's backup settings, read from its config.
type backupTarget struct {
	dir, storePath string
	cfg            *config.Config
	lockWait       time.Duration
}

// backupCopy runs `backup now` or `backup status` on the selected copy.
func (a *App) backupCopy(sub string, args []string) int {
	fl := flag.NewFlagSet("backup "+sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if err := fl.Parse(args); err != nil {
		return a.usageError("backup " + sub + ": " + err.Error())
	}
	if fl.NArg() > 0 {
		return a.usageError(backupUsage)
	}
	var storePath, cfgPath string
	if err := cp.apply(a, &storePath, &cfgPath, nil); err != nil {
		return a.usageError("backup " + sub + ": " + err.Error())
	}
	if cp.searched {
		return a.usageError("backup " + sub + ": " + cp.missing(a).Error())
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: backup %s: %v\n", sub, err)
		return 1
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fail(err)
	}
	if cfg.Backup == nil {
		return fail(fmt.Errorf("%s has no [backup] table, so this copy is not backed up (docs/running.md#backups)", cfgPath))
	}
	wait, err := cfg.LockWait()
	if err != nil {
		return fail(err)
	}
	t := backupTarget{dir: cp.dir, storePath: storePath, cfg: cfg, lockWait: wait}
	if sub == "status" {
		return a.backupStatus(t, fail)
	}
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(storePath))
	}
	res, err := backup.Run(context.Background(), backupOptions(t, a.Now, 0))
	for _, l := range res.LeftOut {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: backup now: left out %s (%s), over the %s limit\n", l.Path, humanBytes(l.Size), humanBytes(backup.DefaultSizeLimit))
	}
	if err != nil {
		return fail(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "branch: %s\nrepo: %s\n", cfg.Backup.Branch, cfg.Backup.Repo)
	if res.Commit != "" {
		fmt.Fprintf(&b, "commit: %s (receipt sequence %d)\n", res.Commit, res.ReceiptSequence)
	} else {
		fmt.Fprintf(&b, "commit: none; nothing changed since %s\n", res.Tip)
	}
	fmt.Fprintf(&b, "pushed: %s\n", res.Pushed)
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}

func backupOptions(t backupTarget, now func() time.Time, push time.Duration) backup.Options {
	return backup.Options{Copy: t.dir, Store: t.storePath, Config: *t.cfg.Backup, LockWait: t.lockWait, PushTimeout: push, Now: now}
}

// backupStatus prints the copy's last backup run from its stamp, and the local backup repository's size.
func (a *App) backupStatus(t backupTarget, fail func(error) int) int {
	stamp, err := backup.ReadStamp(filepath.Join(t.dir, "state"))
	if err != nil {
		return fail(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "branch: %s\nrepo: %s\nevery: %s\n", t.cfg.Backup.Branch, t.cfg.Backup.Repo, everyOf(t.cfg.Backup))
	if stamp.RanAt.IsZero() || stamp.Branch != t.cfg.Backup.Branch {
		b.WriteString("last run: none\n")
	} else {
		outcome := "ok"
		if !stamp.OK {
			outcome = "failed"
		}
		fmt.Fprintf(&b, "last run: %s, %s\n", stamp.RanAt.Format(time.RFC3339), outcome)
		if stamp.Commit != "" {
			fmt.Fprintf(&b, "last commit: %s at %s, receipt sequence %d\n", stamp.Commit, stamp.CommittedAt.Format(time.RFC3339), stamp.ReceiptSequence)
		} else {
			b.WriteString("last commit: none\n")
		}
		switch stamp.Pushed {
		case "":
			b.WriteString("last push: none\n")
		case stamp.Commit:
			fmt.Fprintf(&b, "last push: %s at %s\n", stamp.Pushed, stamp.PushedAt.Format(time.RFC3339))
		default:
			fmt.Fprintf(&b, "last push: %s at %s; the last commit waits for the next push\n", stamp.Pushed, stamp.PushedAt.Format(time.RFC3339))
		}
		if stamp.Error != "" {
			fmt.Fprintf(&b, "last error: %s\n", oneLine(stamp.Error))
		}
		for _, l := range stamp.LeftOut {
			fmt.Fprintf(&b, "left out: %s (%s), over the %s limit\n", l.Path, humanBytes(l.Size), humanBytes(backup.DefaultSizeLimit))
		}
	}
	size, err := dirSize(filepath.Join(t.dir, backup.RepoDir))
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(&b, "repository size: %s\n", humanBytes(size))
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}

func everyOf(b *config.Backup) string {
	d, err := b.EveryDuration()
	if err != nil {
		return b.Every
	}
	return d.String()
}

// dirSize sums the sizes of the regular files under dir; a missing dir is 0.
func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return n, err
}

// backupRestore runs `guildhall backup restore --copy <dir> [--repo <remote>] <branch|commit>`.
func (a *App) backupRestore(args []string) int {
	fl := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	target := fl.String("copy", "", "the copy directory to restore into")
	repo := fl.String("repo", "", "the backup repository; default the [backup] repo of the copy found from here, or of the target")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("backup restore: " + err.Error())
	}
	if *target == "" || fl.NArg() != 1 || fl.Arg(0) == "" {
		return a.usageError(backupUsage)
	}
	rev := fl.Arg(0)
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: backup restore: %v\n", err)
		return 1
	}
	storePath := filepath.Join(*target, directorRole.storeFile)
	if _, err := os.Lstat(storePath); err == nil {
		return fail(restoreRefusal(storePath))
	}
	remote := *repo
	if remote == "" {
		var err error
		if remote, err = a.restoreRepo(*target); err != nil {
			return a.usageError("backup restore: " + err.Error())
		}
	}
	got, err := backup.Restore(context.Background(), backup.RestoreOptions{Target: *target, Repo: remote, Rev: rev, Git: git.Program{}})
	if err != nil {
		return fail(err)
	}
	writeCopyMarker(a.Stderr, *target)
	tasks := 0
	for _, r := range got.Store.Repositories {
		tasks += r.Tasks
	}
	_, err = fmt.Fprintf(a.Stdout, "restored: %s\ncommit: %s\ncopy files: %d\nstore: %s, %d receipts, %d tasks\n", *target, got.Commit, got.Files, storePath, got.Store.Receipts, tasks)
	if err != nil {
		return 1
	}
	return 0
}

// restoreRefusal is why a restore is refused into a copy that has a store: its session runs, or it is restored.
func restoreRefusal(storePath string) error {
	s, err := store.Open(storePath, store.Options{ReadOnly: true})
	if err == nil {
		d, derr := s.LatestDirectorSession()
		_ = s.Close()
		if derr == nil && d.State != store.DirectorStopped {
			return fmt.Errorf("the copy's Director session %d is %s; stop it with guildhall director stop, and restore into a copy with no store", d.Number, d.State)
		}
	}
	return fmt.Errorf("%s already exists, and a restore never replaces a store: %w", storePath, store.ErrExists)
}

// restoreRepo is the backup repository a restore with no --repo fetches from: the [backup] repo of the copy found
// from the working directory, else of the target's own config.
func (a *App) restoreRepo(target string) (string, error) {
	var cfgs []string
	if dir, ok, err := discoverCopy(a.Getwd); err == nil && ok {
		cfgs = append(cfgs, filepath.Join(dir, "config.toml"))
	}
	cfgs = append(cfgs, filepath.Join(target, "config.toml"))
	for _, p := range cfgs {
		if cfg, err := config.Load(p); err == nil && cfg.Backup != nil {
			return cfg.Backup.Repo, nil
		}
	}
	return "", errors.New("no backup repository: pass --repo <remote>, or run it in a copy with a [backup] table")
}

// sessionBackup is the session supervisor's backup of its copy, run every [backup] every off the decision path; nil
// when the config has no [backup] table.
type sessionBackup struct {
	opts   backup.Options
	every  time.Duration
	stderr io.Writer
}

func (a *App) sessionBackup(cfg *config.Config, copyDir, storePath string) (*sessionBackup, error) {
	if cfg.Backup == nil {
		return nil, nil
	}
	every, err := cfg.Backup.EveryDuration()
	if err != nil {
		return nil, err
	}
	wait, err := cfg.LockWait()
	if err != nil {
		return nil, err
	}
	t := backupTarget{dir: copyDir, storePath: storePath, cfg: cfg, lockWait: wait}
	return &sessionBackup{opts: backupOptions(t, a.Now, backup.SupervisorPushTimeout), every: every, stderr: a.Stderr}, nil
}

// run is one round: a backup, with what it did written to the supervisor's stderr.
func (sb *sessionBackup) run() {
	res, err := backup.Run(context.Background(), sb.opts)
	for _, l := range res.LeftOut {
		sb.warn("left out %s (%s), over the %s limit", l.Path, humanBytes(l.Size), humanBytes(backup.DefaultSizeLimit))
	}
	switch {
	case err != nil:
		sb.warn("%v", err)
	case res.Commit != "":
		sb.warn("committed and pushed %s at receipt sequence %d", res.Commit, res.ReceiptSequence)
	}
}

func (sb *sessionBackup) warn(format string, args ...any) {
	if sb.stderr != nil {
		_, _ = fmt.Fprintf(sb.stderr, "guildhall: backup: "+format+"\n", args...)
	}
}
