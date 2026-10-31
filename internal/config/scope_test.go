package config

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEveryKeyIsRegistered fails when an exported *Key constant of this package names a setting the registry does
// not declare, so no setting lands without a scope.
func TestEveryKeyIsRegistered(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, ident := range v.Names {
					if !ident.IsExported() || !strings.HasSuffix(ident.Name, "Key") {
						continue
					}
					lit, ok := v.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s is not a string literal", ident.Name)
						continue
					}
					key, _ := strconv.Unquote(lit.Value)
					found++
					if _, ok := SettingScope(key); !ok {
						t.Errorf("%s (%q) is not in the setting registry; declare its scope in settingRegistry", ident.Name, key)
					}
				}
			}
		}
	}
	if found < 27 {
		t.Errorf("found %d *Key constants, want at least 27", found)
	}
}

func TestSettingScopes(t *testing.T) {
	task := []string{"default_effort", "stall_interval", "checks_refresh", "ci_log_limit",
		"quality_self_review", "quality_size_budget_lines", "quality_size_budget_files", "quality_audit_sample",
		"quality_reference_runs", "quality_reference_runs_per_task", "quality_reference_cost_cap_usd",
		"quality_judge_profiles", "quality_judge_separate_family", "quality_skill_edits_need_approval"}
	machine := []string{"max_running_attempts", "max_attempts_per_day", "lock_wait", "herdr_session",
		"artisan_workspace_close_after", "artisan_pane_output", "prune_after", "prune_daily", "wake_quiet", "wake_max_delay",
		"claude_skills_director", "claude_skills_artisan", "standards_revision", "claude_skills_profile_builder"}
	for _, key := range task {
		if got, ok := SettingScope(key); !ok || got != ScopeTask {
			t.Errorf("SettingScope(%q) = %q, %v; want task", key, got, ok)
		}
	}
	for _, key := range machine {
		if got, ok := SettingScope(key); !ok || got != ScopeMachine {
			t.Errorf("SettingScope(%q) = %q, %v; want machine", key, got, ok)
		}
	}
	for _, key := range []string{"no_such_setting", "claude_skills_profile_", "step.build.effort", "repository.merge"} {
		if got, ok := SettingScope(key); ok {
			t.Errorf("SettingScope(%q) = %q, true; want unknown", key, got)
		}
	}
	if n := len(settingRegistry); n != 28 {
		t.Errorf("the registry has %d entries, want 28: 27 settings and the profile skill policy prefix", n)
	}
}

// overrideConfig parses the test Director config with extra managed and local settings.
func overrideConfig(t *testing.T, managed, local string) *Config {
	t.Helper()
	data := strings.Replace(director(t), "[local]", managed+"[local]\n"+local, 1)
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWithTaskOverridesPrecedence(t *testing.T) {
	cfg := overrideConfig(t,
		"[managed.stall_interval]\nvalue = \"1h\"\noverridable = true\n[managed.checks_refresh]\nvalue = \"5m\"\n",
		"default_effort = \"low\"\n")
	before := maps.Clone(cfg.Local)
	got, skipped, err := cfg.WithTaskOverrides(map[string]string{
		DefaultEffortKey: "max", StallIntervalKey: "2m", ChecksRefreshKey: "0s", "step.build.effort": "max",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The override beats local and an overridable managed key, and loses to a managed key that is not overridable.
	if e := got.EffectiveEffort(""); e != "max" {
		t.Errorf("effective effort = %q, want the override max over local low", e)
	}
	if d, _ := got.StallInterval(); d != 2*time.Minute {
		t.Errorf("stall interval = %v, want the override 2m over an overridable managed 1h", d)
	}
	if d, _ := got.ChecksRefresh(); d != 5*time.Minute {
		t.Errorf("checks refresh = %v, want the pinned managed 5m", d)
	}
	if len(skipped) != 1 || skipped[0].Key != ChecksRefreshKey || !strings.Contains(skipped[0].Reason, "pinned by a managed setting") {
		t.Errorf("skipped = %+v, want only checks_refresh, pinned", skipped)
	}
	// The receiver is not mutated, and a step key is left to the workflow.
	if !maps.Equal(cfg.Local, before) || cfg.EffectiveEffort("") != "low" {
		t.Errorf("receiver Local = %v, want unchanged %v", cfg.Local, before)
	}
	if _, ok := got.Local["step.build.effort"]; ok {
		t.Error("a step key went into the settings overlay")
	}
}

func TestWithTaskOverridesNone(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	got, skipped, err := cfg.WithTaskOverrides(nil)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("WithTaskOverrides(nil) = %v, %v", skipped, err)
	}
	if got == cfg || !maps.Equal(got.Local, cfg.Local) {
		t.Errorf("WithTaskOverrides(nil) = %p with Local %v; want a copy of %p", got, got.Local, cfg)
	}
}

func TestWithTaskOverridesSkips(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	got, skipped, err := cfg.WithTaskOverrides(map[string]string{
		LockWaitKey:                    "5m",
		"no_such_setting":              "x",
		"reapprove_after_rebase":       "never",
		DefaultEffortKey:               "extreme",
		QualityJudgeProfilesKey:        "undeclared",
		QualityAuditSampleKey:          "1",
		QualityReferenceRunsKey:        "on-change-and-release",
		QualityReferenceCostCapKey:     "12.50",
		QualityReferenceRunsPerTaskKey: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Key] = s.Reason
	}
	want := map[string]string{
		LockWaitKey:                    "`lock_wait` is machine-wide; it cannot be overridden per task",
		"no_such_setting":              "unknown setting",
		"reapprove_after_rebase":       "`[repository.merge]` is not overridable per task",
		DefaultEffortKey:               "must be low, medium, high, xhigh or max",
		QualityJudgeProfilesKey:        "not a declared profile",
		QualityReferenceRunsPerTaskKey: "must be a positive whole number",
	}
	if !slices.Equal(slices.Sorted(maps.Keys(reasons)), slices.Sorted(maps.Keys(want))) {
		t.Fatalf("skipped %v, want %v", reasons, slices.Sorted(maps.Keys(want)))
	}
	for key, part := range want {
		if !strings.Contains(reasons[key], part) {
			t.Errorf("skip reason for %s = %q, want it to contain %q", key, reasons[key], part)
		}
	}
	if !slices.IsSortedFunc(skipped, func(a, b Skipped) int { return strings.Compare(a.Key, b.Key) }) {
		t.Errorf("skipped is not in key order: %+v", skipped)
	}
	// The ones that apply apply together, the cost cap with the reference runs it allows.
	q, err := got.Quality()
	if err != nil {
		t.Fatal(err)
	}
	if q.AuditSample != 1 || q.ReferenceRuns != ReferenceRunsOnChangeAndRelease || q.ReferenceCostCapUSD != "12.50" {
		t.Errorf("quality = %+v, want the audit sample, reference runs and cost cap overrides", q)
	}
	if l, _ := got.LockWait(); l != DefaultLockWait {
		t.Errorf("lock wait = %v, want the copy's", l)
	}
}

func TestWithTaskOverridesSkipsAnInvalidCombination(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	got, skipped, err := cfg.WithTaskOverrides(map[string]string{QualityReferenceRunsKey: "on-change-and-release"})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "needs quality_reference_cost_cap_usd set") {
		t.Errorf("skipped = %+v, want reference runs skipped for want of a cost cap", skipped)
	}
	if q, _ := got.Quality(); q.ReferenceRuns != ReferenceRunsOff {
		t.Errorf("reference runs = %q, want the copy's off", q.ReferenceRuns)
	}
}

func TestWithTaskOverridesRefusesAnInvalidReceiver(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	bad := *cfg
	bad.Local = map[string]string{StallIntervalKey: "soon"}
	if _, _, err := bad.WithTaskOverrides(nil); err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("WithTaskOverrides on an invalid config = %v, want invalid config", err)
	}
}

func TestCheckTaskOverride(t *testing.T) {
	cfg := overrideConfig(t, "[managed.ci_log_limit]\nvalue = \"1MiB\"\n", "")
	if err := cfg.CheckTaskOverride(DefaultEffortKey, "high"); err != nil {
		t.Errorf("default_effort high: %v", err)
	}
	for _, tc := range []struct{ key, value, want string }{
		{LockWaitKey, "5m", "`lock_wait` is machine-wide; it cannot be overridden per task"},
		{StandardsRevisionKey, "standards-2", "machine-wide"},
		{"claude_skills_profile_builder", `{"preset":"builder-v1"}`, "machine-wide"},
		{"repository.merge.requires.guildmaster_approval", "false", "may only make merge requirements stricter"},
		{"merge.reapprove_after_rebase", "never", "`[repository.merge]` is not overridable per task"},
		{"director.idle_after", "1m", "`[director]` is not overridable per task"},
		{"owner_machine", "other", "repository identity is not overridable per task"},
		{CILogLimitKey, "2MiB", "pinned by a managed setting"},
		{StallIntervalKey, "soon", "stall_interval \"soon\""},
		{QualityReferenceRunsKey, "on-change-and-release", "needs quality_reference_cost_cap_usd"},
		{"step.build.effort", "max", "unknown setting"},
		{"Bad Key", "x", "unknown setting"},
	} {
		err := cfg.CheckTaskOverride(tc.key, tc.value)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckTaskOverride(%q, %q) = %v, want %q", tc.key, tc.value, err, tc.want)
		}
	}
	if err := cfg.CheckTaskOverride(LockWaitKey, "5m"); !errors.Is(err, ErrNotTaskOverridable) {
		t.Errorf("a machine-wide refusal is %v, want ErrNotTaskOverridable", err)
	}
	if err := cfg.CheckTaskOverride("nope", "x"); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("an unknown key refusal is %v, want ErrUnknownSetting", err)
	}
}
