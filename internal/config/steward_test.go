package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// stewardConfig is a minimal valid Steward config.
const stewardConfig = "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"/home/guild/guildhall-config\"\n"

// TestStewardTableParses: [steward] names the session's workspace and runtime in a Steward config; a Steward config
// that only runs rollouts needs none, and the name and adapter are required to start a session, not to parse.
func TestStewardTableParses(t *testing.T) {
	cfg, err := Parse([]byte(stewardConfig + "\n[steward]\nname = \"steward\"\nadapter = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\nrole = \"roles/steward.md\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Steward{Name: "steward", Adapter: "claude", Model: "opus", Effort: "high", Role: "roles/steward.md"}
	if cfg.Steward == nil || *cfg.Steward != want {
		t.Fatalf("Steward = %+v, want %+v", cfg.Steward, want)
	}
	if cfg, err := Parse([]byte(stewardConfig)); err != nil || cfg.Steward != nil {
		t.Fatalf("no table: Steward = %+v, %v; want nil", cfg, err)
	}
	if cfg, err := Parse([]byte(stewardConfig + "\n[steward]\nmodel = \"opus\"\n")); err != nil || cfg.Steward.Name != "" || cfg.Steward.Adapter != "" {
		t.Fatalf("table without name or adapter: %+v, %v", cfg, err)
	}
}

// TestStewardTableBelongsToTheSteward: [steward] is refused in a Director config, [director] in a Steward config,
// and a malformed [steward] is refused.
func TestStewardTableBelongsToTheSteward(t *testing.T) {
	if _, err := Parse([]byte(director(t) + "\n[steward]\nname = \"steward\"\n")); err == nil || !strings.Contains(err.Error(), "the steward table belongs to the steward, not a director") {
		t.Errorf("director with [steward]: err = %v", err)
	}
	if _, err := Parse([]byte(stewardConfig + "\n[director]\nadapter = \"claude\"\n")); err == nil || !strings.Contains(err.Error(), "the director table belongs to a director, not the steward") {
		t.Errorf("steward with [director]: err = %v", err)
	}
	role := `steward: role must be a relative slash-separated path inside the config directory`
	for body, want := range map[string]string{
		"name = \"The Steward\"\n":                  `steward: invalid name "The Steward"`,
		"adapter = \"Claude Code\"\n":               `steward: invalid adapter "Claude Code"`,
		"role = \"/etc/steward.md\"\n":              role,
		"role = \"../steward.md\"\n":                role,
		"role = 'roles\\steward.md'\n":              role,
		"role = 'C:/steward.md'\n":                  role,
		"name = \"steward\"\ntoken = 1\n":           `unknown keys: steward.token`,
		"name = \"steward\"\nidle_after = \"5m\"\n": `unknown keys: steward.idle_after`,
	} {
		if _, err := Parse([]byte(stewardConfig + "\n[steward]\n" + body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}

// TestStewardRunsTheDefaultSessionSettings: the Steward's session runs the default idle_after, handoff triggers and
// wake_quiet (docs/steward.md#lifecycle), so a Steward config refuses each of them under [steward] and
// wake_quiet in [local] or [managed]; the [director] table that holds the others is refused by
// TestStewardTableBelongsToTheSteward.
func TestStewardRunsTheDefaultSessionSettings(t *testing.T) {
	for _, body := range []string{`idle_after = "5m"`, `handoff_after_tasks = 3`, `handoff_context_percent = 50`, `handoff_idle = "1h"`, `wake_quiet = "3s"`} {
		key, _, _ := strings.Cut(body, " ")
		if _, err := Parse([]byte(stewardConfig + "\n[steward]\nname = \"steward\"\n" + body + "\n")); err == nil || !strings.Contains(err.Error(), "unknown keys: steward."+key) {
			t.Errorf("[steward] %s: err = %v", body, err)
		}
	}
	want := "wake_quiet is a director setting, not the steward's"
	for _, table := range []string{"[local]\nwake_quiet = \"3s\"\n", "[managed.wake_quiet]\nvalue = \"3s\"\n"} {
		if _, err := Parse([]byte(stewardConfig + "\n" + table)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", table, err, want)
		}
	}
	if _, err := Parse([]byte(stewardConfig + "\n[local]\nwake_max_delay = \"30s\"\n")); err != nil {
		t.Errorf("[local] wake_max_delay refused: %v", err)
	}
}

// TestStewardTableKeepsTheDigest: a config without [steward] digests as before the table existed, since the table
// is omitted from JSON when unset.
func TestStewardTableKeepsTheDigest(t *testing.T) {
	cfg, err := Parse([]byte(stewardConfig))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Steward") {
		t.Fatalf("config JSON without [steward] = %s", b)
	}
}
