package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
)

// TestTaskStartRunsTheProfilesModelAndEffort: a Director's profile model and effort reach the Claude session its
// stage runs through launch.Process, `artisan start` and the wrapper; unset effort inherits medium.
func TestTaskStartRunsTheProfilesModelAndEffort(t *testing.T) {
	claudetest.Install(t)
	for name, c := range map[string]struct {
		config   string
		fallback string
		want     adapter.Settings
	}{"set": {"claude-model.toml", "low", adapter.Settings{Model: "opus", Effort: "high"}}, "unset": {"claude.toml", "", adapter.Settings{Effort: "medium"}}, "configured-default": {"claude.toml", "low", adapter.Settings{Effort: "low"}}} {
		t.Run(name, func(t *testing.T) {
			d, copyDir := newRunCopyOn(t, "claude-straight", c.config)
			if c.fallback != "" {
				b, err := os.ReadFile(d.config)
				if err != nil {
					t.Fatal(err)
				}
				b = bytes.Replace(b, []byte("[local]\n"), []byte("[local]\ndefault_effort='"+c.fallback+"'\n"), 1)
				if err := os.WriteFile(d.config, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t-1")
			if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
				t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
			}
			d.waitResult(t, "plan", 1)
			if got, err := claudetest.Settings(d.result("plan", 1)); err != nil || got != c.want {
				t.Errorf("plan-1 ran on %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

// TestTaskStartRefusesSettingsARuntimeCannotHonour: a task whose workflow names a profile with a setting its
// runtime cannot honour is refused by task new, and by task start once the config changed after the task was
// accepted, naming the profile and the setting; task start records no attempt.
func TestTaskStartRefusesSettingsARuntimeCannotHonour(t *testing.T) {
	claudetest.Install(t)
	d, copyDir := newRunCopyOn(t, "claude-straight", "claude-model.toml")
	b, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.config, bytes.Replace(b, []byte(`effort = "high"`), []byte(`effort = "extreme"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "new", "--store", d.store, "--config", d.config, "--repo", "app", "--title", "Another", "t-2")
	if code != 1 || out != "" || !strings.Contains(stderr, "profile planner") || !strings.Contains(stderr, "extreme") {
		t.Errorf("task new: exit %d, %q, stderr %s; want exit 1 naming profile planner and its effort", code, out, stderr)
	}
	code, out, stderr = d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t-1")
	if code != 1 || out != "" || !strings.Contains(stderr, "profile planner") || !strings.Contains(stderr, "extreme") {
		t.Fatalf("task start: exit %d, %q, stderr %s; want exit 1 naming profile planner and its effort", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Errorf("a refused task recorded attempts: %+v", got)
	}
}

// TestDirectorStartRefusesSettingsARuntimeCannotHonour: a profile whose model or effort its runtime cannot honour
// refuses the preflight's artisans check, naming the profile and the setting, rather than running on a default.
func TestDirectorStartRefusesSettingsARuntimeCannotHonour(t *testing.T) {
	claudetest.Install(t)
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n\n[profile.builder]\nadapter = \"claude\"\nmodel = \"opus\"\neffort = \"extreme\"\n", "/exit")
	code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
	if code != 1 || !strings.Contains(out, "artisans: refused") || !strings.Contains(stderr, "profile builder") || !strings.Contains(stderr, "extreme") {
		t.Errorf("exit %d\n%s\n%s; want the artisans check refused naming profile builder and its effort", code, out, stderr)
	}
	dir = directorCopy(t, "[director]\nadapter = \"fake\"\n\n[profile.builder]\nadapter = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir); code != 0 || strings.Contains(out, "refused") {
		t.Errorf("exit %d\n%s\n%s; want a profile Claude can honour admitted", code, out, stderr)
	}
}

func TestDirectorEffortReachesRuntime(t *testing.T) {
	claudetest.Install(t)
	for _, runtime := range []string{"fake", "claude"} {
		for _, tc := range []struct{ tables, fallback, want string }{
			{"[director]\nadapter='fake'\n", "", "medium"},
			{"[director]\nadapter='fake'\n", "low", "low"},
			{"[director]\nadapter='fake'\neffort='high'\n", "low", "high"},
			{"", "", "medium"},
		} {
			dir := directorCopy(t, strings.ReplaceAll(tc.tables, "'fake'", "'"+runtime+"'"), "/exit")
			if tc.fallback != "" {
				path := filepath.Join(dir, "config.toml")
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				b = bytes.Replace(b, []byte("[local]\n"), []byte("[local]\ndefault_effort='"+tc.fallback+"'\n"), 1)
				if err := os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if code, out, stderr := runDirectorCmd(t, "start", runtime, "--copy", dir); code != 0 {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
			}
			n := len(sessions(t, dir))
			workDir := sessionDir(dir, n)
			if runtime == "claude" {
				workDir = claude.WorkspaceDir(dir)
			}
			got, err := fake.RecordedSettings(filepath.Join(workDir, "unused-result.json"))
			if err != nil || got.Effort != tc.want {
				t.Errorf("%s settings = %+v, %v; want %s", runtime, got, err, tc.want)
			}
		}
	}
}

func TestDirectorRefusesOwnEffort(t *testing.T) {
	claudetest.Install(t)
	for _, setting := range []string{"effort='extreme'", "model='--bad'"} {
		dir := directorCopy(t, "[director]\nadapter='claude'\n"+setting+"\n", "/exit")
		code, out, stderr := runDirectorCmd(t, "start", "--check", "--copy", dir)
		if code != 1 || !strings.Contains(out, "runtime: refused") || !strings.Contains(stderr, "cannot honour the setting") {
			t.Errorf("exit %d: %s %s", code, out, stderr)
		}
		if got := sessions(t, dir); len(got) != 0 {
			t.Errorf("refusal recorded sessions: %+v", got)
		}
	}
}

// TestArtisanStartSettingsAreAnOwnedAttempts: --model and --effort go to an owned attempt's runtime, not a tab's.
func TestArtisanStartSettingsAreAnOwnedAttempts(t *testing.T) {
	prompt, result, dir := artisanFiles(t, "x")
	inTab := []string{"artisan", "start", "--adapter", "fake", "--task", "task-1", "--attempt", "attempt-1", "--prompt", prompt, "--result", result, "--workdir", dir,
		"--herdr-session", "lab", "--herdr-workspace", "w1"}
	for _, flag := range []string{"--model", "--effort"} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(append(append([]string{}, inTab...), flag, "x")); code != 2 || !strings.Contains(stderr.String(), "do not apply in a Herdr tab") {
			t.Errorf("%s in a tab: exit %d, stderr %q; want 2", flag, code, stderr)
		}
	}
}
