package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Backup is the [backup] table: where a Director copy backs up its store export and copy files
// (docs/running.md#backups). It is machine scope and outside the task-bound config digest, as Config.Backup's
// json tag keeps it, so adding or editing it changes no open task's digest. With no table, no backup runs.
type Backup struct {
	// Repo is the backup repository's git remote, an SSH remote such as
	// "git@guildhall-backup-main:ndrwl/guildhall-backups.git" whose host alias names the deploy key; it never
	// embeds a credential.
	Repo string `toml:"repo"`
	// Branch is this copy's own branch of Repo, "<machine>/<director>", <director> being the config's name.
	Branch string `toml:"branch"`
	// Every is how often the session supervisor backs up, as a Go duration; empty means DefaultBackupEvery.
	Every string `toml:"every"`
}

// DefaultBackupEvery is how often the session supervisor backs up when [backup] every is not set.
const DefaultBackupEvery = time.Hour

// backupBranchPart is one part of a backup branch: a machine label or a Director name.
var backupBranchPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// EveryDuration is the table's effective every.
func (b Backup) EveryDuration() (time.Duration, error) {
	if b.Every == "" {
		return DefaultBackupEvery, nil
	}
	d, err := time.ParseDuration(b.Every)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("backup: every %q must be a duration of at least a minute, such as \"1h\"", b.Every)
	}
	return d, nil
}

// checkBackup checks a [backup] table of a Director named name.
func checkBackup(b Backup, name string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	switch {
	case strings.TrimSpace(b.Repo) == "":
		add("backup: repo is required")
	case hasCredential(b.Repo):
		add("backup: repo must not contain credentials; name the deploy key in an SSH host alias instead")
	case strings.HasPrefix(b.Repo, "-"):
		add("backup: repo %q must not start with '-'", b.Repo)
	}
	machine, director, ok := strings.Cut(b.Branch, "/")
	switch {
	case b.Branch == "":
		add("backup: branch is required, as \"<machine>/%s\"", name)
	case !ok || !backupBranchPart.MatchString(machine) || !backupBranchPart.MatchString(director):
		add("backup: branch %q must be \"<machine>/<director>\", each a letter or digit followed by letters, digits, '-' or '_'", b.Branch)
	case director != name:
		add("backup: branch %q must end with this Director's name, as \"%s/%s\"", b.Branch, machine, name)
	}
	if _, err := b.EveryDuration(); err != nil {
		add("%v", err)
	}
	return problems
}
