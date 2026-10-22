package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// newCopy returns a Director copy directory (docs/running.md#copies): config.toml with its workflows, an empty
// director.store made by store init and state/.
func newCopy(t *testing.T) string {
	t.Helper()
	dir := bareCopy(t)
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}); code != 0 {
		t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	return dir
}

// bareCopy is a copy directory with its config and state but no store yet.
func bareCopy(t *testing.T) string {
	t.Helper()
	dir := filepath.Dir(writeDirector(t, directorTOML))
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runEnv runs the CLI with GUILDHALL_COPY set to copyEnv, when it is not empty, and nothing else in the environment.
func runEnv(t *testing.T, copyEnv string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getenv = func(k string) string {
		if k == copyEnvVar {
			return copyEnv
		}
		return ""
	}
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

// The copy form reads the same store as naming the copy's store directly, and task new takes the copy's config.
func TestCopySelectsStoreAndConfig(t *testing.T) {
	dir := newCopy(t)
	code, _, stderr := runEnv(t, "", "task", "new", "--copy", dir, "--repo", "app", "--title", "From the copy", "t-1")
	if code != 0 {
		t.Fatalf("task new --copy: exit %d, stderr %s", code, stderr)
	}
	if got, want := readTask(t, filepath.Join(dir, "director.store"), "app", "t-1").ConfigDigest, configDigest(t, filepath.Join(dir, "config.toml")); got != want {
		t.Errorf("config digest %s, want the copy's %s", got, want)
	}
	_, want, _ := runTask(t, "list", "--store", filepath.Join(dir, "director.store"), "--json")
	code, got, stderr := runEnv(t, "", "task", "list", "--copy", dir, "--json")
	if code != 0 || got != want {
		t.Errorf("task list --copy: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
	// An explicit flag naming the copy's own file agrees with it.
	code, got, stderr = runEnv(t, "", "task", "list", "--copy", dir, "--store", filepath.Join(dir, ".", "director.store"), "--json")
	if code != 0 || got != want {
		t.Errorf("task list --copy --store <copy's>: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
}

// GUILDHALL_COPY selects the copy when --copy is absent; --copy overrides it.
func TestCopyEnvironment(t *testing.T) {
	dir := newCopy(t)
	other := newCopy(t)
	if code, _, stderr := runEnv(t, "", "task", "new", "--copy", dir, "--repo", "app", "--title", "Only in dir", "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, stderr %s", code, stderr)
	}
	_, inDir, _ := runEnv(t, "", "task", "list", "--copy", dir, "--json")
	_, inOther, _ := runEnv(t, "", "task", "list", "--copy", other, "--json")
	if inDir == inOther {
		t.Fatalf("copies list the same tasks: %q", inDir)
	}
	if code, got, stderr := runEnv(t, dir, "task", "list", "--json"); code != 0 || got != inDir {
		t.Errorf("GUILDHALL_COPY: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, inDir)
	}
	if code, got, stderr := runEnv(t, dir, "task", "list", "--copy", other, "--json"); code != 0 || got != inOther {
		t.Errorf("--copy over GUILDHALL_COPY: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, inOther)
	}
}

// An explicit --store, --config or --state outside the copy given with --copy is a usage error.
func TestCopyDisagreementRefused(t *testing.T) {
	dir := newCopy(t)
	elsewhere := t.TempDir()
	outside := filepath.Join(elsewhere, "director.store")
	for _, tc := range []struct {
		name, env, flag string
		args            []string
	}{
		{"store", "", "--store", []string{"task", "list", "--copy", dir, "--store", outside, "--json"}},
		{"store doctor path", "", "store", []string{"store", "doctor", "--copy", dir, outside}},
		{"config", "", "--config", []string{"task", "new", "--copy", dir, "--config", filepath.Join(elsewhere, "config.toml"), "--repo", "app", "--title", "T", "t-1"}},
		{"state", "", "--state", []string{"artisan", "status", "--copy", dir, "--state", elsewhere, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"}},
		{"advance state", "", "--state", []string{"task", "start", "--copy", dir, "--state", filepath.Join(dir, "state", "x"), "--repo", "app", "t-1"}},
	} {
		code, stdout, stderr := runEnv(t, tc.env, tc.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tc.flag) || !strings.Contains(stderr, "copy") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want a usage error naming %s and the copy", tc.name, code, stdout, stderr, tc.flag)
		}
	}
}

// An explicit path flag overrides GUILDHALL_COPY entirely, so a launched child given another copy's paths keeps them.
func TestCopyEnvironmentOverriddenByPathFlag(t *testing.T) {
	envCopy := newCopy(t)
	given := newCopy(t)
	if code, _, stderr := runEnv(t, "", "task", "new", "--copy", given, "--repo", "app", "--title", "Only in given", "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, stderr %s", code, stderr)
	}
	_, want, _ := runEnv(t, "", "task", "list", "--copy", given, "--json")
	if code, got, stderr := runEnv(t, envCopy, "task", "list", "--store", filepath.Join(given, "director.store"), "--json"); code != 0 || got != want {
		t.Errorf("--store over GUILDHALL_COPY: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
	code, _, stderr := runEnv(t, envCopy, "artisan", "status", "--store", filepath.Join(given, "director.store"), "--state", filepath.Join(given, "state"),
		"--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1")
	if code == 2 || strings.Contains(stderr, "copy") {
		t.Errorf("artisan status --store --state over GUILDHALL_COPY: exit %d, stderr %q; want no usage error about the copy", code, stderr)
	}
}

// An inherited GUILDHALL_COPY must not change meaning with the working directory, so it must be absolute.
func TestCopyEnvironmentMustBeAbsolute(t *testing.T) {
	code, stdout, stderr := runEnv(t, "relative-copy", "task", "list", "--json")
	if code != 2 || stdout != "" || !strings.Contains(stderr, copyEnvVar) || !strings.Contains(stderr, "absolute") {
		t.Errorf("exit %d, stdout %q, stderr %q; want a usage error naming %s", code, stdout, stderr, copyEnvVar)
	}
}

// Every store-taking command accepts --copy and GUILDHALL_COPY: against a copy without a store each reaches the
// store, refuses and names store init.
func TestCopyEveryStoreCommandNamesStoreInit(t *testing.T) {
	dir := bareCopy(t)
	result := filepath.Join(t.TempDir(), "result.json")
	writeFile(t, result, "{}")
	attempt := []string{"--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"}
	for _, args := range [][]string{
		{"task", "new", "--repo", "app", "--title", "T", "t-1"},
		{"task", "show", "--repo", "app", "--json", "t-1"},
		{"task", "list", "--json"},
		{"task", "timeline", "--repo", "app", "--json", "t-1"},
		{"task", "files", "--repo", "app", "--json", "t-1"},
		{"task", "finding", "rule", "--repo", "app", "--outcome", "o", "--finding", "f", "--move", "defer", "--reason", "r", "t-1"},
		{"task", "review", "--full", "--repo", "app", "t-1"},
		{"task", "start", "--repo", "app", "t-1"},
		append([]string{"step", "retry"}, attempt...),
		{"decide", "list"},
		{"decide", "show", "--repo", "app", "--task", "t-1", "--decision", "d"},
		{"messages", "list", "director"},
		{"messages", "read", "m-1"},
		{"workflow", "approvals", "--json"},
		{"workflow", "approve", "--repo", "app", "--words-file", result},
		append([]string{"artisan", "status"}, attempt...),
		{"store", "doctor"},
	} {
		want := "guildhall store init " + filepath.Join(dir, "director.store")
		for _, form := range []struct {
			name, env string
			args      []string
		}{
			{"--copy", "", append(append([]string{}, args...), "--copy", dir)},
			{copyEnvVar, dir, args},
		} {
			code, stdout, stderr := runEnv(t, form.env, form.args...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, want) {
				t.Errorf("%q with %s: exit %d, stdout %q, stderr %q; want a refusal naming %q", args, form.name, code, stdout, stderr, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "director.store")); !os.IsNotExist(err) {
		t.Errorf("a command created the copy's store: %v", err)
	}
	// step done reads its result before the store, so it shows its state came from the copy by naming the
	// attempt's result file there.
	want := filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(filepath.Join(dir, "state")), artisan.ResultFile)
	code, _, stderr := runEnv(t, dir, append([]string{"step", "done", "--result", result}, attempt...)...)
	if code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("step done: exit %d, stderr %q; want a refusal naming %s", code, stderr, want)
	}
}

// Without a copy the path flags stay required.
func TestNoCopyKeepsFlagsRequired(t *testing.T) {
	for _, args := range [][]string{
		{"task", "list", "--json"},
		{"task", "start", "--repo", "app", "t-1"},
		{"decide", "list"},
		{"messages", "list", "director"},
		{"workflow", "approvals"},
		{"artisan", "status", "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"},
		{"store", "doctor"},
	} {
		if code, _, stderr := runEnv(t, "", args...); code != 2 || !strings.Contains(strings.ToLower(stderr), "usage:") {
			t.Errorf("%q: exit %d, stderr %q; want a usage error", args, code, stderr)
		}
	}
}
