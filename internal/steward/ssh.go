package steward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// DefaultGuildhall is the program a remote command runs when the roster names no guildhall_path: guildhall,
// found on the PATH of the machine's Guildhall account.
const DefaultGuildhall = "guildhall"

// ErrNotAllowed reports a remote command outside the Runner allow-list; nothing was run.
var ErrNotAllowed = errors.New("not one of the fixed remote commands a Steward may run")

// The fixed remote commands a Steward may run on an enrolled machine (docs/steward.md#remote-commands), as their
// arguments after the Guildhall program and before the copy directory, which every one of them ends with.
// Only these run: no other command, flag or argument can be passed.
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
		if len(argv) != len(c.args)+2 || !slices.Equal(argv[1:len(argv)-1], c.args) {
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

// validFile reports whether path is an absolute file path, POSIX or Windows, in the restricted character set.
func validFile(path string) bool {
	return validDir(posixDirPattern, path) || validDir(windowsDirPattern, path)
}

// Runner runs the fixed remote commands on enrolled machines: `config reload` after a rollout publishes a copy's
// managed.toml, and the read-only `director mode --json`. SSH is the ssh implementation and stewardtest.Fleet an
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
