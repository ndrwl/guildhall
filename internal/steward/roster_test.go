package steward

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
)

const rosterFixture = `schema_version = 1

[[machine]]
label = "workstation"
ssh_alias = "guild-ws"
os = "linux"
roles = ["steward", "director"]

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/directors/tools"
repositories = ["cli", "site"]

[[machine.director]]
name = "docs"
copy_dir = "/home/guild/directors/docs"
repositories = ["handbook"]

[[machine]]
label = "laptop"
ssh_alias = "guild-laptop"
os = "windows"
roles = ["director"]

[[machine.director]]
name = "games"
copy_dir = "C:/Users/guild/directors/games"
repositories = ["engine"]
`

func TestParseRoster(t *testing.T) {
	r, err := ParseRoster([]byte(rosterFixture))
	if err != nil {
		t.Fatalf("valid roster refused: %v", err)
	}
	ws, ok := r.Machine("workstation")
	if !ok || ws.SSHAlias != "guild-ws" || ws.OS != OSLinux || len(ws.Directors) != 2 {
		t.Fatalf("workstation = %+v, %v", ws, ok)
	}
	if got := ws.Directors[1].ConfigPath(); got != "/home/guild/directors/docs/config.toml" {
		t.Errorf("ConfigPath = %q", got)
	}
	laptop, _ := r.Machine("laptop")
	if got := laptop.Directors[0].ConfigPath(); got != "C:/Users/guild/directors/games/config.toml" {
		t.Errorf("windows ConfigPath = %q", got)
	}
	if _, ok := r.Machine("absent"); ok {
		t.Error("absent machine found")
	}
}

func TestRosterSchemaRefusals(t *testing.T) {
	newer := strings.Replace(rosterFixture, "schema_version = 1", "schema_version = 2", 1)
	if _, err := ParseRoster([]byte(newer)); !errors.Is(err, config.ErrNewerSchema) {
		t.Fatalf("newer roster schema: %v", err)
	}
	unknown := strings.Replace(rosterFixture, `os = "linux"`, "os = \"linux\"\npassword_hint = \"x\"", 1)
	if _, err := ParseRoster([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := ParseRoster([]byte("schema_version = 0\n")); err == nil {
		t.Fatal("zero schema accepted")
	}
}

func TestRosterValidationRefusals(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"option-like alias", `ssh_alias = "guild-ws"`, `ssh_alias = "-oProxyCommand=sh"`, "invalid ssh_alias"},
		{"user in alias", `ssh_alias = "guild-ws"`, `ssh_alias = "guild@ws"`, "invalid ssh_alias"},
		{"shell in alias", `ssh_alias = "guild-ws"`, `ssh_alias = "ws;rm"`, "invalid ssh_alias"},
		{"duplicate alias", `ssh_alias = "guild-laptop"`, `ssh_alias = "guild-ws"`, `ssh_alias "guild-ws" is used by more than one machine`},
		{"duplicate label", `label = "laptop"`, `label = "workstation"`, `duplicate machine label "workstation"`},
		{"invalid label", `label = "laptop"`, `label = "Lap Top"`, "invalid machine label"},
		{"unknown os", `os = "windows"`, `os = "plan9"`, `os must be "linux", "windows" or "darwin"`},
		{"unknown role", `roles = ["director"]`, `roles = ["admin"]`, `unknown role "admin"`},
		{"no roles", `roles = ["director"]`, `roles = []`, "needs at least one role"},
		{"directors without role", `roles = ["director"]`, `roles = ["steward"]`, "lists directors but lacks the director role"},
		{"duplicate director", `name = "docs"`, `name = "tools"`, `duplicate director "tools"`},
		{"invalid director", `name = "docs"`, `name = "../docs"`, "invalid director name"},
		{"relative copy dir", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "home/guild/docs"`, "copy_dir"},
		{"parent segment", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "/home/guild/../docs"`, "copy_dir"},
		{"space in copy dir", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "/home/guild/my docs"`, "copy_dir"},
		{"quote in copy dir", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "/home/guild/x'y"`, "copy_dir"},
		{"shell in copy dir", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "/home/$(id)"`, "copy_dir"},
		{"windows path on linux", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "C:/guild/docs"`, "copy_dir"},
		{"backslash windows path", `copy_dir = "C:/Users/guild/directors/games"`, `copy_dir = 'C:\Users\guild\games'`, "copy_dir"},
		{"posix path on windows", `copy_dir = "C:/Users/guild/directors/games"`, `copy_dir = "/c/Users/guild"`, "copy_dir"},
		{"shared copy dir", `copy_dir = "/home/guild/directors/docs"`, `copy_dir = "/home/guild/directors/tools"`, "copy_dir is used by more than one director"},
		{"no repositories", `repositories = ["handbook"]`, `repositories = []`, "needs at least one repository"},
		{"invalid repository", `repositories = ["handbook"]`, `repositories = ["Hand Book"]`, "invalid repository id"},
		{"duplicate repository", `repositories = ["cli", "site"]`, `repositories = ["cli", "cli"]`, `duplicate repository "cli"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(rosterFixture, c.old) {
				t.Fatalf("fixture lacks %q", c.old)
			}
			_, err := ParseRoster([]byte(strings.Replace(rosterFixture, c.old, c.new, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}
