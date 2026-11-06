package steward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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
// arguments after the program: guildhall, or an absolute guildhall_path, unless program names another.
// An argument written argDir is a value: an absolute path in the restricted character set, such as the copy directory
// most commands end with; argLabel is a lowercase id (workflow.ValidID); argHex a lowercase hex sha256; argIDs
// comma-separated step ids; argSteps a built-in step file's name or a step file's absolute path; argRemote a git
// remote (ValidRemote); argVersion a release version; and argSets, only last, zero or more `--set <name>=<value>` pairs (SetPattern).
// Only these run: no other command, flag or argument can be passed.
var allowedCommands = []struct {
	program string
	args    []string
	// reserved names the follow-up that builds a command, which is refused until it is built.
	reserved string
	// long marks a command that may take minutes, such as a clone or a session start, which SSH bounds with its
	// LongTimeout.
	long bool
}{
	// config reload authorizes the published managed.toml in the copy's own store.
	{args: []string{"config", "reload", "--copy", argDir}},
	// director mode --json reads the copy's mode and managed digest, changing nothing.
	{args: []string{"director", "mode", "--json", "--copy", argDir}},
	// director reload restarts the copy's session on a new release (docs/releases.md); built by guildhall/d98.
	{args: []string{"director", "reload", "--copy", argDir}, reserved: "guildhall/d98"},
	// lesson list and incident list --json --pending read the copy's lessons and incidents not yet collected,
	// changing nothing (docs/decisions/steward-collection-commands.md).
	{args: []string{"lesson", "list", "--json", "--pending", "--copy", argDir}},
	{args: []string{"incident", "list", "--json", "--pending", "--copy", argDir}},
	// director collected marks the items of the receipt the Steward copied to CollectReceiptPath as collected.
	{args: []string{"director", "collected", "--copy", argDir}},
	// messages receive takes the Steward message the Steward copied to MessageDropPath into the copy's inbox
	// (docs/decisions/steward-messages-directors.md).
	{args: []string{"messages", "receive", "--copy", argDir}},
	// The takeover commands of an approved setup plan (docs/decisions/steward-setup-plans.md): director status
	// --json only reads, and the Director health checks read it too (docs/decisions/steward-reads-director-status.md);
	// pause and resume move the copy's session; backup now and store export back its store up; director enrol
	// records the copy's enrolment with this Steward.
	{args: []string{"director", "status", "--json", "--copy", argDir}},
	{args: []string{"director", "pause", "--copy", argDir}},
	{args: []string{"director", "resume", "--copy", argDir}},
	{args: []string{"backup", "now", "--copy", argDir}},
	{args: []string{"store", "export", "--out", argDir, "--copy", argDir}},
	{args: []string{"director", "enrol", "--steward", argLabel, "--copy", argDir}},
	// secrets check --json reports whether the machine's secrets file decrypts with its key and which names it holds,
	// never a value, changing nothing (docs/decisions/steward-secrets-push.md).
	{args: []string{"secrets", "check", "--json", "--copy", argDir}},
	// The provisioning commands of an approved setup plan (docs/steward.md#provisioning): setup doctor --json reads
	// a step file's gaps, and with --fix --only closes only the fixable gaps the plan names, from a built-in or a
	// plan-bound step file whose digest it checks; store init makes the new copy's store; git clone clones a
	// repository into an empty path; release adopt gives the copy its first release pin; and director start --tab
	// opens the Director's Herdr tab.
	{args: []string{"setup", "doctor", "--json", "--steps", argSteps, "--digest", argHex, "--setup-dir", argDir, argSets}},
	{args: []string{"setup", "doctor", "--json", "--fix", "--only", argIDs, "--steps", argSteps, "--digest", argHex, "--setup-dir", argDir, argSets}, long: true},
	{args: []string{"store", "init", "--copy", argDir}},
	{args: []string{"release", "adopt", "--copy", argDir}, long: true},
	{args: []string{"director", "start", "--tab", "--copy", argDir}, long: true},
	{program: "git", args: []string{"clone", "--", argRemote, argDir}, long: true},
	// The bootstrap commands of an approved provisioning plan for a bare machine
	// (docs/steward.md#bootstrapping-a-bare-machine): sha256sum only reads a file the plan copied, which is checked
	// before it first runs, and release install --from --sha256, run by that file, installs it as a release after
	// checking its sha256 again itself.
	{program: "sha256sum", args: []string{argDir}},
	// certutil -hashfile <path> SHA256 is the Windows form of sha256sum: it only reads the file and prints its sha256.
	{program: "certutil", args: []string{"-hashfile", argDir, "SHA256"}},
	{args: []string{"release", "install", argVersion, "--from", argDir, "--sha256", argHex}, long: true},
}

// Value placeholders of allowedCommands.
const (
	argDir     = "<dir>"
	argLabel   = "<label>"
	argHex     = "<hex>"
	argIDs     = "<ids>"
	argSteps   = "<steps>"
	argRemote  = "<remote>"
	argSets    = "<sets>"
	argVersion = "<version>"
)

var (
	// remotePattern is the shape of a git remote a provisioning plan may clone: an https:// URL or git@host:path,
	// in a character set no remote shell interprets.
	remotePattern = regexp.MustCompile(`^(https://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~-]+)+|git@[A-Za-z0-9.-]+:[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*)$`)
	// SetPattern is a step file param a remote setup doctor is given: a lowercase identifier, then a value of
	// letters, digits and '.', '_', '~', '+', ':', '/' and '-', or none.
	SetPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}=[A-Za-z0-9._~+:/-]*$`)
	hexPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stepIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// ValidRemote reports whether remote is a git remote a provisioning plan may clone: an https:// URL or
// git@host:path of letters, digits, '.', '_', '~' and '-', with no "." or ".." path segment.
func ValidRemote(remote string) bool {
	if !remotePattern.MatchString(remote) {
		return false
	}
	var rest string
	if strings.HasPrefix(remote, "git@") {
		_, rest, _ = strings.Cut(remote, ":")
	} else {
		_, rest, _ = strings.Cut(strings.TrimPrefix(remote, "https://"), "/")
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

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

// SecretsCheckCommand is the read-only remote command that reports the machine's secrets file, with copyDir one of
// its Director copies, which the key and file must be outside: `<guildhall> secrets check --json --copy <copyDir>`.
func SecretsCheckCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "secrets", "check", "--json", "--copy", copyDir}
}

// DoctorCommand is `<guildhall> setup doctor --json --steps <steps> --digest <hex> --setup-dir <dir>` with a
// `--set <name>=<value>` for each of sets: the machine's gaps against one step file, changing nothing.
func DoctorCommand(guildhall, steps, digest, setupDir string, sets []string) []string {
	argv := []string{guildhall, "setup", "doctor", "--json", "--steps", steps, "--digest", digest, "--setup-dir", setupDir}
	for _, kv := range sets {
		argv = append(argv, "--set", kv)
	}
	return argv
}

// FixCommand is DoctorCommand with `--fix --only <ids>`: it closes only the fixable gaps ids names, checking each
// first, and prints the report the doctor derives afresh.
func FixCommand(guildhall, ids, steps, digest, setupDir string, sets []string) []string {
	argv := []string{guildhall, "setup", "doctor", "--json", "--fix", "--only", ids, "--steps", steps, "--digest", digest, "--setup-dir", setupDir}
	for _, kv := range sets {
		argv = append(argv, "--set", kv)
	}
	return argv
}

// StoreInitCommand is `<guildhall> store init --copy <copyDir>`, which makes the copy's store and refuses one that
// exists.
func StoreInitCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "store", "init", "--copy", copyDir}
}

// AdoptCommand is `<guildhall> release adopt --copy <copyDir>`: the guildhall on the machine's PATH becomes the
// copy's first release pin; run again, it reports what is in place.
func AdoptCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "release", "adopt", "--copy", copyDir}
}

// StartTabCommand is `<guildhall> director start --tab --copy <copyDir>`, which opens the Director's Herdr tab and
// runs its session there.
func StartTabCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "director", "start", "--tab", "--copy", copyDir}
}

// CloneCommand is `git clone -- <remote> <path>`, which refuses a path that is not empty.
func CloneCommand(remote, path string) []string {
	return []string{"git", "clone", "--", remote, path}
}

// HashCommand is the command that prints the sha256 of the file at the absolute path on a goos machine, changing
// nothing: `sha256sum <path>`, or on Windows `certutil -hashfile <path> SHA256`, which every Windows machine has.
func HashCommand(goos OS, path string) []string {
	if goos == OSWindows {
		return []string{"certutil", "-hashfile", path, "SHA256"}
	}
	return []string{"sha256sum", path}
}

// ParseHash reads the sha256 HashCommand printed on a goos machine: sha256sum's first field, or the line of hex
// certutil prints between its header and its completion line, in lowercase and with any spaces between the bytes,
// as older Windows prints them, removed.
func ParseHash(goos OS, out []byte) (string, bool) {
	if goos != OSWindows {
		fields := strings.Fields(string(out))
		if len(fields) == 0 || !hexPattern.MatchString(fields[0]) {
			return "", false
		}
		return fields[0], true
	}
	for line := range strings.Lines(string(out)) {
		if h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(line), " ", "")); hexPattern.MatchString(h) {
			return h, true
		}
	}
	return "", false
}

// hashMissing reports whether a failed HashCommand said there is no file at the path: sha256sum's "No such file or
// directory", or certutil's ERROR_FILE_NOT_FOUND or ERROR_PATH_NOT_FOUND (0x80070002, 0x80070003).
func hashMissing(msg string) bool {
	msg = strings.ToLower(msg)
	return missingSource(msg) || strings.Contains(msg, "error_file_not_found") || strings.Contains(msg, "error_path_not_found")
}

// InstallFromCommand is `<guildhall> release install <version> --from <file> --sha256 <hex>`: the binary at file,
// checked against sum, becomes release version in the machine's Guildhall home; installing it again changes nothing.
func InstallFromCommand(guildhall, version, file, sum string) []string {
	return []string{guildhall, "release", "install", version, "--from", file, "--sha256", sum}
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
// its program, guildhall or an absolute guildhall_path for a Guildhall command, then the command's words and values,
// every value in the restricted character set no remote shell interprets. A reserved command not yet built is
// refused too.
func CheckCommand(argv []string) error {
	_, err := allowed(argv)
	return err
}

// LongCommand reports whether argv is an allowed command that may take minutes, such as a clone or a session
// start, which SSH bounds with its LongTimeout.
func LongCommand(argv []string) bool {
	long, err := allowed(argv)
	return err == nil && long
}

func allowed(argv []string) (long bool, err error) {
	if len(argv) < 2 {
		return false, fmt.Errorf("%w: %q", ErrNotAllowed, argv)
	}
	guildhallProgram := argv[0] == DefaultGuildhall || validFile(argv[0])
	for _, c := range allowedCommands {
		switch {
		case c.program == "" && !guildhallProgram, c.program != "" && argv[0] != c.program:
			continue
		case !matchArgs(c.args, argv[1:]):
			continue
		case c.reserved != "":
			return false, fmt.Errorf("%w: %s is reserved for %s and not built yet", ErrNotAllowed, strings.Join(c.args[:2], " "), c.reserved)
		}
		return c.long, nil
	}
	if argv[0] != "git" && argv[0] != "sha256sum" && argv[0] != "certutil" && !guildhallProgram {
		return false, fmt.Errorf("%w: program %q must be %s or an absolute guildhall_path", ErrNotAllowed, argv[0], DefaultGuildhall)
	}
	return false, fmt.Errorf("%w: %q", ErrNotAllowed, argv[1:])
}

// matchArgs reports whether args fit pattern: each fixed word equal and each value of the kind its placeholder
// names, in the restricted character set; argSets, only last in a pattern, takes zero or more
// `--set <name>=<value>` pairs.
func matchArgs(pattern, args []string) bool {
	for i, p := range pattern {
		if p == argSets {
			rest := args[min(i, len(args)):]
			if len(rest)%2 != 0 {
				return false
			}
			for j := 0; j < len(rest); j += 2 {
				if rest[j] != "--set" || !SetPattern.MatchString(rest[j+1]) {
					return false
				}
			}
			return true
		}
		if i >= len(args) {
			return false
		}
		a := args[i]
		var ok bool
		switch p {
		case argDir:
			ok = validFile(a)
		case argLabel:
			ok = workflow.ValidID(a)
		case argHex:
			ok = hexPattern.MatchString(a)
		case argIDs:
			ok = a != ""
			for id := range strings.SplitSeq(a, ",") {
				ok = ok && stepIDPattern.MatchString(id)
			}
		case argSteps:
			ok = stepIDPattern.MatchString(a) || validFile(a)
		case argRemote:
			ok = ValidRemote(a)
		case argVersion:
			ok = versionPattern.MatchString(a)
		default:
			ok = a == p
		}
		if !ok {
			return false
		}
	}
	return len(pattern) == len(args)
}

// validFile reports whether path is an absolute file path, POSIX or Windows, in the restricted character set.
func validFile(path string) bool {
	return validDir(posixDirPattern, path) || validDir(windowsDirPattern, path)
}

// Runner runs the fixed remote commands on enrolled machines: `config reload` after a rollout publishes a copy's
// managed.toml, the read-only `director mode --json`, collection's read-only pending lists and
// `director collected`, `messages receive` after Deliver copies a Steward message, the read-only
// `director status --json` of the Director health checks, an approved takeover's status, pause, resume, backup,
// export and enrol, and an approved provisioning's setup doctor, store init, git clone, release adopt and
// director start --tab. SSH is the ssh implementation and stewardtest.Fleet an in-memory one; both pass
// stewardtest.RunRunnerContract.
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
	// Timeout bounds each ssh run, and LongTimeout, when set, each run of a LongCommand, such as a clone or a
	// session start, in its place.
	Timeout     time.Duration
	LongTimeout time.Duration
}

var _ Runner = SSH{}

// sshCommand is the ssh command that runs argv on the machine with alias, as plans and receipts record it.
// ssh joins argv with spaces for the remote side, which is safe because CheckCommand admits only values with no
// character a POSIX shell, cmd.exe or PowerShell interprets. On a Windows machine the line reaches the OpenSSH
// server's shell, cmd.exe by default, with Windows paths in their forward-slash form (C:/Users/...), which Windows
// programs read as they are; the one value PowerShell would split, a comma-separated --only, is a single step id in
// every plan step.
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
	timeout := s.Timeout
	if s.LongTimeout > 0 && LongCommand(argv) {
		timeout = s.LongTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
