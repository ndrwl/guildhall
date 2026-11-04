package steward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// DefaultGuildhall is the program a remote command runs when the roster names no guildhall_path: guildhall,
// found on the PATH of the machine's Guildhall account.
const DefaultGuildhall = "guildhall"

// ErrNotAllowed reports a remote command outside the Runner allow-list; nothing was run.
var ErrNotAllowed = errors.New("not one of the fixed remote commands a Steward may run")

// The fixed remote commands a Steward may run on an enrolled machine (docs/steward.md#remote-commands), as their
// arguments after the Guildhall program and before the copy directory, which every one of them ends with.
// An argument written argDir is a value: an absolute path in the restricted character set; one written argLabel is
// a lowercase id (workflow.ValidID). Only these run: no other command, flag or argument can be passed.
var allowedCommands = []struct {
	args []string
	// reserved names the follow-up that builds a command, which is refused until it is built.
	reserved string
}{
	// config reload authorizes the published managed.toml in the copy's own store.
	{args: []string{"config", "reload", "--copy"}},
	// director mode --json reads the copy's mode and managed digest, changing nothing.
	{args: []string{"director", "mode", "--json", "--copy"}},
	// director reload restarts the copy's session on a new release (docs/releases.md); built by guildhall/d98.
	{args: []string{"director", "reload", "--copy"}, reserved: "guildhall/d98"},
	// lesson list and incident list --json --pending read the copy's lessons and incidents not yet collected,
	// changing nothing (docs/decisions/steward-collection-commands.md).
	{args: []string{"lesson", "list", "--json", "--pending", "--copy"}},
	{args: []string{"incident", "list", "--json", "--pending", "--copy"}},
	// director collected marks the items of the receipt the Steward copied to CollectReceiptPath as collected.
	{args: []string{"director", "collected", "--copy"}},
	// messages receive takes the Steward message the Steward copied to MessageDropPath into the copy's inbox
	// (docs/decisions/steward-messages-directors.md).
	{args: []string{"messages", "receive", "--copy"}},
	// The takeover commands of an approved setup plan (docs/decisions/steward-setup-plans.md): director status
	// --json only reads, and the Director health checks read it too (docs/decisions/steward-reads-director-status.md); pause and resume move the copy's session; backup now and store export back its store up;
	// director enrol records the copy's enrolment with this Steward.
	{args: []string{"director", "status", "--json", "--copy"}},
	{args: []string{"director", "pause", "--copy"}},
	{args: []string{"director", "resume", "--copy"}},
	{args: []string{"backup", "now", "--copy"}},
	{args: []string{"store", "export", "--out", argDir, "--copy"}},
	{args: []string{"director", "enrol", "--steward", argLabel, "--copy"}},
}

// Value placeholders of allowedCommands.
const (
	argDir   = "<dir>"
	argLabel = "<label>"
)

// StatusCommand is the read-only remote command that prints the copy's status feed as JSON:
// `<guildhall> director status --json --copy <copyDir>`.
func StatusCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "status", "--json", "--copy", copyDir}
}

// PauseCommand is `<guildhall> director pause --copy <copyDir>`: the copy's running session starts no new work.
func PauseCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "pause", "--copy", copyDir}
}

// ResumeCommand is `<guildhall> director resume --copy <copyDir>`: the copy's paused session runs again.
func ResumeCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "resume", "--copy", copyDir}
}

// BackupCommand is `<guildhall> backup now --copy <copyDir>`, for a copy whose config has a [backup] table.
func BackupCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "backup", "now", "--copy", copyDir}
}

// ExportCommand is `<guildhall> store export --out <out> --copy <copyDir>`: the store's text export into the new
// directory out on the machine.
func ExportCommand(guildhall, out, copyDir string) []string {
	return []string{guildhall, "store", "export", "--out", out, "--copy", copyDir}
}

// EnrolCommand is `<guildhall> director enrol --steward <label> --copy <copyDir>`: the copy records that the
// Steward with label manages it, with the digest of its managed.toml, which enrolment never authorizes.
func EnrolCommand(guildhall, label, copyDir string) []string {
	return []string{guildhall, "director", "enrol", "--steward", label, "--copy", copyDir}
}

// CollectReceiptPath is where, relative to a Director copy's directory, the Steward copies the receipt that
// `director collected` reads, marks and deletes.
const CollectReceiptPath = "state/steward/collect-receipt.json"

// PendingLessonsCommand is the read-only remote command that prints a copy's lessons not yet collected as JSON:
// `<guildhall> lesson list --json --pending --copy <copyDir>`.
func PendingLessonsCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "lesson", "list", "--json", "--pending", "--copy", copyDir}
}

// PendingIncidentsCommand is the incident form of PendingLessonsCommand.
func PendingIncidentsCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "incident", "list", "--json", "--pending", "--copy", copyDir}
}

// CollectedCommand is the remote command that marks the items of the receipt at CollectReceiptPath collected:
// `<guildhall> director collected --copy <copyDir>`.
func CollectedCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "collected", "--copy", copyDir}
}

// ReloadCommand is the remote command that authorizes the managed.toml published to the Director copy in copyDir,
// run with the Guildhall program guildhall: `<guildhall> config reload --copy <copyDir>`.
func ReloadCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "config", "reload", "--copy", copyDir}
}

// ModeCommand is the read-only remote command that prints the Director copy's mode as JSON:
// `<guildhall> director mode --json --copy <copyDir>`.
func ModeCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "mode", "--json", "--copy", copyDir}
}

// CheckCommand refuses argv, with an error wrapping ErrNotAllowed, unless it is one of the fixed remote commands:
// guildhall, or an absolute guildhall_path, then one allowed command and an absolute copy directory, every value in
// the restricted character set no remote shell interprets. A reserved command not yet built is refused too.
func CheckCommand(argv []string) error {
	if len(argv) < 2 {
		return fmt.Errorf("%w: %q", ErrNotAllowed, argv)
	}
	if argv[0] != DefaultGuildhall && !validFile(argv[0]) {
		return fmt.Errorf("%w: program %q must be %s or an absolute guildhall_path", ErrNotAllowed, argv[0], DefaultGuildhall)
	}
	for _, c := range allowedCommands {
		if len(argv) != len(c.args)+2 || !matchArgs(c.args, argv[1:len(argv)-1]) {
			continue
		}
		if c.reserved != "" {
			return fmt.Errorf("%w: %s is reserved for %s and not built yet", ErrNotAllowed, strings.Join(c.args[:2], " "), c.reserved)
		}
		if dir := argv[len(argv)-1]; !validDir(posixDirPattern, dir) && !validDir(windowsDirPattern, dir) {
			return fmt.Errorf("%w: copy directory %q must be an absolute path of letters, digits, '.', '_', '-' and '/'", ErrNotAllowed, dir)
		}
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNotAllowed, argv[1:])
}

// matchArgs reports whether args fit pattern: each fixed word equal, each argDir an absolute file path and each
// argLabel a lowercase id, all in the restricted character set.
func matchArgs(pattern, args []string) bool {
	if len(pattern) != len(args) {
		return false
	}
	for i, p := range pattern {
		switch p {
		case argDir:
			if !validFile(args[i]) {
				return false
			}
		case argLabel:
			if !workflow.ValidID(args[i]) {
				return false
			}
		default:
			if args[i] != p {
				return false
			}
		}
	}
	return true
}

// validFile reports whether path is an absolute file path, POSIX or Windows, in the restricted character set.
func validFile(path string) bool {
	return validDir(posixDirPattern, path) || validDir(windowsDirPattern, path)
}

// Runner runs the fixed remote commands on enrolled machines: `config reload` after a rollout publishes a copy's
// managed.toml, the read-only `director mode --json`, collection's read-only pending lists and
// `director collected`, `messages receive` after Deliver copies a Steward message, the read-only
// `director status --json` of the Director health checks, and an approved takeover's status, pause, resume,
// backup, export and enrol. SSH is the ssh implementation and stewardtest.Fleet an
// in-memory one; both pass stewardtest.RunRunnerContract.
//
// Run refuses an alias and argv that CheckRun refuses, with an error wrapping ErrNotAllowed, and runs nothing. Otherwise it runs argv as one fixed command on the machine
// with alias and returns what the command printed on standard output. It fails, with an error carrying the
// command's diagnostic, when the machine refuses or cannot be reached or the command exits non-zero, as an older
// guildhall that does not know the command does; it stops when ctx ends, with an error that wraps ctx.Err().
// A Runner adds no credentials of its own and runs no shell of its own.
type Runner interface {
	Run(ctx context.Context, alias string, argv []string) ([]byte, error)
}

// SSH is the Runner that runs remote commands with ssh. Like Transport it adds no credentials or trust options,
// so SSH's own config, agent, keys and host-key checks decide access, and batch mode makes ssh fail rather than prompt.
type SSH struct {
	// Path is the resolved ssh executable.
	Path string
	// Timeout bounds each ssh run.
	Timeout time.Duration
}

var _ Runner = SSH{}

// sshCommand is the ssh command that runs argv on the machine with alias, as plans and receipts record it.
// ssh joins argv with spaces for the remote side, which is safe because CheckCommand admits only values with no
// character a POSIX shell, cmd.exe or PowerShell interprets.
func sshCommand(alias string, argv []string) []string {
	return append([]string{"ssh", "-o", "BatchMode=yes", "--", alias}, argv...)
}

// CheckRun refuses, with an error wrapping ErrNotAllowed, an alias or a command a Runner must not run: an alias
// that is not a plain roster alias, or a command CheckCommand refuses.
func CheckRun(alias string, argv []string) error {
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("%w: ssh alias %q must be a plain Host from your SSH config", ErrNotAllowed, alias)
	}
	return CheckCommand(argv)
}

// Run runs argv on the machine with alias through ssh.
func (s SSH) Run(ctx context.Context, alias string, argv []string) ([]byte, error) {
	if err := CheckRun(alias, argv); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	full := sshCommand(alias, argv)
	cmd := exec.CommandContext(ctx, s.Path, full[1:]...)
	configureCancellation(cmd)
	// Bound pipe draining even if a descendant escapes process cancellation.
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// ssh stopped by the context reports only the signal that killed it.
			err = ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("ssh: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("ssh: %w", err)
	}
	return stdout.Bytes(), nil
}
