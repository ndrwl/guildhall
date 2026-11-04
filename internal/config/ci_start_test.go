package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCIStartGrace: ci_start_grace is a duration setting, local or managed, defaulting to ten minutes.
func TestCIStartGrace(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 time.Duration
	}{
		{name: "unset is the default", want: DefaultCIStartGrace},
		{name: "a local duration", local: `ci_start_grace = "15m"`, want: 15 * time.Minute},
		{name: "zero reports at the first read", local: `ci_start_grace = "0s"`, want: 0},
		{name: "a managed duration", managed: "[managed.ci_start_grace]\nvalue = \"20m\"\n", want: 20 * time.Minute},
		{name: "an overridable managed duration replaced locally", local: `ci_start_grace = "5m"`,
			managed: "[managed.ci_start_grace]\nvalue = \"20m\"\noverridable = true\n", want: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.CIStartGrace(); got != tc.want || err != nil {
				t.Errorf("CIStartGrace() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultCIStartGrace != 10*time.Minute {
		t.Errorf("DefaultCIStartGrace = %v, want 10m", DefaultCIStartGrace)
	}
}

func TestCIStartGraceRefusesAnInvalidDuration(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `ci_start_grace = "soon"`,
		`[local]` + "\n" + `ci_start_grace = "-1m"`,
		`[local]` + "\n" + `ci_start_grace = "600"`,
		"[managed.ci_start_grace]\nvalue = \"later\"\n[local]",
		"[managed.ci_start_grace]\nvalue = \"-5m\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "ci_start_grace") {
			t.Errorf("%q: Parse error %v, want one naming ci_start_grace", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{CIStartGraceKey: "soon"}}
	if _, err := cfg.CIStartGrace(); err == nil {
		t.Error("CIStartGrace of an unparsed invalid value succeeded")
	}
}

// TestNoCIWorkflowKey: a missing run is re-triggered through the pull request, not a dispatched workflow, so there
// is no ci_workflow repository key; it is refused as unknown, and the config's JSON has no such field.
func TestNoCIWorkflowKey(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(director(t), "no_ci = true\n", "no_ci = true\nci_workflow = \"ci.yml\"\n", 1)))
	if err == nil || !strings.Contains(err.Error(), "unknown keys") || !strings.Contains(err.Error(), "ci_workflow") {
		t.Fatalf("ci_workflow: err = %v, want an unknown key", err)
	}
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"CIWorkflow"`) {
		t.Fatalf("config JSON = %s, want no CIWorkflow", b)
	}
}

// TestCIRetrigger: ci_retrigger is a repository key, off by default, taking off or reopen; anything else is refused,
// and a config without it has no CIRetrigger in its JSON, so its digest is unchanged.
func TestCIRetrigger(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Repositories[1].CIRetriggerPolicy(); got != CIRetriggerOff {
		t.Errorf("unset CIRetriggerPolicy = %q, want off", got)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"CIRetrigger"`) {
		t.Fatalf("config JSON without ci_retrigger = %s", b)
	}
	for _, good := range []CIRetrigger{CIRetriggerOff, CIRetriggerReopen} {
		cfg, err := Parse([]byte(strings.Replace(director(t), "no_ci = true\n", "no_ci = true\nci_retrigger = \""+string(good)+"\"\n", 1)))
		if err != nil {
			t.Fatalf("%q: %v", good, err)
		}
		if got := cfg.Repositories[1].CIRetriggerPolicy(); got != good {
			t.Errorf("CIRetriggerPolicy = %q, want %q", got, good)
		}
	}
	for _, bad := range []string{"on", "true", "Reopen", "dispatch", " reopen"} {
		_, err := Parse([]byte(strings.Replace(director(t), "no_ci = true\n", "no_ci = true\nci_retrigger = \""+bad+"\"\n", 1)))
		if err == nil || !strings.Contains(err.Error(), `repository "site": ci_retrigger "`+bad+`" must be off or reopen`) {
			t.Errorf("ci_retrigger = %q: err = %v, want it refused", bad, err)
		}
	}
}
