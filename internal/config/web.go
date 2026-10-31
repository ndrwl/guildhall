package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxWebLoginBytes bounds [web] guildmaster.
const MaxWebLoginBytes = 256

// checkWeb checks a [web] table: an address with a host and a port from 1 to 65535, and a guildmaster login, when
// set, of printable characters with no space, at most MaxWebLoginBytes long. Whether the host resolves only to
// Tailscale addresses is checked when the server binds, not here, so a config parses with no network.
func checkWeb(w Web) []string {
	var problems []string
	host, port, err := net.SplitHostPort(w.Address)
	switch {
	case strings.TrimSpace(w.Address) == "":
		problems = append(problems, `web: address is required, a host and port such as "<machine>.<tailnet>.ts.net:4388"`)
	case err != nil:
		problems = append(problems, fmt.Sprintf("web: address %q must be a host and port: %v", w.Address, err))
	case host == "" || strings.ContainsAny(host, " \t/"):
		problems = append(problems, fmt.Sprintf("web: address %q needs a host", w.Address))
	default:
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			problems = append(problems, fmt.Sprintf("web: address %q needs a port from 1 to 65535", w.Address))
		}
	}
	if g := w.Guildmaster; g != "" {
		ok := len(g) <= MaxWebLoginBytes && utf8.ValidString(g)
		for _, r := range g {
			if unicode.IsSpace(r) || !unicode.IsPrint(r) {
				ok = false
			}
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("web: guildmaster %q must be a Tailscale login: printable, with no spaces, at most %d bytes", g, MaxWebLoginBytes))
		}
	}
	return problems
}
