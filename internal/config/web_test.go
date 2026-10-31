package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWebTableParses: [web] parses its address and login; a config without it has no Web and encodes no web key, so
// its config digest is unchanged by the table's existence.
func TestWebTableParses(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + "\n[web]\naddress = \"andrew-lap-wsl.tail5da40a.ts.net:4388\"\nguildmaster = \"andrew@example.com\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Web{Address: "andrew-lap-wsl.tail5da40a.ts.net:4388", Guildmaster: "andrew@example.com"}
	if cfg.Web == nil || *cfg.Web != want {
		t.Fatalf("Web = %+v, want %+v", cfg.Web, want)
	}
	// The login is optional until answering arrives.
	if cfg, err := Parse([]byte(director(t) + "\n[web]\naddress = \"100.101.102.103:4388\"\n")); err != nil || cfg.Web.Guildmaster != "" {
		t.Fatalf("no login: %+v, %v", cfg, err)
	}
	cfg, err = Parse([]byte(director(t)))
	if err != nil || cfg.Web != nil {
		t.Fatalf("no table: Web = %+v, %v; want nil", cfg.Web, err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"Web"`) {
		t.Fatalf("a config without [web] encodes a Web key, which would change its digest: %s", data)
	}
}

func TestInvalidWebTableRefused(t *testing.T) {
	cases := map[string]string{
		"guildmaster = \"a@b\"\n":                          `web: address is required`,
		"address = \"andrew-lap-wsl\"\n":                   `web: address "andrew-lap-wsl" must be a host and port`,
		"address = \":4388\"\n":                            `web: address ":4388" needs a host`,
		"address = \"host.ts.net:0\"\n":                    `needs a port from 1 to 65535`,
		"address = \"host.ts.net:99999\"\n":                `needs a port from 1 to 65535`,
		"address = \"host.ts.net:http\"\n":                 `needs a port from 1 to 65535`,
		"address = \"h:1\"\nguildmaster = \"a b\"\n":       `web: guildmaster "a b" must be a Tailscale login`,
		"address = \"h:1\"\nguildmaster = \"a\\u0007b\"\n": `must be a Tailscale login`,
		"address = \"h:1\"\nport = 4388\n":                 `unknown keys: web.port`,
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + "\n[web]\n" + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	steward := "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"git@github.com:example/c.git\"\n\n[web]\naddress = \"h:1\"\n"
	if _, err := Parse([]byte(steward)); err == nil || !strings.Contains(err.Error(), "the web table belongs to a director, not the steward") {
		t.Errorf("steward web table: err = %v", err)
	}
}
