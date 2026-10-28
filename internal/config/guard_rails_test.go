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
		{name: "a local 0 is no daily cap", local: `max_attempts_per_day = "0"`, wantRunning: DefaultMaxRunningAttempts, wantDaily: 0},
		{name: "a managed 0 is no daily cap", local: `max_running_attempts = "2"`,
			managed: "[managed.max_attempts_per_day]\nvalue = \"0\"\n", wantRunning: 2, wantDaily: 0},
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
			tool, site := g.MergeRequires["tool"], g.MergeRequires["site"]
			if len(g.MergeRequires) != 2 || tool.GuildmasterApproval == nil || !*tool.GuildmasterApproval || site.GuildmasterApproval == nil || *site.GuildmasterApproval ||
				site.ArtisanApproval == nil || !*site.ArtisanApproval {
				t.Errorf("MergeRequires = %+v, want every effective value set", g.MergeRequires)
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
		"[local]\nmax_attempts_per_day = \"-1\"",
		"[local]\nmax_attempts_per_day = \"00\"",
		"[local]\nmax_attempts_per_day = \"+0\"",
		"[local]\nmax_attempts_per_day = \" 0\"",
		"[managed.max_running_attempts]\nvalue = \"0\"\n[local]",
		"[managed.max_attempts_per_day]\nvalue = \"-1\"\n[local]",
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
	cfg = &Config{Local: map[string]string{MaxRunningAttemptsKey: "0"}}
	if _, err := cfg.GuardRails(); err == nil || !strings.Contains(err.Error(), MaxRunningAttemptsKey) {
		t.Errorf("GuardRails of an unparsed running cap of 0 = %v", err)
	}
	cfg = &Config{Local: map[string]string{MaxAttemptsPerDayKey: "-1"}}
	if _, err := cfg.GuardRails(); err == nil || !strings.Contains(err.Error(), MaxAttemptsPerDayKey) || !strings.Contains(err.Error(), "0 for no daily cap") {
		t.Errorf("GuardRails of an unparsed daily cap of -1 = %v, want one naming the key and that 0 is no daily cap", err)
	}
}

// TestParseAcceptsNoDailyCap: Parse accepts a daily cap of "0", managed or local, and still refuses a running cap
// of "0" in either place.
func TestParseAcceptsNoDailyCap(t *testing.T) {
	for _, tc := range []struct {
		name, replace, wantKey string
	}{
		{name: "local daily 0", replace: "[local]\nmax_attempts_per_day = \"0\""},
		{name: "managed daily 0", replace: "[managed.max_attempts_per_day]\nvalue = \"0\"\n[local]"},
		{name: "local running 0", replace: "[local]\nmax_running_attempts = \"0\"", wantKey: MaxRunningAttemptsKey},
		{name: "managed running 0", replace: "[managed.max_running_attempts]\nvalue = \"0\"\n[local]", wantKey: MaxRunningAttemptsKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(director(t), "[local]", tc.replace, 1)))
			switch {
			case tc.wantKey == "" && err != nil:
				t.Errorf("Parse = %v, want a daily cap of 0 accepted", err)
			case tc.wantKey != "" && (err == nil || !strings.Contains(err.Error(), tc.wantKey)):
				t.Errorf("Parse = %v, want a refusal naming %s", err, tc.wantKey)
			}
		})
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
