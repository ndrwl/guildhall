package config

import (
	"strings"
	"testing"
	"time"
)

func TestChecksRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 time.Duration
	}{
		{name: "unset is the default", want: DefaultChecksRefresh},
		{name: "a local duration", local: `checks_refresh = "30s"`, want: 30 * time.Second},
		{name: "zero reads at every advance", local: `checks_refresh = "0s"`, want: 0},
		{name: "a managed duration", managed: "[managed.checks_refresh]\nvalue = \"5m\"\n", want: 5 * time.Minute},
		{name: "an overridable managed duration replaced locally", local: `checks_refresh = "2m"`,
			managed: "[managed.checks_refresh]\nvalue = \"5m\"\noverridable = true\n", want: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.ChecksRefresh(); got != tc.want || err != nil {
				t.Errorf("ChecksRefresh() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultChecksRefresh != time.Minute {
		t.Errorf("DefaultChecksRefresh = %v, want 1m", DefaultChecksRefresh)
	}
}

func TestChecksRefreshRefusesAnInvalidDuration(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `checks_refresh = "soon"`,
		`[local]` + "\n" + `checks_refresh = "-1m"`,
		`[local]` + "\n" + `checks_refresh = "60"`,
		"[managed.checks_refresh]\nvalue = \"later\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "checks_refresh") {
			t.Errorf("%q: Parse error %v, want one naming checks_refresh", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{ChecksRefreshKey: "soon"}}
	if _, err := cfg.ChecksRefresh(); err == nil {
		t.Error("ChecksRefresh of an unparsed invalid value succeeded")
	}
}
