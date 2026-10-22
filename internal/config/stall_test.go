package config

import (
	"strings"
	"testing"
	"time"
)

func TestStallInterval(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 time.Duration
	}{
		{name: "unset is the default", want: DefaultStallInterval},
		{name: "a local duration", local: `stall_interval = "90s"`, want: 90 * time.Second},
		{name: "zero disables", local: `stall_interval = "0s"`, want: 0},
		{name: "a managed duration", managed: "[managed.stall_interval]\nvalue = \"1h\"\n", want: time.Hour},
		{name: "an overridable managed duration replaced locally", local: `stall_interval = "2m"`,
			managed: "[managed.stall_interval]\nvalue = \"1h\"\noverridable = true\n", want: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.StallInterval(); got != tc.want || err != nil {
				t.Errorf("StallInterval() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultStallInterval != 15*time.Minute {
		t.Errorf("DefaultStallInterval = %v, want 15m", DefaultStallInterval)
	}
}

func TestStallIntervalRefusesAnInvalidDuration(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `stall_interval = "soon"`,
		`[local]` + "\n" + `stall_interval = "-1m"`,
		`[local]` + "\n" + `stall_interval = "15"`,
		"[managed.stall_interval]\nvalue = \"later\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "stall_interval") {
			t.Errorf("%q: Parse error %v, want one naming stall_interval", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{StallIntervalKey: "soon"}}
	if _, err := cfg.StallInterval(); err == nil {
		t.Error("StallInterval of an unparsed invalid value succeeded")
	}
}
