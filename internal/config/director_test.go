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

// TestDirectorHandoffTriggers: the three triggers parse, default to the context trigger at 70% and the others
// off, take 0 as off and refuse values out of range.
func TestDirectorHandoffTriggers(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if h, err := cfg.Handoff(); err != nil || h != (Handoff{ContextPercent: DefaultHandoffContextPercent}) || DefaultHandoffContextPercent != 70 {
		t.Fatalf("no table: Handoff = %+v, %v; want only the context trigger at 70%%", h, err)
	}
	cfg, err = Parse([]byte(director(t) + "\n[director]\nhandoff_after_tasks = 3\nhandoff_context_percent = 85\nhandoff_idle = \"30m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if h, err := cfg.Handoff(); err != nil || h != (Handoff{AfterTasks: 3, ContextPercent: 85, Idle: 30 * time.Minute}) {
		t.Fatalf("Handoff = %+v, %v", h, err)
	}
	cfg, err = Parse([]byte(director(t) + "\n[director]\nhandoff_context_percent = 0\nhandoff_idle = \"0s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if h, err := cfg.Handoff(); err != nil || h != (Handoff{}) {
		t.Fatalf("all off: Handoff = %+v, %v; want every trigger off", h, err)
	}
	cases := map[string]string{
		"handoff_after_tasks = -1\n":        `director: handoff_after_tasks -1 must not be negative`,
		"handoff_context_percent = 101\n":   `director: handoff_context_percent 101 must be between 0 and 100`,
		"handoff_context_percent = -1\n":    `director: handoff_context_percent -1 must be between 0 and 100`,
		"handoff_idle = \"later\"\n":        `director: handoff_idle "later" must be a duration such as "30m", or "0s" for off`,
		"handoff_idle = \"-5m\"\n":          `director: handoff_idle "-5m" must be a duration such as "30m", or "0s" for off`,
		"handoff_after_tasks = \"three\"\n": `handoff_after_tasks`,
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + "\n[director]\n" + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}

// TestDirectorMaintenanceCadences: the two cadences parse, default to a note audit every 3 sessions and a
// hallmark after 4 merges, take 0 as off and refuse negative values.
func TestDirectorMaintenanceCadences(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := cfg.Maintenance(); err != nil || m != (Maintenance{NoteAuditEverySessions: 3, HallmarkAfterMerges: 4}) {
		t.Fatalf("no table: Maintenance = %+v, %v; want 3 sessions and 4 merges", m, err)
	}
	cfg, err = Parse([]byte(director(t) + "\n[director]\nnote_audit_every_sessions = 5\nhallmark_after_merges = 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := cfg.Maintenance(); err != nil || m != (Maintenance{NoteAuditEverySessions: 5, HallmarkAfterMerges: 2}) {
		t.Fatalf("Maintenance = %+v, %v", m, err)
	}
	cfg, err = Parse([]byte(director(t) + "\n[director]\nnote_audit_every_sessions = 0\nhallmark_after_merges = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := cfg.Maintenance(); err != nil || m != (Maintenance{}) {
		t.Fatalf("all off: Maintenance = %+v, %v; want both off", m, err)
	}
	cases := map[string]string{
		"note_audit_every_sessions = -1\n": `director: note_audit_every_sessions -1 must not be negative`,
		"hallmark_after_merges = -2\n":     `director: hallmark_after_merges -2 must not be negative`,
		"hallmark_after_merges = \"4\"\n":  `hallmark_after_merges`,
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + "\n[director]\n" + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}
