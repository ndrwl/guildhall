// Package webtest holds the task page's test doubles and the contract they share with the real implementations
// (docs/contracts/web.md).
package webtest

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/ndrwl/guildhall/internal/web"
)

// Logins is the fake web.Identifier: a fixed login for each tailnet node, keyed by its IP. Like web.Whois it
// refuses with web.ErrNoIdentity an address that does not parse, lies outside the tailnet or has no node.
type Logins map[string]string

// Login implements web.Identifier.
func (l Logins) Login(_ context.Context, remote string) (string, error) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return "", fmt.Errorf("%w: remote address %q: %v", web.ErrNoIdentity, remote, err)
	}
	if !web.InTailnet(ap.Addr()) {
		return "", fmt.Errorf("%w: remote address %s is not on the tailnet", web.ErrNoIdentity, ap.Addr())
	}
	login, ok := l[ap.Addr().Unmap().String()]
	if !ok || login == "" {
		return "", fmt.Errorf("%w: no node at %s", web.ErrNoIdentity, ap.Addr())
	}
	return login, nil
}

// RunIdentifier is the web.Identifier contract: each address of known, an "ip:port" on the tailnet, gives its
// login; unknown, a tailnet "ip:port" with no node, and addresses that do not parse or lie outside the tailnet are
// refused with an error matching web.ErrNoIdentity and no login.
func RunIdentifier(t *testing.T, id web.Identifier, known map[string]string, unknown string) {
	t.Helper()
	ctx := context.Background()
	for remote, want := range known {
		t.Run("known "+remote, func(t *testing.T) {
			got, err := id.Login(ctx, remote)
			if err != nil || got != want {
				t.Fatalf("Login(%q) = %q, %v; want %q", remote, got, err, want)
			}
		})
	}
	for name, remote := range map[string]string{
		"unknown node":  unknown,
		"loopback":      "127.0.0.1:4388",
		"private LAN":   "192.168.1.20:51000",
		"public IPv6":   "[2001:db8::1]:443",
		"no port":       "100.101.102.103",
		"not an addr":   "andrew-lap-wsl:4388",
		"empty":         "",
		"option-shaped": "--json",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := id.Login(ctx, remote)
			if !errors.Is(err, web.ErrNoIdentity) || got != "" {
				t.Fatalf("Login(%q) = %q, %v; want ErrNoIdentity and no login", remote, got, err)
			}
		})
	}
}
