package config

import (
	"strings"
	"testing"
	"time"
)

func TestDirectorTableParses(t *testing.T) {
	cfg, err := Parse([]byte(director(t) + "\n[director]\nadapter = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\nrole = \"roles/director.md\"\nidle_after = \"90s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Director{Adapter: "claude", Model: "opus", Effort: "high", Role: "roles/director.md", IdleAfter: "90s"}
	if cfg.Director == nil || *cfg.Director != want {
		t.Fatalf("Director = %+v, want %+v", cfg.Director, want)
	}
	if d, err := cfg.IdleAfter(); err != nil || d != 90*time.Second {
		t.Fatalf("IdleAfter = %v, %v; want 90s", d, err)
	}
	cfg, err = Parse([]byte(director(t)))
	if err != nil || cfg.Director != nil {
		t.Fatalf("no table: Director = %+v, %v; want nil", cfg.Director, err)
	}
	if d, err := cfg.IdleAfter(); err != nil || d != DefaultIdleAfter {
		t.Fatalf("no table: IdleAfter = %v, %v; want the default", d, err)
	}
	// The adapter is required to start a session, not to parse a config.
	if cfg, err := Parse([]byte(director(t) + "\n[director]\nmodel = \"opus\"\n")); err != nil || cfg.Director.Adapter != "" {
		t.Fatalf("table without adapter: %+v, %v", cfg, err)
	}
}

func TestInvalidDirectorTableRefused(t *testing.T) {
	role := `director: role must be a relative slash-separated path inside the config directory`
	cases := map[string]string{
		"adapter = \"Claude Code\"\n":       `director: invalid adapter "Claude Code"`,
		"role = \"/etc/director.md\"\n":     role,
		"role = \"../director.md\"\n":       role,
		"role = 'roles\\director.md'\n":     role,
		"role = 'C:/director.md'\n":         role,
		"adapter = \"claude\"\ntoken = 1\n": `unknown keys: director.token`,
		"idle_after = \"soon\"\n":           `director: idle_after "soon" must be a positive duration`,
		"idle_after = \"0s\"\n":             `director: idle_after "0s" must be a positive duration`,
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + "\n[director]\n" + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	steward := "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"git@github.com:example/c.git\"\n\n[director]\nadapter = \"claude\"\n"
	if _, err := Parse([]byte(steward)); err == nil || !strings.Contains(err.Error(), "the director table belongs to a director, not the steward") {
		t.Errorf("steward director table: err = %v", err)
	}
}
