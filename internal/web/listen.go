package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// tailnet holds Tailscale's address ranges: the CGNAT IPv4 range and Tailscale's IPv6 ULA prefix.
var tailnet = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// ErrNotTailnet is the refusal of an address that resolves to anything outside Tailscale's ranges.
var ErrNotTailnet = errors.New("not a Tailscale address")

// InTailnet reports whether ip lies in Tailscale's ranges, 100.64.0.0/10 or fd7a:115c:a1e0::/48.
func InTailnet(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range tailnet {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Lookup resolves a host to its addresses, as net.Resolver.LookupNetIP does.
type Lookup func(ctx context.Context, host string) ([]netip.Addr, error)

// BindAddress resolves address, a host and port such as "andrew-lap-wsl.tail5da40a.ts.net:4388", with lookup, and
// returns the address to bind: its first resolved address with the port. It fails closed: an address with no host,
// no port or a host that does not resolve is refused, and so is a host any of whose resolved addresses lies outside
// Tailscale's ranges, with ErrNotTailnet naming it.
func BindAddress(ctx context.Context, address string, lookup Lookup) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("web address %q: %w", address, err)
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("web address %q needs a host and a port", address)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = lookup(ctx, host); err != nil {
		return "", fmt.Errorf("web address %q: %w", address, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("web address %q resolves to no address", address)
	}
	for _, ip := range ips {
		if !InTailnet(ip) {
			return "", fmt.Errorf("web address %q resolves to %s: %w (100.64.0.0/10, fd7a:115c:a1e0::/48), so nothing is served", address, ip, ErrNotTailnet)
		}
	}
	return net.JoinHostPort(ips[0].Unmap().String(), port), nil
}

// Listen binds address on the tailnet: BindAddress with the system resolver, then a TCP listener on the address it
// returns. A refused address binds nothing.
func Listen(address string) (net.Listener, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bind, err := BindAddress(ctx, address, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	})
	if err != nil {
		return nil, err
	}
	return net.Listen("tcp", bind)
}

// URL is the task page's link under address: http://<address>/<repo>/<task>.
func URL(address, repo, task string) string {
	return "http://" + address + "/" + pathEscape(repo) + "/" + pathEscape(task)
}

// Running is a server serving pages on one listener until Close.
type Running struct {
	srv  *http.Server
	done chan struct{}
}

// Serve serves h on ln in the background, with bounded header, read and write times, until Close.
func Serve(ln net.Listener, h http.Handler) *Running {
	r := &Running{srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		_ = r.srv.Serve(ln)
	}()
	return r
}

// Close stops serving: it lets requests in flight finish for up to five seconds, then closes their connections,
// and returns once the server has stopped. A nil Running, or one already closed, closes nothing.
func (r *Running) Close() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.srv.Shutdown(ctx); err != nil {
		_ = r.srv.Close()
	}
	<-r.done
}
