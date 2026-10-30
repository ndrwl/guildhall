package config

import (
	"strings"
	"testing"
	"time"
)

func TestPruneSettings(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		after                time.Duration
		daily                bool
	}{
		{name: "unset is the default", after: DefaultPruneAfter},
		{name: "a local delay", local: `prune_after = "48h"`, after: 48 * time.Hour},
		{name: "zero prunes at close", local: `prune_after = "0"`, after: 0},
		{name: "zero seconds prunes at close", local: `prune_after = "0s"`, after: 0},
		{name: "a managed delay", managed: "[managed.prune_after]\nvalue = \"720h\"\n", after: 720 * time.Hour},
		{name: "daily on", local: `prune_daily = "true"`, after: DefaultPruneAfter, daily: true},
		{name: "daily off", local: `prune_daily = "false"`, after: DefaultPruneAfter},
		{name: "a managed daily on", managed: "[managed.prune_daily]\nvalue = \"true\"\n", after: DefaultPruneAfter, daily: true},
		{name: "an overridable managed daily replaced locally", local: `prune_daily = "false"`,
			managed: "[managed.prune_daily]\nvalue = \"true\"\noverridable = true\n", after: DefaultPruneAfter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.PruneAfter(); got != tc.after || err != nil {
				t.Errorf("PruneAfter() = %v, %v; want %v", got, err, tc.after)
			}
			if got, err := cfg.PruneDaily(); got != tc.daily || err != nil {
				t.Errorf("PruneDaily() = %v, %v; want %v", got, err, tc.daily)
			}
		})
	}
	if DefaultPruneAfter != 336*time.Hour {
		t.Errorf("DefaultPruneAfter = %v, want 336h", DefaultPruneAfter)
	}
}

func TestPruneSettingsRefuseInvalidValues(t *testing.T) {
	for key, bad := range map[string][]string{
		PruneAfterKey: {
			`[local]` + "\n" + `prune_after = "soon"`,
			`[local]` + "\n" + `prune_after = "-1h"`,
			`[local]` + "\n" + `prune_after = "14"`,
			"[managed.prune_after]\nvalue = \"later\"\n[local]",
		},
		PruneDailyKey: {
			`[local]` + "\n" + `prune_daily = "yes"`,
			`[local]` + "\n" + `prune_daily = "True"`,
			"[managed.prune_daily]\nvalue = \"on\"\n[local]",
		},
	} {
		for _, b := range bad {
			data := strings.Replace(director(t), "[local]", b, 1)
			if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%q: Parse error %v, want one naming %s", b, err, key)
			}
		}
	}
	cfg := &Config{Local: map[string]string{PruneAfterKey: "soon", PruneDailyKey: "maybe"}}
	if _, err := cfg.PruneAfter(); err == nil {
		t.Error("PruneAfter of an unparsed invalid value succeeded")
	}
	if _, err := cfg.PruneDaily(); err == nil {
		t.Error("PruneDaily of an unparsed invalid value succeeded")
	}
}
