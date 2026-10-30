package config

import (
	"strings"
	"testing"
	"time"
)

// TestWakeHold (d20): wake_quiet and wake_max_delay default to 3s and 15s, "0s" turns holding off, and each is
// read as set.
func TestWakeHold(t *testing.T) {
	for _, tc := range []struct {
		name, local string
		quiet, most time.Duration
	}{
		{name: "unset is the default", quiet: DefaultWakeQuiet, most: DefaultWakeMaxDelay},
		{name: "zero turns holding off", local: `wake_quiet = "0s"`, quiet: 0, most: DefaultWakeMaxDelay},
		{name: "both set", local: "wake_quiet = \"5s\"\nwake_max_delay = \"1m\"", quiet: 5 * time.Second, most: time.Minute},
		{name: "equal", local: "wake_quiet = \"15s\"", quiet: 15 * time.Second, most: 15 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.WakeQuiet(); got != tc.quiet || err != nil {
				t.Errorf("WakeQuiet() = %v, %v; want %v", got, err, tc.quiet)
			}
			if got, err := cfg.WakeMaxDelay(); got != tc.most || err != nil {
				t.Errorf("WakeMaxDelay() = %v, %v; want %v", got, err, tc.most)
			}
		})
	}
	if DefaultWakeQuiet != 3*time.Second || DefaultWakeMaxDelay != 15*time.Second {
		t.Errorf("defaults = %v, %v; want 3s, 15s", DefaultWakeQuiet, DefaultWakeMaxDelay)
	}
}

// TestWakeHoldRefusesABadSetting (d20): a value that is not a duration, a negative quiet period, a zero bound and
// a bound below the quiet period are refused, each naming its key.
func TestWakeHoldRefusesABadSetting(t *testing.T) {
	for bad, key := range map[string]string{
		`wake_quiet = "soon"`:                           WakeQuietKey,
		`wake_quiet = "-1s"`:                            WakeQuietKey,
		`wake_max_delay = "later"`:                      WakeMaxDelayKey,
		`wake_max_delay = "0s"`:                         WakeMaxDelayKey,
		"wake_quiet = \"10s\"\nwake_max_delay = \"5s\"": WakeMaxDelayKey,
		`wake_quiet = "20s"`:                            WakeMaxDelayKey,
	} {
		data := strings.Replace(director(t), "[local]", "[local]\n"+bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%q: Parse error %v, want one naming %s", bad, err, key)
		}
	}
	cfg := &Config{Local: map[string]string{WakeQuietKey: "soon", WakeMaxDelayKey: "0s"}}
	if _, err := cfg.WakeQuiet(); err == nil {
		t.Error("WakeQuiet of an unparsed invalid value succeeded")
	}
	if _, err := cfg.WakeMaxDelay(); err == nil {
		t.Error("WakeMaxDelay of an unparsed invalid value succeeded")
	}
}
