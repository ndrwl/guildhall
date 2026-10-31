package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/store"
)

// recordOverrides records set overrides on app/t-1 straight into the store, as `task override set` would after its
// config check; a key that check would refuse stands for an override that has since stopped applying.
func recordOverrides(t *testing.T, path string, kv ...string) {
	t.Helper()
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < len(kv); i += 2 {
		if _, err := s.AddTaskOverride(fmt.Sprintf("override-%d", i), "app", "t-1", store.TaskOverrideSet, kv[i], kv[i+1], 0, "Guildmaster: run it so."); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTaskOverridesReachTheArtisan: the task's default_effort override is the effort its plan runs at, and the plan's
// binding records it with its record number, beside a machine-wide lock_wait override, skipped with the registry's
// reason rather than failing the advance.
func TestTaskOverridesReachTheArtisan(t *testing.T) {
	claudetest.Install(t)
	d, copyDir := newRunCopyOn(t, "claude-straight", "claude.toml")
	recordOverrides(t, d.store, "default_effort", "high", "lock_wait", "1s")
	code, out, stderr := d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t-1")
	if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	if got, err := claudetest.Settings(d.result("plan", 1)); err != nil || got != (adapter.Settings{Effort: "high"}) {
		t.Errorf("plan-1 ran on %+v, %v; want the overridden high effort", got, err)
	}
	data, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t-1", "plan-1.inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Overrides []struct {
			Key, Value string
			Number     int
		} `json:"overrides"`
		Skipped []config.Skipped `json:"skipped_overrides"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Overrides) != 1 || b.Overrides[0].Key != "default_effort" || b.Overrides[0].Value != "high" || b.Overrides[0].Number != 1 {
		t.Errorf("binding overrides = %+v, want default_effort high, record 1", b.Overrides)
	}
	if len(b.Skipped) != 1 || b.Skipped[0].Key != "lock_wait" || !strings.Contains(b.Skipped[0].Reason, "machine-wide") {
		t.Errorf("binding skipped overrides = %+v, want lock_wait as machine-wide", b.Skipped)
	}
}

// TestTaskOverridesReachValidate: the task's ci_log_limit and checks_refresh overrides, not the copy's defaults, are
// what validate cuts the failed job's log to and paces its reads by.
func TestTaskOverridesReachValidate(t *testing.T) {
	f := forgetest.NewFake()
	_, d := validateCopy(t, f)
	recordOverrides(t, d.store, "ci_log_limit", "64KiB", "checks_refresh", "0s")
	var log strings.Builder
	for i := range ciLogLines {
		fmt.Fprintf(&log, "2026-10-29T10:00:01.0000000Z line %04d %s\n", i, strings.Repeat("x", 40))
	}
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	f.SetChecks(remote, head, forge.Check{Name: "ci", State: forge.CheckFailed, ID: "CR_1", Job: "123"})
	f.SetLog("123", log.String())
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || strings.TrimSpace(out) != `{"wait":"director","step":"validate"}` {
		t.Fatalf("task start = %d, %s, %s; want the failed result, read at once under checks_refresh 0s", code, out, stderr)
	}
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "ci-logs", "123.log"))
	if err != nil || len(cached) > 64<<10+100 || !strings.HasPrefix(string(cached), "[guildhall: ") {
		t.Errorf("cached log is %d bytes, %v; want it cut to the overridden 64 KiB", len(cached), err)
	}
}

// TestTaskOverrideBindingWithoutOverrides: a task with no overrides binds its attempt with neither field, so its
// binding reads as it did before task overrides existed.
func TestTaskOverrideBindingWithoutOverrides(t *testing.T) {
	claudetest.Install(t)
	d, copyDir := newRunCopyOn(t, "claude-straight", "claude.toml")
	if code, _, stderr := d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t-1"); code != 0 {
		t.Fatalf("task start: exit %d, stderr %s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t-1", "plan-1.inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	if !reflect.DeepEqual(keys, []string{"inputs"}) {
		t.Errorf("binding fields = %v, want only inputs", keys)
	}
}
