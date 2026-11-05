package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// Director-to-Director messages (docs/messaging.md#director-to-director-messages): a Director sends a peer one of the
// four Director message types with `messages send --to <peer>`, which records it in its outbox and delivers it with the
// peer's receive command, in process for a peer on this machine and account and over SSH for any other. Like every
// message it informs only.

// peerDeliveryTimeout bounds one delivery over SSH.
const peerDeliveryTimeout = 30 * time.Second

// peerDeliveryEvery is how often a Director session's supervisor retries its pending peer messages.
const peerDeliveryEvery = time.Minute

// peerRunner is the transport to peers over SSH: a.PeerRunner, or ssh found with LookPath.
func (a *App) peerRunner() (messaging.PeerRunner, error) {
	if a.PeerRunner != nil {
		return a.PeerRunner, nil
	}
	ssh, err := a.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}
	return messaging.PeerSSH{Path: ssh, Timeout: peerDeliveryTimeout}, nil
}

// messagesSendPeer runs `guildhall messages send --to <peer> [--copy <dir>] <envelope-file|->`: it checks the Director
// message envelope, records it pending in the copy's outbox under its id, delivers it to the [[peer]] named --to and
// prints the outbox entry: delivered with the peer's receipt, refused with its reason, or still pending with the
// error, which the session supervisor retries. Sending the same envelope again prints its entry and delivers it only
// while it is pending; another envelope under the same id is refused. It exits 0 when the entry is delivered or
// pending and 1 when the peer refused it.
func (a *App) messagesSendPeer(args []string) int {
	const name = "messages send"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	to := fl.String("to", "", "the peer Director")
	dir, storePath, code := a.externalCopy(name, fl, &cp, args)
	if code >= 0 {
		return code
	}
	if fl.NArg() != 1 {
		return a.messagesUsage(name + " --to takes one envelope file or - for standard input")
	}
	cfg, err := config.LoadUnadmitted(filepath.Join(dir, "config.toml"))
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	peer, ok := cfg.Peer(*to)
	if !ok {
		return a.fail(fmt.Errorf("%s: %q is not a [[peer]] of this Director's config; its peers are %q", name, *to, cfg.PeerNames()))
	}
	data, err := a.readBounded(fl.Arg(0))
	if err != nil {
		return a.fail(fmt.Errorf("%s: message body: %w", name, err))
	}
	env, err := messaging.DecodeDirector(bytes.NewReader(data))
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	// The receiver checks the same rules; checking them here keeps a message it would refuse out of the outbox.
	m, err := messaging.ExternalMessage(cfg.Name, peer.Name, env)
	if err == nil {
		err = store.ValidateExternalMessage(m)
	}
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	canonical, err := json.Marshal(env)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	e, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.OutboxEntry, error) {
		if m.Type == store.MessageAnswer {
			if err := receivedQuestion(s, peer.Name, m.InReplyTo); err != nil {
				return store.OutboxEntry{}, err
			}
		}
		if _, err := s.RecordOutbox(env.ID, peer.Name, canonical); err != nil {
			return store.OutboxEntry{}, err
		}
		return s.OutboxEntryOf(env.ID)
	})
	if errors.Is(err, store.ErrMutationConflict) {
		return a.fail(fmt.Errorf("%s: message %s is already in the outbox with other content or to another peer; choose a new id", name, env.ID))
	}
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if e.State == store.OutboxPending {
		ctx, cancel := context.WithTimeout(context.Background(), peerDeliveryTimeout+5*time.Second)
		problem := a.deliverToPeer(ctx, cfg, storePath, e)
		cancel()
		if problem != "" {
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: not delivered yet, kept pending for the session supervisor to retry: %s\n", name, problem)
		}
		if e, err = withStore(storePath, true, func(s *store.Store) (store.OutboxEntry, error) { return s.OutboxEntryOf(env.ID) }); err != nil {
			return a.fail(fmt.Errorf("%s: %w", name, err))
		}
	}
	if code := a.printJSON(e); code != 0 {
		return code
	}
	if e.State == store.OutboxRefused {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %s refused message %s: %s\n", name, peer.Name, e.ID, e.Reason)
		return 1
	}
	return 0
}

// receivedQuestion refuses, as invalid, an answer to id unless id is a question peer sent this Director.
func receivedQuestion(s *store.Store, peer, id string) error {
	q, err := s.External(peer, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: in_reply_to %q is no message %s sent this Director", store.ErrInvalid, id, peer)
	}
	if err != nil {
		return err
	}
	if q.Type != store.MessageQuestion {
		return fmt.Errorf("%w: in_reply_to %q is a %s from %s, not a question", store.ErrInvalid, id, q.Type, peer)
	}
	return nil
}

// deliverToPeer makes one delivery attempt of the pending outbox entry e of the Director copy with config cfg and
// store at storePath, and records it: delivered with the peer's receipt, refused with its reason, which is never
// retried, or kept pending with the error, which it returns.
func (a *App) deliverToPeer(ctx context.Context, cfg *config.Config, storePath string, e store.OutboxEntry) string {
	var res messaging.ReceiveResult
	var failure string
	peer, ok := cfg.Peer(e.To)
	switch {
	case !ok:
		failure = fmt.Sprintf("%s is no longer a [[peer]] of this Director's config", e.To)
	case peer.Local():
		var err error
		if res, err = a.receiveLocalPeer(peer, cfg.Name, e.Envelope); err != nil {
			failure = oneLine(err.Error())
		}
	default:
		run, err := a.peerRunner()
		var out []byte
		if err == nil {
			out, err = run.Run(ctx, peer.SSHAlias, messaging.PeerReceiveCommand(peer.CopyDir, cfg.Name, peer.Name), e.Envelope)
		}
		if err == nil {
			res, err = messaging.ParseReceiveResult(out)
		}
		if err != nil {
			failure = oneLine(err.Error())
		}
	}
	var receipt []byte
	var reason string
	if failure == "" {
		if res.Status == messaging.ReceiveRefused {
			reason = res.Reason
		} else {
			var err error
			if receipt, err = json.Marshal(res.Receipt); err != nil {
				failure = err.Error()
			}
		}
	}
	if _, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.Receipt, error) {
		return s.OutboxAttempt(e.ID, e.Attempts+1, receipt, reason, truncateUTF8(failure, store.MaxMessageTextBytes))
	}); err != nil {
		return fmt.Sprintf("record attempt: %v", err)
	}
	return failure
}

// receiveLocalPeer runs the receive of a peer on this machine and account in process, against the peer's copy: the
// message envelope from the Director named from, surfaced and woken for in the peer's copy when delivered.
func (a *App) receiveLocalPeer(peer config.Peer, from string, envelope []byte) (messaging.ReceiveResult, error) {
	dir := filepath.FromSlash(peer.CopyDir)
	storePath := filepath.Join(dir, directorStoreFile)
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return messaging.ReceiveResult{}, noStore(storePath)
	}
	cfg, err := config.LoadUnadmitted(filepath.Join(dir, "config.toml"))
	if err != nil {
		return messaging.ReceiveResult{}, fmt.Errorf("peer %s: %w", peer.Name, err)
	}
	res, err := a.acceptPeer(cfg, storePath, from, peer.Name, envelope)
	if err != nil {
		return res, fmt.Errorf("peer %s: %w", peer.Name, err)
	}
	if res.Status == messaging.ReceiveDelivered {
		a.announceExternal("messages send", storePath, from, res.Receipt)
	}
	return res, nil
}

// deliverPeerOutbox makes one delivery attempt of every pending outbox entry of the Director copy with config cfg and
// store at storePath, one at a time, and returns what went wrong. A refused entry is never retried.
func (a *App) deliverPeerOutbox(ctx context.Context, cfg *config.Config, storePath string) []string {
	entries, err := withStore(storePath, true, func(s *store.Store) ([]store.OutboxEntry, error) { return s.Outbox(false) })
	if err != nil {
		return []string{fmt.Sprintf("outbox: %v", err)}
	}
	var problems []string
	for _, e := range entries {
		if e.State != store.OutboxPending {
			continue
		}
		if p := a.deliverToPeer(ctx, cfg, storePath, e); p != "" {
			problems = append(problems, fmt.Sprintf("%s: %s: %s", e.To, e.ID, p))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return problems
}

// sessionPeerDeliveries is a Director session's retry of its pending peer messages, every peerDeliveryEvery off the
// decision path: deterministic code with no model turn. It is nil for a copy with no [[peer]].
func (a *App) sessionPeerDeliveries(cfg *config.Config, storePath string) *workspaceCloseWatch {
	if len(cfg.Peers) == 0 {
		return nil
	}
	return &workspaceCloseWatch{every: peerDeliveryEvery, round: func() {
		ctx, cancel := context.WithTimeout(context.Background(), peerDeliveryEvery)
		defer cancel()
		for _, p := range a.deliverPeerOutbox(ctx, cfg, storePath) {
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: peer messages: %s\n", p)
		}
	}}
}

// peerSetup is what `messages peer setup` prints: the commands the Guildmaster runs, on this machine and on the
// peer's, so this Director's key reaches the peer's receive command and nothing else. Guildhall runs none of them.
type peerSetup struct {
	Peer string `json:"peer"`
	// Local is set for a peer on this machine and account, which needs no SSH and no commands.
	Local bool `json:"local"`
	// Key is the key file this Director's messages to the peer use, under ~/.ssh on this machine.
	Key string `json:"key,omitempty"`
	// ForcedCommand is the receive command the peer's authorized_keys fixes for the key, its program found on the
	// peer's account by the commands below.
	ForcedCommand string `json:"forced_command,omitempty"`
	// ThisMachine are the commands to run on this machine, for its operating system, OS.
	OS          string   `json:"os,omitempty"`
	ThisMachine []string `json:"this_machine,omitempty"`
	// PeerLinux and PeerWindows are the commands to run in the peer's account, by the peer's operating system.
	PeerLinux   []string `json:"peer_linux,omitempty"`
	PeerWindows []string `json:"peer_windows,omitempty"`
	// PeerConfig is the [[peer]] entry the peer's config.toml needs for this Director, so it takes its messages.
	PeerConfig string   `json:"peer_config"`
	Notes      []string `json:"notes"`
}

// publicKeyLine admits a public key line that is safe to print inside the quoted commands: a type, base64 and an
// optional plain comment.
var publicKeyLine = regexp.MustCompile(`^[a-z0-9-]+ [A-Za-z0-9+/=]+( [A-Za-z0-9._@-]+)?$`)

// messagesPeerCmd runs `guildhall messages peer setup <peer> [--copy <dir>]` (docs/messaging.md#setting-up-a-peer):
// it prints, as peerSetup, the exact commands that create this Director's key for the peer and its SSH alias on this
// machine, and the forced-command authorized_keys line on the peer's account, for Linux and for Windows OpenSSH under
// a non-administrator account (R1.47). It runs none of them and changes nothing.
func (a *App) messagesPeerCmd(args []string) int {
	if len(args) == 0 || args[0] != "setup" {
		return a.messagesUsage("messages peer: the subcommand is setup")
	}
	const name = "messages peer setup"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	dir, _, code := a.externalCopy(name, fl, &cp, args[1:])
	if code >= 0 {
		return code
	}
	if fl.NArg() != 1 {
		return a.messagesUsage(name + " takes the peer's name")
	}
	cfg, err := config.LoadUnadmitted(filepath.Join(dir, "config.toml"))
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	peer, ok := cfg.Peer(fl.Arg(0))
	if !ok {
		return a.fail(fmt.Errorf("%s: %q is not a [[peer]] of this Director's config; its peers are %q", name, fl.Arg(0), cfg.PeerNames()))
	}
	return a.printJSON(a.peerSetupFor(cfg, dir, peer))
}

// peerSetupFor builds the setup of peer for the Director with config cfg in the copy dir.
func (a *App) peerSetupFor(cfg *config.Config, dir string, peer config.Peer) peerSetup {
	me := cfg.Name
	copyDir := filepath.ToSlash(dir)
	if abs, err := filepath.Abs(dir); err == nil {
		copyDir = filepath.ToSlash(abs)
	}
	out := peerSetup{Peer: peer.Name, Local: peer.Local()}
	entry := fmt.Sprintf("[[peer]]\nname = %q\nmachine = \"<this machine's Tailscale name>\"\nssh_alias = \"<the peer's SSH alias for this machine>\"\ncopy_dir = %q\n", me, copyDir)
	if peer.Local() {
		out.PeerConfig = fmt.Sprintf("[[peer]]\nname = %q\nmachine = %q\ncopy_dir = %q\n", me, peer.Machine, copyDir)
		out.Notes = []string{
			"The peer is on this machine and account, so messages to it are received in process, with no SSH and no key.",
			fmt.Sprintf("Add peer_config to %s/config.toml, so the peer takes this Director's messages.", peer.CopyDir),
		}
		return out
	}
	out.PeerConfig = entry
	key := "guildhall-" + me + "-to-" + peer.Name
	out.Key = key
	receive := strings.Join(messaging.PeerReceiveCommand(peer.CopyDir, me, peer.Name)[1:], " ")
	pub := "<the one line of ~/.ssh/" + key + ".pub on this machine>"
	exact := false
	home := a.getenv("HOME")
	if home == "" {
		home = a.getenv("USERPROFILE")
	}
	if home != "" {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", key+".pub")); err == nil {
			if line := strings.TrimSpace(string(b)); publicKeyLine.MatchString(line) {
				pub, exact = line, true
			}
		}
	}
	out.ForcedCommand = "<guildhall on the peer's account> " + receive
	if runtime.GOOS == "windows" {
		out.OS = "windows"
		out.ThisMachine = []string{
			fmt.Sprintf(`ssh-keygen -t ed25519 -C %s -f "$env:USERPROFILE\.ssh\%s"`, key, key),
			fmt.Sprintf("Add-Content -Encoding ascii -Path \"$env:USERPROFILE\\.ssh\\config\" -Value \"`nHost %s`n  HostName %s`n  IdentityFile ~/.ssh/%s`n  IdentitiesOnly yes\"", peer.SSHAlias, peer.Machine, key),
		}
	} else {
		out.OS = "linux"
		out.ThisMachine = []string{
			fmt.Sprintf("ssh-keygen -t ed25519 -N '' -C %s -f ~/.ssh/%s", key, key),
			fmt.Sprintf(`printf '\nHost %%s\n  HostName %%s\n  IdentityFile ~/.ssh/%%s\n  IdentitiesOnly yes\n' %s %s %s >> ~/.ssh/config`, peer.SSHAlias, peer.Machine, key),
		}
	}
	out.ThisMachine = append(out.ThisMachine, fmt.Sprintf("ssh -n -o BatchMode=yes %s true", peer.SSHAlias))
	out.PeerLinux = []string{
		"mkdir -p ~/.ssh && chmod 700 ~/.ssh",
		fmt.Sprintf(`printf '%%s\n' "command=\"$(command -v guildhall) %s\",restrict %s" >> ~/.ssh/authorized_keys`, receive, pub),
		"chmod 600 ~/.ssh/authorized_keys",
	}
	out.PeerWindows = []string{
		`New-Item -ItemType Directory -Force -Path "$env:USERPROFILE\.ssh" | Out-Null`,
		fmt.Sprintf(`Add-Content -Encoding ascii -Path "$env:USERPROFILE\.ssh\authorized_keys" -Value ('command="' + (Get-Command guildhall).Source + ' %s",restrict %s')`, receive, pub),
	}
	out.Notes = []string{
		fmt.Sprintf("On this machine, create the key and the SSH alias %s with this_machine, in order; the key has no passphrase, since a delivery cannot prompt (on Windows, press Enter twice at ssh-keygen's passphrase prompts).", peer.SSHAlias),
		fmt.Sprintf("HostName %s is the peer machine's Tailscale MagicDNS name; add a User line to the alias when the peer's account name differs from this one's, and a Port line when the peer's sshd listens elsewhere, as WSL's own sshd does beside its Windows side.", peer.Machine),
		"In the peer's account, a non-administrator account, run peer_linux or peer_windows, in a terminal where guildhall is on PATH: the line fixes the key to the receive command, so it can do nothing else, and its --from is this Director's name whatever a sender asks. Windows needs its OpenSSH Server already running, which an administrator installs once; a non-administrator's keys are read from %USERPROFILE%\\.ssh\\authorized_keys.",
		fmt.Sprintf("Add peer_config to %s/config.toml on the peer, with this machine's Tailscale name and the alias the peer would reach this Director by, so the peer takes this Director's messages.", peer.CopyDir),
		"WSL and its Windows side are two machines here: each has its own account, sshd, key and [[peer]] entry.",
		"The last this_machine command checks the key: the peer's forced command runs its receive with no message and prints a refused receive result, storing nothing.",
		"Guildhall runs none of these commands.",
	}
	if !exact {
		out.Notes = append(out.Notes, fmt.Sprintf("~/.ssh/%s.pub is not on this machine yet: once this_machine has created it, run guildhall messages peer setup %s again to print the peer's lines with the key in them.", key, peer.Name))
	}
	return out
}
