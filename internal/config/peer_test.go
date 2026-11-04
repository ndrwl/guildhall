package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const peerVDS = "\n[[peer]]\nname = \"web\"\nmachine = \"vds\"\nssh_alias = \"guild-vds\"\ncopy_dir = \"/home/guild/directors/web\"\n"

// TestPeersParse: [[peer]] entries parse in order, a peer with no ssh_alias is on this machine and account, and peers
// are never encoded, so adding one leaves the config digest unchanged.
func TestPeersParse(t *testing.T) {
	base := director(t)
	plain, err := Parse([]byte(base))
	if err != nil || len(plain.Peers) != 0 {
		t.Fatalf("no peers: %+v, %v", plain, err)
	}
	cfg, err := Parse([]byte(base + peerVDS + "\n[[peer]]\nname = \"games\"\nmachine = \"laptop-win\"\nssh_alias = \"guild-win\"\ncopy_dir = \"C:/Users/guild/directors/games\"\n" +
		"\n[[peer]]\nname = \"tools\"\nmachine = \"laptop\"\ncopy_dir = \"/home/guild/directors/tools\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.PeerNames(), ","); got != "web,games,tools" {
		t.Fatalf("peers %q", got)
	}
	web, ok := cfg.Peer("web")
	if !ok || web != (Peer{Name: "web", Machine: "vds", SSHAlias: "guild-vds", CopyDir: "/home/guild/directors/web"}) || web.Local() {
		t.Fatalf("web: %+v, %v", web, ok)
	}
	if tools, _ := cfg.Peer("tools"); !tools.Local() {
		t.Errorf("tools, with no ssh_alias, is not local")
	}
	if _, ok := cfg.Peer("nobody"); ok {
		t.Errorf("found a peer that is not listed")
	}
	a, _ := json.Marshal(plain)
	b, _ := json.Marshal(cfg)
	if !bytes.Equal(a, b) {
		t.Fatalf("adding peers changed the config's JSON, and so its digest:\n%s\n%s", a, b)
	}
}

// TestInvalidPeersRefused: a peer's name is a Director name that is no reserved sender word, not this Director's own
// and not repeated, and its machine, ssh_alias and copy_dir follow the roster's character rules.
func TestInvalidPeersRefused(t *testing.T) {
	entry := func(name, machine, alias, dir string) string {
		return "\n[[peer]]\nname = \"" + name + "\"\nmachine = \"" + machine + "\"\nssh_alias = \"" + alias + "\"\ncopy_dir = \"" + dir + "\"\n"
	}
	cases := map[string]string{
		entry("Web", "vds", "a", "/d"):                       "must be a Director name",
		entry("steward", "vds", "a", "/d"):                   "reserved sender word",
		entry("guildmaster", "vds", "a", "/d"):               "reserved sender word",
		entry("other-director", "vds", "a", "/d"):            "reserved sender word",
		entry("alpha", "vds", "a", "/d"):                     "this Director's own name",
		peerVDS + peerVDS:                                    `duplicate peer "web"`,
		entry("web", "", "a", "/d"):                          "machine",
		entry("web", "vds", "user@host", "/d"):               "ssh_alias",
		entry("web", "vds", "-oProxyCommand=x", "/d"):        "ssh_alias",
		entry("web", "vds", "a", "relative/dir"):             "copy_dir",
		entry("web", "vds", "a", "/home/guild/my directors"): "copy_dir",
		entry("web", "vds", "a", `C:\Users\guild`):           "copy_dir",
		entry("web", "vds", "a", "/d") + "port = 22\n":       "unknown keys",
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	steward := "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"git@github.com:example/c.git\"\n" + peerVDS
	if _, err := Parse([]byte(steward)); err == nil || !strings.Contains(err.Error(), "peers belong to a director") {
		t.Errorf("steward peers: err = %v", err)
	}
}
