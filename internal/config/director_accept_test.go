package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// withAccept returns director(t) with the site repository's director_accept keys set to extra and [local] given
// local's lines.
func withAccept(t *testing.T, extra, local string) string {
	t.Helper()
	s := strings.Replace(director(t), "no_ci = true\n", "no_ci = true\n"+extra, 1)
	return strings.Replace(s, "[local]\n", "[local]\n"+local, 1)
}

// TestDirectorAcceptDefaultsOff: a config without the keys reads off and has neither in its JSON, so its digest is
// unchanged.
func TestDirectorAcceptDefaultsOff(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfg.DirectorAccept("site")
	if err != nil {
		t.Fatal(err)
	}
	if a.On() || a != (DirectorAccept{}) {
		t.Errorf("unset DirectorAccept = %+v, want off", a)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"DirectorAccept`) {
		t.Fatalf("config JSON without director_accept keys = %s", b)
	}
	if _, err := cfg.DirectorAccept("nowhere"); err == nil {
		t.Error("DirectorAccept of an unknown repository: no error")
	}
}

// TestDirectorAcceptKeys: both keys must be non-zero to turn it on, each is capped by the repository's size budget
// where that half is non-zero, and a negative value is refused.
func TestDirectorAcceptKeys(t *testing.T) {
	for _, c := range []struct {
		name, keys, local string
		want              DirectorAccept
	}{
		{"one key only", "director_accept_lines = 200\n", "", DirectorAccept{SetLines: 200}},
		{"under the default budget", "director_accept_lines = 200\ndirector_accept_files = 8\n", "",
			DirectorAccept{Lines: 200, Files: 8, SetLines: 200, SetFiles: 8, BudgetLines: 400, BudgetFiles: 15}},
		{"capped by the default budget", "director_accept_lines = 900\ndirector_accept_files = 30\n", "",
			DirectorAccept{Lines: 400, Files: 15, SetLines: 900, SetFiles: 30, BudgetLines: 400, BudgetFiles: 15}},
		{"capped by a local budget", "director_accept_lines = 300\ndirector_accept_files = 10\n", "quality_size_budget_lines = \"100\"\n",
			DirectorAccept{Lines: 100, Files: 10, SetLines: 300, SetFiles: 10, BudgetLines: 100, BudgetFiles: 15}},
		{"a budget half off adds no cap", "director_accept_lines = 900\ndirector_accept_files = 10\n", "quality_size_budget_lines = \"0\"\n",
			DirectorAccept{Lines: 900, Files: 10, SetLines: 900, SetFiles: 10, BudgetFiles: 15}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(withAccept(t, c.keys, c.local)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := cfg.DirectorAccept("site")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("DirectorAccept = %+v, want %+v", got, c.want)
			}
			if got.On() != (c.want.Lines > 0 && c.want.Files > 0) {
				t.Errorf("On = %v", got.On())
			}
		})
	}
	for _, key := range []string{"director_accept_lines", "director_accept_files"} {
		_, err := Parse([]byte(withAccept(t, key+" = -1\n", "")))
		if err == nil || !strings.Contains(err.Error(), `repository "site": `+key+` -1 must be a whole number`) {
			t.Errorf("%s = -1: err = %v, want it refused", key, err)
		}
	}
}

// TestDirectorAcceptTaskBudget: a task override that raises the size budget raises the task's effective budget, but
// the threshold is read from the copy's config, so it stays capped by the repository's own budget.
func TestDirectorAcceptTaskBudget(t *testing.T) {
	cfg, err := Parse([]byte(withAccept(t, "director_accept_lines = 300\ndirector_accept_files = 10\n", "quality_size_budget_lines = \"100\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	eff, skipped, err := cfg.WithTaskOverrides(map[string]string{QualitySizeBudgetLinesKey: "1000"})
	if err != nil || len(skipped) > 0 {
		t.Fatalf("WithTaskOverrides: %v %v", skipped, err)
	}
	if q, _ := eff.Quality(); q.SizeBudgetLines != 1000 {
		t.Fatalf("task budget = %d, want the override", q.SizeBudgetLines)
	}
	a, err := cfg.DirectorAccept("site")
	if err != nil {
		t.Fatal(err)
	}
	if a.Lines != 100 {
		t.Errorf("threshold = %d lines, want the repository's budget of 100", a.Lines)
	}
}

// TestAcceptRequiresGuildmasterOverride: the override is stricter only, true applies and is kept out of Local,
// false and other values are refused or skipped with the reason.
func TestAcceptRequiresGuildmasterOverride(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckTaskOverride(AcceptRequiresGuildmasterOverride, "true"); err != nil {
		t.Errorf("true refused: %v", err)
	}
	for _, bad := range []string{"false", "yes", ""} {
		if err := cfg.CheckTaskOverride(AcceptRequiresGuildmasterOverride, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	eff, skipped, err := cfg.WithTaskOverrides(map[string]string{AcceptRequiresGuildmasterOverride: "true"})
	if err != nil || len(skipped) > 0 {
		t.Fatalf("WithTaskOverrides(true): %v %v", skipped, err)
	}
	if _, ok := eff.Local[AcceptRequiresGuildmasterOverride]; ok {
		t.Error("the override went into Local")
	}
	_, skipped, err = cfg.WithTaskOverrides(map[string]string{AcceptRequiresGuildmasterOverride: "false"})
	if err != nil || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "cannot be false") {
		t.Errorf("WithTaskOverrides(false) skipped = %+v, err = %v, want it skipped", skipped, err)
	}
	if !AcceptRequiresGuildmaster(map[string]string{AcceptRequiresGuildmasterOverride: "true"}) ||
		AcceptRequiresGuildmaster(map[string]string{AcceptRequiresGuildmasterOverride: "false"}) || AcceptRequiresGuildmaster(nil) {
		t.Error("AcceptRequiresGuildmaster reads the override wrongly")
	}
}
