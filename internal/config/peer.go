package config

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// Peer is one [[peer]] entry: another Director this Director sends Director messages to and receives them from
// (docs/messaging.md#director-to-director-messages). Peers are written by hand in a copy's own config.toml, in both
// modes, since managed settings cannot carry a table.
type Peer struct {
	// Name is the peer Director's name, its config's name; it is the sender this copy records for the peer's messages
	// and the name its own messages carry in "to".
	Name string `toml:"name"`
	// Machine labels the machine the peer runs on, such as "vds"; with an SSH alias, its Tailscale MagicDNS name,
	// which `messages peer setup` writes as the alias's HostName.
	Machine string `toml:"machine"`
	// SSHAlias is the Host in this account's SSH config that reaches the peer's account; empty for a peer on this
	// machine and account, which receives locally with no SSH.
	SSHAlias string `toml:"ssh_alias"`
	// CopyDir is the peer's copy directory on its machine, an absolute path.
	CopyDir string `toml:"copy_dir"`
}

// Local reports whether the peer is on this machine and account, reached with no SSH.
func (p Peer) Local() bool { return p.SSHAlias == "" }

// reservedPeerNames are the sender words no peer may be named, so a peer's messages never pass for another sender's.
// It matches store.ReservedSenderNames, which config cannot import.
var reservedPeerNames = []string{"guildhall", "artisan", "director", "other-director", "guildmaster", "steward", "unknown", "local", "page"}

// The roster's character rules (internal/steward), which peers follow: a plain SSH Host, and an absolute POSIX or
// Windows drive path of letters, digits, '.', '_', '-' and '/'.
var (
	peerAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	peerPosixDir     = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	peerWindowsDir   = regexp.MustCompile(`^[A-Za-z]:(/[A-Za-z0-9._-]+)+$`)
)

// Peer returns the [[peer]] entry named name.
func (c *Config) Peer(name string) (Peer, bool) {
	for _, p := range c.Peers {
		if p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

// PeerNames returns the [[peer]] names in config order.
func (c *Config) PeerNames() []string {
	out := make([]string, len(c.Peers))
	for i, p := range c.Peers {
		out[i] = p.Name
	}
	return out
}

// checkPeers checks the [[peer]] entries of a config in mode, named name.
func checkPeers(peers []Peer, mode Mode, name string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if len(peers) > 0 && mode != ModeDirector {
		return []string{"peers belong to a director, not the steward"}
	}
	seen := map[string]bool{}
	for i, p := range peers {
		label := fmt.Sprintf("peer %d", i+1)
		switch {
		case !workflow.ValidID(p.Name):
			add("%s: name %q must be a Director name: lowercase letters, digits and hyphens, starting with a letter", label, p.Name)
		case slices.Contains(reservedPeerNames, p.Name):
			add("%s: name %q is a reserved sender word", label, p.Name)
		case p.Name == name:
			add("%s: name %q is this Director's own name", label, p.Name)
		case seen[p.Name]:
			add("duplicate peer %q", p.Name)
		default:
			label = fmt.Sprintf("peer %q", p.Name)
		}
		seen[p.Name] = true
		if !peerAliasPattern.MatchString(p.Machine) {
			add("%s: machine %q must be 1 to 63 letters, digits, '.', '_' and '-'", label, p.Machine)
		}
		if p.SSHAlias != "" && !peerAliasPattern.MatchString(p.SSHAlias) {
			add("%s: ssh_alias %q must be a plain Host from your SSH config", label, p.SSHAlias)
		}
		if !peerPosixDir.MatchString(p.CopyDir) && !peerWindowsDir.MatchString(p.CopyDir) {
			add("%s: copy_dir %q must be an absolute path of letters, digits, '.', '_', '-' and '/', such as /home/guild/directors/web or C:/Users/guild/directors/web", label, p.CopyDir)
		}
	}
	return problems
}
