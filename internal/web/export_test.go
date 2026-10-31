package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// FixtureToken is the form token of AnsweringFixture's server, fixed so its page is a stable golden file.
const FixtureToken = "fixture-token"

// FixtureAddress is AnsweringFixture's configured [web] address.
const FixtureAddress = "andrew-lap-wsl.tail5da40a.ts.net:4388"

// AnsweringFixture is the fixture's server at FixtureAddress answering for the login guildmaster, told by id,
// through a.
func AnsweringFixture(t *testing.T, id Identifier, guildmaster string, a Answerer) *Server {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	src := &fakeSource{tasks: map[string]Task{"guildhall/d122": fixtureTask(sums)}}
	s := New(Options{Source: src, Documents: dir, Location: time.UTC, Host: FixtureAddress, Identity: id, Guildmaster: guildmaster,
		Answerer: a})
	s.token = FixtureToken
	return s
}

// UpdateGolden reports whether the test run rewrites its golden files.
func UpdateGolden() bool { return *update }

// TailscaleSelf is this machine's own tailnet address, as an "ip:port", and the login tailscale status names for
// it; ok is false when tailscale is missing or not up.
func TailscaleSelf(t *testing.T) (string, string, bool) {
	t.Helper()
	if _, err := exec.LookPath("tailscale"); err != nil {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", "", false
	}
	var st struct {
		BackendState string
		Self         *struct {
			TailscaleIPs []string
			UserID       int64
		}
		User map[string]struct{ LoginName string }
	}
	if json.NewDecoder(bytes.NewReader(out)).Decode(&st) != nil || st.BackendState != "Running" || st.Self == nil || len(st.Self.TailscaleIPs) == 0 {
		return "", "", false
	}
	u, ok := st.User[strconv.FormatInt(st.Self.UserID, 10)]
	if !ok || u.LoginName == "" {
		return "", "", false
	}
	return net.JoinHostPort(st.Self.TailscaleIPs[0], "1"), u.LoginName, true
}
