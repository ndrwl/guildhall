package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// ErrNoIdentity is an Identifier's answer when it cannot tell which Tailscale login sent a request: the address is
// not a tailnet address, tailscale is missing or failed, or the node has no login.
var ErrNoIdentity = errors.New("no Tailscale identity for the request")

// Identifier tells which Tailscale login sent a request, from the request's remote address, an "ip:port" on the
// tailnet. Login returns the login, such as "someone@example.com", or an error matching ErrNoIdentity when it
// cannot tell: an address that does not parse or lies outside Tailscale's ranges is refused without asking anyone.
// It is the task page's only identity check; the page answers only for the configured Guildmaster's login
// (docs/web.md#answering). webtest.RunIdentifier is the contract every implementation passes.
type Identifier interface {
	Login(ctx context.Context, remote string) (string, error)
}

// whoisTimeout bounds one tailscale whois call.
const whoisTimeout = 5 * time.Second

// Whois is the real Identifier: it runs `tailscale whois --json <remote>` and reads the login of the user who owns
// the node at that address.
type Whois struct {
	// Command is the tailscale command and its leading arguments; nil means "tailscale" on PATH.
	Command []string
}

// whoisReply is the part of `tailscale whois --json` the page reads.
type whoisReply struct {
	UserProfile *struct {
		LoginName string `json:"LoginName"`
	} `json:"UserProfile"`
}

// Login implements Identifier.
func (w Whois) Login(ctx context.Context, remote string) (string, error) {
	if _, err := tailnetRemote(remote); err != nil {
		return "", err
	}
	cmd := w.Command
	if len(cmd) == 0 {
		cmd = []string{"tailscale"}
	}
	ctx, cancel := context.WithTimeout(ctx, whoisTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], append(cmd[1:len(cmd):len(cmd)], "whois", "--json", remote)...) //nolint:gosec // the configured tailscale command, with an address checked to be on the tailnet
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%w: tailscale whois %s: %v %s", ErrNoIdentity, remote, err, strings.TrimSpace(errOut.String()))
	}
	var r whoisReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return "", fmt.Errorf("%w: tailscale whois %s: %v", ErrNoIdentity, remote, err)
	}
	if r.UserProfile == nil || strings.TrimSpace(r.UserProfile.LoginName) == "" {
		return "", fmt.Errorf("%w: tailscale whois %s names no login", ErrNoIdentity, remote)
	}
	return r.UserProfile.LoginName, nil
}

// tailnetRemote parses remote, an "ip:port", and refuses it with ErrNoIdentity unless its address is on the tailnet.
func tailnetRemote(remote string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("%w: remote address %q: %v", ErrNoIdentity, remote, err)
	}
	if !InTailnet(ap.Addr()) {
		return netip.AddrPort{}, fmt.Errorf("%w: remote address %s is not on the tailnet", ErrNoIdentity, ap.Addr())
	}
	return ap, nil
}
