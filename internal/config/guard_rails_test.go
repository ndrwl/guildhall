package config

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardRails(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed   string
		wantRunning, wantDaily int
	}{
		{name: "unset are the defaults", wantRunning: DefaultMaxRunningAttempts, wantDaily: DefaultMaxAttemptsPerDay},
		{name: "local caps", local: "max_running_attempts = \"2\"\nmax_attempts_per_day = \"10\"", wantRunning: 2, wantDaily: 10},
		{name: "a managed cap", managed: "[managed.max_attempts_per_day]\nvalue = \"5\"\n", wantRunning: DefaultMaxRunningAttempts, wantDaily: 5},
		{name: "an overridable managed cap replaced locally", local: `max_running_attempts = "1"`,
			managed: "[managed.max_running_attempts]\nvalue = \"3\"\noverridable = true\n", wantRunning: 1, wantDaily: DefaultMaxAttemptsPerDay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			g, err := cfg.GuardRails()
			if err != nil {
				t.Fatal(err)
			}
			if g.MaxRunningAttempts != tc.wantRunning || g.MaxAttemptsPerDay != tc.wantDaily {
				t.Errorf("caps = %d running, %d a day; want %d, %d", g.MaxRunningAttempts, g.MaxAttemptsPerDay, tc.wantRunning, tc.wantDaily)
			}
			if strings.Join(g.Repositories, ",") != "tool,site" {
				t.Errorf("Repositories = %q, want the listed ids in config order", g.Repositories)
			}
			if g.MergePolicies["tool"] != MergeApproval || g.MergePolicies["site"] != MergeAuto || len(g.MergePolicies) != 2 {
				t.Errorf("MergePolicies = %v", g.MergePolicies)
			}
		})
	}
	if DefaultMaxRunningAttempts != 4 || DefaultMaxAttemptsPerDay != 40 {
		t.Errorf("defaults = %d, %d; want 4 and 40", DefaultMaxRunningAttempts, DefaultMaxAttemptsPerDay)
	}
}

func TestGuardRailsRefuseAnInvalidCap(t *testing.T) {
	for _, bad := range []string{
		"[local]\nmax_running_attempts = \"0\"",
		"[local]\nmax_running_attempts = \"-1\"",
		"[local]\nmax_running_attempts = \"four\"",
		"[local]\nmax_attempts_per_day = \"2.5\"",
		"[local]\nmax_attempts_per_day = \" 3\"",
		"[managed.max_attempts_per_day]\nvalue = \"many\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		key := MaxRunningAttemptsKey
		if strings.Contains(bad, MaxAttemptsPerDayKey) {
			key = MaxAttemptsPerDayKey
		}
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%q: Parse error %v, want one naming %s", bad, err, key)
		}
	}
	cfg := &Config{Local: map[string]string{MaxAttemptsPerDayKey: "none"}}
	if _, err := cfg.GuardRails(); err == nil || !strings.Contains(err.Error(), MaxAttemptsPerDayKey) {
		t.Errorf("GuardRails of an unparsed invalid cap = %v", err)
	}
}

func TestListedRepository(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := cfg.ListedRepository("site"); err != nil || r.ID != "site" {
		t.Errorf("ListedRepository(site) = %+v, %v", r, err)
	}
	_, err = cfg.ListedRepository("elsewhere")
	if !errors.Is(err, ErrRepositoryNotListed) || !strings.Contains(err.Error(), "guard rail repositories") || !strings.Contains(err.Error(), `"elsewhere"`) {
		t.Errorf("ListedRepository(elsewhere) = %v, want ErrRepositoryNotListed naming the rail and the id", err)
	}
}
