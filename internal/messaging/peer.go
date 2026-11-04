package messaging

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultGuildhall is the program a peer's receive command names: guildhall, found on the PATH of the peer's
// account. The peer's authorized_keys forced command, which names its own program, runs instead of it over SSH.
const DefaultGuildhall = "guildhall"

// PeerReceiveCommand is the receive command a sender runs for one Director message to a peer, over SSH or, for a peer
// on the same machine and account, in process: `guildhall messages receive --copy <copyDir> --from <from> --to <to> -`,
// the envelope on standard input (docs/messaging.md#transport-between-machines). Over SSH, the peer's forced command
// for the sender's key replaces it and fixes --from; the command is still sent, as SSH_ORIGINAL_COMMAND.
func PeerReceiveCommand(copyDir, from, to string) []string {
	return []string{DefaultGuildhall, "messages", "receive", "--copy", copyDir, "--from", from, "--to", to, "-"}
}

// PeerRunner runs a peer's receive command on the account reached by an SSH alias, with stdin as its standard
// input, and returns what it printed on standard output. It fails, with the command's diagnostic, when the account
// cannot be reached, refuses the key or the command exits non-zero, and stops when ctx ends. PeerSSH is the ssh
// implementation; tests use a fake that runs the receiving copy's command in process.
type PeerRunner interface {
	Run(ctx context.Context, alias string, argv []string, stdin []byte) ([]byte, error)
}

// PeerSSH is the PeerRunner over ssh. It adds no credentials or trust options of its own, so the SSH config the
// alias names, its key, agent and host-key checks decide access, and batch mode makes ssh fail rather than prompt.
type PeerSSH struct {
	// Path is the resolved ssh executable.
	Path string
	// Timeout bounds each run.
	Timeout time.Duration
}

var _ PeerRunner = PeerSSH{}

// PeerSSHCommand is the ssh command that runs argv on the account with alias: `ssh -o BatchMode=yes -- <alias> <argv>`.
// ssh joins argv with spaces for the remote side, which is safe because every value is a lowercase id, a plain alias
// or a copy directory in the roster's character set, none of which a POSIX shell, cmd.exe or PowerShell interprets.
func PeerSSHCommand(alias string, argv []string) []string {
	return append([]string{"ssh", "-o", "BatchMode=yes", "--", alias}, argv...)
}

// Run runs argv on the account with alias through ssh, with stdin on its standard input.
func (s PeerSSH) Run(ctx context.Context, alias string, argv []string, stdin []byte) ([]byte, error) {
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}
	full := PeerSSHCommand(alias, argv)
	cmd := exec.CommandContext(ctx, s.Path, full[1:]...)
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
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
