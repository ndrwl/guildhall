package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

// ciLogLines is how many lines the fixture job log has: more than a 64 KiB ci_log_limit keeps.
const ciLogLines = 2000

// failedValidate is a validate copy whose task app/t-1 has recorded a failed validate result for job 123 of check
// ci, under a ci_log_limit of 64 KiB, and for the commit status lint.
func failedValidate(t *testing.T) (string, runDirector, *forgetest.Fake) {
	t.Helper()
	f := forgetest.NewFake()
	dir, d := validateCopy(t, f)
	b, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, d.config, strings.Replace(string(b), "[local]", "[local]\nci_log_limit = \"64KiB\"\nchecks_refresh = \"0s\"", 1))
	var log strings.Builder
	log.WriteString("2026-10-29T10:00:00.0000000Z ##[group]Run go test ./...\n")
	for i := range ciLogLines {
		fmt.Fprintf(&log, "2026-10-29T10:00:01.0000000Z line %04d %s\n", i, strings.Repeat("x", 40))
	}
	log.WriteString("2026-10-29T10:00:02.0000000Z ##[error]Process completed with exit code 1.\n")
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	f.SetChecks(remote, head, forge.Check{Name: "ci", State: forge.CheckFailed, ID: "CR_1", Job: "123"}, forge.Check{Name: "lint", State: forge.CheckFailed, ID: "SC_1"})
	f.SetLog("123", log.String())
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || strings.TrimSpace(out) != `{"wait":"director","step":"validate"}` {
		t.Fatalf("task start = %d, %s, %s; want the failed result waiting for the Director", code, out, stderr)
	}
	return dir, d, f
}

// ciRun runs guildhall ci log in this process over d's forge, with env as the environment.
func ciRun(d runDirector, env map[string]string, args ...string) (int, string, string) {
	app, stdout, stderr := newTestApp(nil, nil)
	app.InstallBaseline = baseline.Install
	app.Forge = d.forge
	app.Getenv = func(k string) string { return env[k] }
	code := app.Run(append([]string{"ci", "log"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestCILogLimitReachesValidate: the advance gives validate the copy's ci_log_limit, so the failed result's
// excerpt is of a log cut to it, and the logs name the command; the commit status has no job or command.
func TestCILogLimitReachesValidate(t *testing.T) {
	_, d, f := failedValidate(t)
	data, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t-1", "validate-1", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Logs []map[string]any `json:"logs"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Logs) != 2 || v.Logs[0]["check"] != "ci" || v.Logs[0]["job"] != "123" || v.Logs[0]["command"] != "guildhall ci log app/t-1 123" ||
		v.Logs[0]["dropped"].(float64) <= 0 || !strings.Contains(v.Logs[0]["excerpt"].(string), "##[error]") ||
		v.Logs[1]["check"] != "lint" || v.Logs[1]["unavailable"] != "not a GitHub Actions job" || v.Logs[1]["command"] != nil {
		t.Errorf("logs = %v", v.Logs)
	}
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "ci-logs", "123.log"))
	if err != nil || len(cached) > 64<<10+100 || !strings.HasPrefix(string(cached), "[guildhall: ") {
		t.Errorf("cached log is %d bytes, %v; want it within 64 KiB with its dropped header", len(cached), err)
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want 1", n)
	}
	code, out, stderr := d.run(t, "task", "files", "--store", d.store, "--repo", "app", "--json", "t-1")
	if code != 0 || !strings.Contains(out, "validate-1-result") || strings.Contains(out, "ci-logs") || strings.Contains(out, "123.log") {
		t.Errorf("task files = %d, %s, %s; want no cached log among the documents", code, out, stderr)
	}
}

// TestCILog: guildhall ci log serves a recorded failed job's log from the cache, by job id or check name, with
// no forge call and while the task lock is held; fetches it again once the cache is pruned; and prints its tail
// or writes it to a file.
func TestCILog(t *testing.T) {
	dir, d, f := failedValidate(t)
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "ci-logs", "123.log"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := artisan.LockAttempt(context.Background(), filepath.Join(d.state, "app", "t-1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, arg := range []string{"123", "ci"} {
		if code, out, stderr := ciRun(d, nil, "--copy", dir, "app/t-1", arg); code != 0 || out != string(cached) {
			t.Errorf("ci log %s = %d, %d bytes, %s; want the cached log", arg, code, len(out), stderr)
		}
	}
	if code, out, stderr := ciRun(d, map[string]string{"GUILDHALL_COPY": dir}, "app/t-1", "123"); code != 0 || out != string(cached) {
		t.Errorf("ci log with GUILDHALL_COPY = %d, %d bytes, %s", code, len(out), stderr)
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want only validate's", n)
	}

	code, out, stderr := ciRun(d, nil, "--copy", dir, "--tail", "2", "app/t-1", "ci")
	last := fmt.Sprintf("2026-10-29T10:00:01.0000000Z line %04d %s\n2026-10-29T10:00:02.0000000Z ##[error]Process completed with exit code 1.\n", ciLogLines-1, strings.Repeat("x", 40))
	if code != 0 || out != last {
		t.Errorf("ci log --tail 2 = %d, %q, %s; want only the last 2 lines, with no dropped-bytes line", code, out, stderr)
	}
	code, out, stderr = ciRun(d, nil, "--copy", dir, "--tail", strconv.Itoa(2*ciLogLines), "app/t-1", "ci")
	if code != 0 || out != string(cached) {
		t.Errorf("ci log --tail past the kept lines = %d, %d bytes, %s; want the whole cached log, its dropped-bytes line first", code, len(out), stderr)
	}
	file := filepath.Join(t.TempDir(), "ci.log")
	code, out, stderr = ciRun(d, nil, "--copy", dir, "--out", file, "app/t-1", "123")
	if data, err := os.ReadFile(file); code != 0 || err != nil || string(data) != string(cached) || !strings.Contains(out, file) ||
		!strings.Contains(out, fmt.Sprintf("%d bytes,", len(cached))) || !strings.Contains(out, "dropped") {
		t.Errorf("ci log --out = %d, %q, %s; file %d bytes, %v", code, out, stderr, len(data), err)
	}

	if err := os.RemoveAll(filepath.Join(d.state, "app", "t-1", "ci-logs")); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := ciRun(d, nil, "--copy", dir, "app/t-1", "123"); code != 0 || out != string(cached) || f.LogCalls() != 2 {
		t.Errorf("ci log after pruning = %d, %d bytes, %s, %d forge reads; want the log fetched again", code, len(out), stderr, f.LogCalls())
	}
}

// TestCILogRefusals: a job or check the task did not record failing as an Actions job is a usage error listing
// the task's failed checks; an expired log is unavailable, naming the excerpt that remains.
func TestCILogRefusals(t *testing.T) {
	dir, d, f := failedValidate(t)
	for _, arg := range []string{"999", "lint", "build"} {
		code, out, stderr := ciRun(d, nil, "--copy", dir, "app/t-1", arg)
		if code != 2 || out != "" || !strings.Contains(stderr, "123\tci") || !strings.Contains(stderr, "-\tlint") {
			t.Errorf("ci log %s = %d, %q, %s; want exit 2 listing the failed checks", arg, code, out, stderr)
		}
	}
	for _, args := range [][]string{{"app/t-1"}, {"app", "123"}, {"--tail", "-1", "app/t-1", "123"}} {
		if code, _, _ := ciRun(d, nil, append([]string{"--copy", dir}, args...)...); code != 2 {
			t.Errorf("ci log %q = %d, want 2", args, code)
		}
	}
	if code, _, stderr := ciRun(d, nil, "app/t-1", "123"); code != 2 || !strings.Contains(stderr, "GUILDHALL_COPY") {
		t.Errorf("ci log without a copy = %d, %s; want 2", code, stderr)
	}

	if err := os.RemoveAll(filepath.Join(d.state, "app", "t-1", "ci-logs")); err != nil {
		t.Fatal(err)
	}
	f.SetLogError("123", &forge.LogUnavailableError{Reason: "gh: Gone (HTTP 410)", Expired: true})
	code, out, stderr := ciRun(d, nil, "--copy", dir, "app/t-1", "ci")
	result := filepath.Join(d.state, "documents", "tasks", "app", "t-1", "validate-1", "result.json")
	if want := "guildhall: GitHub no longer keeps the log of job 123 (expired); only the excerpt in " + result + " remains\n"; code != 1 || out != "" || stderr != want {
		t.Errorf("ci log of an expired log = %d, %q, %q; want 1 and %q", code, out, stderr, want)
	}
	f.SetLogError("123", &forge.LogUnavailableError{Reason: "gh: Forbidden (HTTP 403)"})
	if code, _, stderr := ciRun(d, nil, "--copy", dir, "app/t-1", "ci"); code != 1 || stderr != "guildhall: the log of job 123 is unavailable: gh: Forbidden (HTTP 403)\n" {
		t.Errorf("ci log of a forbidden log = %d, %q", code, stderr)
	}
}
