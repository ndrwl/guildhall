package cli

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// overrideStore is goldenStore with the test Director config, and a managed.toml beside it that pins ci_log_limit,
// in a copy enrolled and authorized for it; it returns the store's and the config's paths.
func overrideStore(t *testing.T) (path, cfg string) {
	t.Helper()
	cfg = writeDirector(t, directorTOML)
	managed, err := config.EncodeManaged(map[string]config.ManagedSetting{config.CILogLimitKey: {Value: "1MiB"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(filepath.Dir(cfg), "managed.toml"), string(managed))
	enrolManaged(t, filepath.Dir(cfg))
	return goldenStore(t), cfg
}

// TestTaskOverrideSetUnsetList: set records an override the config allows and prints its receipt, a rerun prints the
// same receipt and writes nothing, set A → B → A with the same words records three times, unset removes it, and list
// prints every record.
func TestTaskOverrideSetUnsetList(t *testing.T) {
	path, cfg := overrideStore(t)
	words := `Guildmaster: "run t-2 at high effort."`
	set := func(value string) []string {
		return []string{"override", "set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--value", value, "--text", words, "t-2"}
	}
	code, out, stderr := runTask(t, set("high")...)
	if code != 0 || stderr != "" {
		t.Fatalf("set: exit %d, stderr %s", code, stderr)
	}
	var rc store.Receipt
	if err := json.Unmarshal([]byte(out), &rc); err != nil || rc.Record != "task-override/1" || rc.Operation != "add-task-override" ||
		rc.MutationID != "task-override:app:t-2:set:default_effort:0" {
		t.Fatalf("set receipt = %+v, %v", rc, err)
	}
	if code, again, _ := runTask(t, "override", "set", "t-2", "--text", words, "--value", "high", "--key", "default_effort", "--repo", "app", "--config", cfg, "--store", path); code != 0 || again != out {
		t.Fatalf("rerun: exit %d, %s, want %s", code, again, out)
	}
	for i, value := range []string{"low", "high"} {
		if code, out, stderr := runTask(t, set(value)...); code != 0 || !strings.Contains(out, `"record":"task-override/`+string(rune('2'+i))+`"`) {
			t.Fatalf("set %s: exit %d, %s, stderr %s", value, code, out, stderr)
		}
	}
	// unset reads no config, so it needs none.
	if code, out, stderr := runTask(t, "override", "unset", "--store", path, "--repo", "app", "--key", "default_effort", "--text", "Guildmaster: back to normal.", "t-2"); code != 0 ||
		!strings.Contains(out, `"record":"task-override/4"`) || !strings.Contains(out, `"mutation_id":"task-override:app:t-2:unset:default_effort:3"`) {
		t.Fatalf("unset: exit %d, %s, stderr %s", code, out, stderr)
	}

	code, out, stderr = runTask(t, "override", "list", "--store", path, "--repo", "app", "--json", "t-2")
	var records []store.TaskOverride
	if code != 0 || json.Unmarshal([]byte(out), &records) != nil || len(records) != 4 {
		t.Fatalf("list: exit %d, %s, stderr %s", code, out, stderr)
	}
	for i, o := range records {
		op, value := []store.TaskOverrideOp{"set", "set", "set", "unset"}[i], []string{"high", "low", "high", ""}[i]
		if o.Number != i+1 || o.Op != op || o.Key != "default_effort" || o.Value != value || o.Author != "director" || o.AddedAt.IsZero() {
			t.Errorf("record %d = %+v", i+1, o)
		}
	}
	if active := store.ActiveTaskOverrides(records); len(active) != 0 {
		t.Errorf("active after unset = %+v", active)
	}
	if code, out, _ := runTask(t, "override", "list", "--store", path, "--repo", "app", "--json", "t-1"); code != 0 || out != "[]\n" {
		t.Errorf("list of a task without overrides: exit %d, %q", code, out)
	}
	if code, _, stderr := runTask(t, "override", "list", "--store", path, "--repo", "app", "--json", "t-9"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Errorf("list of an unknown task: exit %d, stderr %q", code, stderr)
	}
	app, stdout, stderr2 := newTestApp(nil, nil)
	code, out = app.Run([]string{"store", "doctor", path}), stdout.String()
	if code != 0 || stderr2.String() != "" || !strings.Contains(out, "task overrides 4,") {
		t.Errorf("store doctor: exit %d, %s", code, out)
	}
}

// TestTaskOverrideRefusals: usage errors exit 2 with the usage; the config's refusals exit 1 with the registry's
// reason unchanged, and the store's refusals exit 1, all writing nothing.
func TestTaskOverrideRefusals(t *testing.T) {
	path, cfg := overrideStore(t)
	for name, args := range map[string][]string{
		"no subcommand":          {},
		"unknown subcommand":     {"clear", "--store", path, "--repo", "app", "t-2"},
		"no config on set":       {"set", "--store", path, "--repo", "app", "--key", "default_effort", "--value", "high", "--text", "Words.", "t-2"},
		"config on unset":        {"unset", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--text", "Words.", "t-2"},
		"value on unset":         {"unset", "--store", path, "--repo", "app", "--key", "default_effort", "--value", "high", "--text", "Words.", "t-2"},
		"no key":                 {"set", "--store", path, "--config", cfg, "--repo", "app", "--value", "high", "--text", "Words.", "t-2"},
		"no value":               {"set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--text", "Words.", "t-2"},
		"no text":                {"set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--value", "high", "t-2"},
		"no repo":                {"set", "--store", path, "--config", cfg, "--key", "default_effort", "--value", "high", "--text", "Words.", "t-2"},
		"no task":                {"set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--value", "high", "--text", "Words."},
		"two tasks":              {"unset", "--store", path, "--repo", "app", "--key", "default_effort", "--text", "Words.", "t-1", "t-2"},
		"bad task id":            {"unset", "--store", path, "--repo", "app", "--key", "default_effort", "--text", "Words.", "T 2"},
		"json on set":            {"set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--value", "high", "--text", "Words.", "--json", "t-2"},
		"key on list":            {"list", "--store", path, "--repo", "app", "--key", "default_effort", "--json", "t-2"},
		"list without json":      {"list", "--store", path, "--repo", "app", "t-2"},
		"flags after terminator": {"list", "--store", path, "--repo", "app", "--", "t-2", "--json"},
		"no store":               {"unset", "--repo", "app", "--key", "default_effort", "--text", "Words.", "t-2"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append([]string{"override"}, args...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, "guildhall task override set") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
		})
	}

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown setting":       {[]string{"set", "--config", cfg, "--key", "no_such_setting", "--value", "1", "--text", "Words.", "t-2"}, `unknown setting "no_such_setting"`},
		"step key":              {[]string{"set", "--config", cfg, "--key", "step.build.effort", "--value", "max", "--text", "Words.", "t-2"}, `unknown setting "step.build.effort"`},
		"machine-wide":          {[]string{"set", "--config", cfg, "--key", "lock_wait", "--value", "1m", "--text", "Words.", "t-2"}, "`lock_wait` is machine-wide; it cannot be overridden per task"},
		"excluded merge key":    {[]string{"set", "--config", cfg, "--key", "merge.reapprove_after_rebase", "--value", "never", "--text", "Words.", "t-2"}, "`[repository.merge]` is not overridable per task"},
		"looser merge":          {[]string{"set", "--config", cfg, "--key", "merge.requires.ci_green", "--value", "false", "--text", "Words.", "t-2"}, "may only make merge requirements stricter"},
		"excluded director":     {[]string{"set", "--config", cfg, "--key", "director.idle_after", "--value", "1m", "--text", "Words.", "t-2"}, "`[director]` is not overridable per task"},
		"managed-pinned":        {[]string{"set", "--config", cfg, "--key", "ci_log_limit", "--value", "2MiB", "--text", "Words.", "t-2"}, "`ci_log_limit` is pinned by a managed setting that is not overridable"},
		"invalid value":         {[]string{"set", "--config", cfg, "--key", "default_effort", "--value", "huge", "--text", "Words.", "t-2"}, "default_effort"},
		"blank text":            {[]string{"set", "--config", cfg, "--key", "default_effort", "--value", "high", "--text", " ", "t-2"}, "must not be empty"},
		"unknown task":          {[]string{"set", "--config", cfg, "--key", "default_effort", "--value", "high", "--text", "Words.", "t-9"}, store.ErrNotFound.Error()},
		"unset when absent":     {[]string{"unset", "--key", "default_effort", "--text", "Words.", "t-2"}, "no override of default_effort in effect"},
		"unset of a step key":   {[]string{"unset", "--key", "step.build.effort", "--text", "Words.", "t-2"}, "is a step key"},
		"unset of a spaced key": {[]string{"unset", "--key", "default effort", "--text", "Words.", "t-2"}, "without whitespace"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append(append([]string{"override"}, tc.args[0], "--store", path, "--repo", "app"), tc.args[1:]...)...)
			if code != 1 || out != "" || !strings.Contains(stderr, "guildhall: task override "+tc.args[0]+":") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q, want %q", code, out, stderr, tc.want)
			}
		})
	}
	if code, out, stderr := runTask(t, "override", "list", "--store", path, "--repo", "app", "--json", "t-2"); code != 0 || out != "[]\n" {
		t.Errorf("after refusals: exit %d, %q, stderr %s", code, out, stderr)
	}
}

// TestTaskShowOverrides: task show lists the task's overrides in effect with their records; with the copy's config it
// also lists those the config would not apply now, with why, and without one it prints skipped_overrides null.
// task timeline lists every override record with its time.
func TestTaskShowOverrides(t *testing.T) {
	path, cfg := overrideStore(t)
	if code, _, stderr := runTask(t, "override", "set", "--store", path, "--config", cfg, "--repo", "app", "--key", "default_effort", "--value", "high", "--text", "Guildmaster: high.", "t-2"); code != 0 {
		t.Fatalf("set: exit %d, stderr %s", code, stderr)
	}
	// An override recorded before the managed pin it now loses to.
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTaskOverride("pinned", "app", "t-2", store.TaskOverrideSet, config.CILogLimitKey, "2MiB", 0, "Guildmaster: more log."); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	type shown struct {
		Overrides        []store.TaskOverride `json:"overrides"`
		SkippedOverrides *[]config.Skipped    `json:"skipped_overrides"`
	}
	show := func(args ...string) shown {
		t.Helper()
		code, out, stderr := runTask(t, append([]string{"show", "--store", path, "--repo", "app", "--json", "t-2"}, args...)...)
		var d shown
		if code != 0 || json.Unmarshal([]byte(out), &d) != nil {
			t.Fatalf("show %v: exit %d, %s, stderr %s", args, code, out, stderr)
		}
		return d
	}
	d := show()
	if len(d.Overrides) != 2 || d.Overrides[0].Key != config.CILogLimitKey || d.Overrides[0].Number != 2 ||
		d.Overrides[1].Key != "default_effort" || d.Overrides[1].Value != "high" || d.Overrides[1].Text != "Guildmaster: high." || d.Overrides[1].AddedAt.IsZero() {
		t.Errorf("overrides = %+v", d.Overrides)
	}
	if d.SkippedOverrides != nil {
		t.Errorf("skipped_overrides without a config = %+v, want null", *d.SkippedOverrides)
	}
	d = show("--config", cfg)
	if d.SkippedOverrides == nil || len(*d.SkippedOverrides) != 1 || (*d.SkippedOverrides)[0].Key != config.CILogLimitKey || !strings.Contains((*d.SkippedOverrides)[0].Reason, "pinned") {
		t.Errorf("skipped_overrides with the config = %+v", d.SkippedOverrides)
	}

	code, out, stderr := runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-2")
	var tl struct {
		Overrides []store.TaskOverride `json:"overrides"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &tl) != nil || len(tl.Overrides) != 2 || tl.Overrides[0].Number != 1 || tl.Overrides[0].AddedAt.IsZero() {
		t.Errorf("timeline overrides: exit %d, %+v, stderr %s", code, tl.Overrides, stderr)
	}
}

// TestTaskShowAlsoRequires: with the copy's config, task show lists the merge requirements the task's overrides add
// to the repository's merge_requires, and a recorded false among the skipped overrides; true on a requirement the
// repository already has adds nothing, and without a config also_requires is null.
func TestTaskShowAlsoRequires(t *testing.T) {
	path := goldenStore(t)
	cfg := writeDirector(t, strings.Replace(directorTOML, "owner_machine = \"workstation\"\n\n[[repository]]\nid = \"lib\"",
		"owner_machine = \"workstation\"\n\n[repository.merge.requires]\nguildmaster_approval = false\nci_green = false\n\n[[repository]]\nid = \"lib\"", 1))
	for _, key := range []string{"merge.requires.ci_green", "repository.merge.requires.artisan_approval"} {
		if code, _, stderr := runTask(t, "override", "set", "--store", path, "--config", cfg, "--repo", "app", "--key", key, "--value", "true", "--text", "Guildmaster: stricter.", "t-2"); code != 0 {
			t.Fatalf("set %s: exit %d, stderr %s", key, code, stderr)
		}
	}
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTaskOverride("looser", "app", "t-2", store.TaskOverrideSet, "merge.requires.nothing_open", "false", 0, "Recorded before the rule."); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	type shown struct {
		SkippedOverrides []config.Skipped `json:"skipped_overrides"`
		AlsoRequires     *[]string        `json:"also_requires"`
	}
	show := func(args ...string) shown {
		t.Helper()
		code, out, stderr := runTask(t, append([]string{"show", "--store", path, "--repo", "app", "--json", "t-2"}, args...)...)
		var d shown
		if code != 0 || json.Unmarshal([]byte(out), &d) != nil {
			t.Fatalf("show %v: exit %d, %s, stderr %s", args, code, out, stderr)
		}
		return d
	}
	if d := show(); d.AlsoRequires != nil {
		t.Errorf("also_requires without a config = %v, want null", *d.AlsoRequires)
	}
	d := show("--config", cfg)
	if d.AlsoRequires == nil || !slices.Equal(*d.AlsoRequires, []string{"ci_green"}) {
		t.Errorf("also_requires = %v, want [ci_green]", d.AlsoRequires)
	}
	if len(d.SkippedOverrides) != 1 || d.SkippedOverrides[0].Key != "merge.requires.nothing_open" || !strings.Contains(d.SkippedOverrides[0].Reason, "only make merge requirements stricter") {
		t.Errorf("skipped_overrides = %+v", d.SkippedOverrides)
	}
}

// TestTaskNewOverride: task new --override records each override after the task, with --override-quote's words, and
// a rerun records nothing more; one the config refuses creates no task; and --override without --override-quote, or
// with a malformed or repeated key, is a usage error.
func TestTaskNewOverride(t *testing.T) {
	path, cfg := overrideStore(t)
	newArgs := func(id string, extra ...string) []string {
		return append([]string{"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Overridden", id}, extra...)
	}
	code, _, stderr := runTask(t, newArgs("t-8", "--override", "default_effort=high", "--override", "lock_wait=1m", "--override-quote", "Guildmaster: so.")...)
	if code != 1 || !strings.Contains(stderr, "`lock_wait` is machine-wide") {
		t.Fatalf("refused override: exit %d, stderr %s; want exit 1 naming lock_wait", code, stderr)
	}
	if code, _, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-8"); code != 1 {
		t.Errorf("show of the refused task: exit %d, want 1 for no task", code)
	}
	for name, args := range map[string][]string{
		"no quote":   newArgs("t-8", "--override", "default_effort=high"),
		"no value":   newArgs("t-8", "--override", "default_effort", "--override-quote", "So."),
		"twice":      newArgs("t-8", "--override", "default_effort=high", "--override", "default_effort=low", "--override-quote", "So."),
		"quote only": newArgs("t-8", "--override-quote", "So."),
	} {
		if code, _, stderr := runTask(t, args...); code != 2 {
			t.Errorf("%s: exit %d, stderr %s; want 2", name, code, stderr)
		}
	}
	args := newArgs("t-9", "--override", "stall_interval=5m", "--override", "default_effort=high", "--override-quote", "Guildmaster: so.")
	if code, out, stderr := runTask(t, args...); code != 0 || !strings.Contains(out, `"record":"task"`) {
		t.Fatalf("task new: exit %d, %s, stderr %s", code, out, stderr)
	}
	if code, _, stderr := runTask(t, args...); code != 0 {
		t.Fatalf("task new rerun: exit %d, stderr %s", code, stderr)
	}
	code, out, stderr := runTask(t, "override", "list", "--store", path, "--repo", "app", "--json", "t-9")
	var records []store.TaskOverride
	if code != 0 || json.Unmarshal([]byte(out), &records) != nil || len(records) != 2 ||
		records[0].Key != "stall_interval" || records[1].Key != "default_effort" || records[1].Text != "Guildmaster: so." {
		t.Errorf("records after task new and its rerun: exit %d, %s, stderr %s", code, out, stderr)
	}
}
