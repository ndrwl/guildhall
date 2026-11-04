package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// ciLogLines is how many lines the fixture job log has: more than a 64 KiB ci_log_limit keeps.
const ciLogLines = 2000

// failedValidate is a validate copy whose task app/t1 has recorded a failed validate result for job 123 of check
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
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t1")...)...); code != 0 || strings.TrimSpace(out) != `{"wait":"director","step":"validate"}` {
		t.Fatalf("task start = %d, %s, %s; want the failed result waiting for the Director", code, out, stderr)
	}
	return dir, d, f
}

// ciRun runs guildhall ci log in this process over d's forge, from the working directory wd, a new empty directory
// when wd is empty.
func ciRun(t *testing.T, d runDirector, wd string, args ...string) (int, string, string) {
	t.Helper()
	if wd == "" {
		wd = t.TempDir()
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.InstallBaseline = baseline.Install
	app.Forge = d.forge
	app.Getwd = func() (string, error) { return wd, nil }
	code := app.Run(append([]string{"ci", "log"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestCILogLimitReachesValidate: the advance gives validate the copy's ci_log_limit, so the failed result's
// excerpt is of a log cut to it, and the logs name the command; the commit status has no job or command.
func TestCILogLimitReachesValidate(t *testing.T) {
	_, d, f := failedValidate(t)
	data, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t1", "validate-1", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Logs []map[string]any `json:"logs"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Logs) != 2 || v.Logs[0]["check"] != "ci" || v.Logs[0]["job"] != "123" || v.Logs[0]["command"] != "guildhall ci log app/t1 123" ||
		v.Logs[0]["dropped"].(float64) <= 0 || !strings.Contains(v.Logs[0]["excerpt"].(string), "##[error]") ||
		v.Logs[1]["check"] != "lint" || v.Logs[1]["unavailable"] != "not a GitHub Actions job" || v.Logs[1]["command"] != nil {
		t.Errorf("logs = %v", v.Logs)
	}
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t1", "ci-logs", "123.log"))
	if err != nil || len(cached) > 64<<10+100 || !strings.HasPrefix(string(cached), "[guildhall: ") {
		t.Errorf("cached log is %d bytes, %v; want it within 64 KiB with its dropped header", len(cached), err)
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want 1", n)
	}
	code, out, stderr := d.run(t, "task", "files", "--store", d.store, "--repo", "app", "--json", "t1")
	if code != 0 || !strings.Contains(out, "validate-1-result") || strings.Contains(out, "ci-logs") || strings.Contains(out, "123.log") {
		t.Errorf("task files = %d, %s, %s; want no cached log among the documents", code, out, stderr)
	}
}

// TestCILog: guildhall ci log serves a recorded failed job's log from the cache, by job id or check name, with
// no forge call and while the task lock is held; fetches it again once the cache is pruned; and prints its tail
// or writes it to a file.
func TestCILog(t *testing.T) {
	dir, d, f := failedValidate(t)
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t1", "ci-logs", "123.log"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := artisan.LockAttempt(context.Background(), filepath.Join(d.state, "app", "t1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, arg := range []string{"123", "ci"} {
		if code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", arg); code != 0 || out != string(cached) {
			t.Errorf("ci log %s = %d, %d bytes, %s; want the cached log", arg, code, len(out), stderr)
		}
	}
	if code, out, stderr := ciRun(t, d, dir, "app/t1", "123"); code != 0 || out != string(cached) {
		t.Errorf("ci log in the copy = %d, %d bytes, %s", code, len(out), stderr)
	}
	// An Artisan runs it in its working directory under the copy's state/, where discovery finds no copy, so the
	// command validate records names the copy.
	work := filepath.Join(d.state, "app", "t1", "work")
	if code, _, stderr := ciRun(t, d, work, "app/t1", "123"); code != 2 || !strings.Contains(stderr, noCopyLine(work)) {
		t.Errorf("ci log under state/ = %d, %s; want 2 and no copy found", code, stderr)
	}
	if code, out, stderr := ciRun(t, d, work, "--copy", dir, "app/t1", "123"); code != 0 || out != string(cached) {
		t.Errorf("ci log --copy under state/ = %d, %d bytes, %s", code, len(out), stderr)
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want only validate's", n)
	}

	code, out, stderr := ciRun(t, d, "", "--copy", dir, "--tail", "2", "app/t1", "ci")
	last := fmt.Sprintf("2026-10-29T10:00:01.0000000Z line %04d %s\n2026-10-29T10:00:02.0000000Z ##[error]Process completed with exit code 1.\n", ciLogLines-1, strings.Repeat("x", 40))
	if code != 0 || out != last {
		t.Errorf("ci log --tail 2 = %d, %q, %s; want only the last 2 lines, with no dropped-bytes line", code, out, stderr)
	}
	code, out, stderr = ciRun(t, d, "", "--copy", dir, "--tail", strconv.Itoa(2*ciLogLines), "app/t1", "ci")
	if code != 0 || out != string(cached) {
		t.Errorf("ci log --tail past the kept lines = %d, %d bytes, %s; want the whole cached log, its dropped-bytes line first", code, len(out), stderr)
	}
	file := filepath.Join(t.TempDir(), "ci.log")
	code, out, stderr = ciRun(t, d, "", "--copy", dir, "--out", file, "app/t1", "123")
	if data, err := os.ReadFile(file); code != 0 || err != nil || string(data) != string(cached) || !strings.Contains(out, file) ||
		!strings.Contains(out, fmt.Sprintf("%d bytes,", len(cached))) || !strings.Contains(out, "dropped") {
		t.Errorf("ci log --out = %d, %q, %s; file %d bytes, %v", code, out, stderr, len(data), err)
	}

	if err := os.RemoveAll(filepath.Join(d.state, "app", "t1", "ci-logs")); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", "123"); code != 0 || out != string(cached) || f.LogCalls() != 2 {
		t.Errorf("ci log after pruning = %d, %d bytes, %s, %d forge reads; want the log fetched again", code, len(out), stderr, f.LogCalls())
	}
}

// TestCILogRefusals: a job or check the task did not record failing as an Actions job is a usage error listing
// the task's failed checks; an expired log is unavailable, naming the excerpt that remains.
func TestCILogRefusals(t *testing.T) {
	dir, d, f := failedValidate(t)
	for _, arg := range []string{"999", "lint", "build"} {
		code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", arg)
		if code != 2 || out != "" || !strings.Contains(stderr, "123\tci") || !strings.Contains(stderr, "-\tlint") {
			t.Errorf("ci log %s = %d, %q, %s; want exit 2 listing the failed checks", arg, code, out, stderr)
		}
	}
	for _, args := range [][]string{{"app/t1"}, {"app", "123"}, {"--tail", "-1", "app/t1", "123"}} {
		if code, _, _ := ciRun(t, d, "", append([]string{"--copy", dir}, args...)...); code != 2 {
			t.Errorf("ci log %q = %d, want 2", args, code)
		}
	}
	if code, _, stderr := ciRun(t, d, "", "app/t1", "123"); code != 2 || !strings.Contains(stderr, "no Guildhall copy found from ") {
		t.Errorf("ci log without a copy = %d, %s; want 2", code, stderr)
	}

	if err := os.RemoveAll(filepath.Join(d.state, "app", "t1", "ci-logs")); err != nil {
		t.Fatal(err)
	}
	f.SetLogError("123", &forge.LogUnavailableError{Reason: "gh: Gone (HTTP 410)", Expired: true})
	code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", "ci")
	result := filepath.Join(d.state, "documents", "tasks", "app", "t1", "validate-1", "result.json")
	if want := "guildhall: GitHub no longer keeps the log of job 123 (expired); only the excerpt in " + result + " remains\n"; code != 1 || out != "" || stderr != want {
		t.Errorf("ci log of an expired log = %d, %q, %q; want 1 and %q", code, out, stderr, want)
	}
	f.SetLogError("123", &forge.LogUnavailableError{Reason: "gh: Forbidden (HTTP 403)"})
	if code, _, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", "ci"); code != 1 || stderr != "guildhall: the log of job 123 is unavailable: gh: Forbidden (HTTP 403)\n" {
		t.Errorf("ci log of a forbidden log = %d, %q", code, stderr)
	}
}

// putDocument writes data as task t1's document name at path under d's documents and records it.
func putDocument(t *testing.T, d runDirector, name, path string, data []byte) {
	t.Helper()
	file := filepath.Join(d.state, "documents", filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, string(data))
	s, err := store.Open(d.store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.PutDocument("doc:t1:"+name, store.DocumentRef{Repository: "app", TaskID: "t1", Name: name, Path: path, SHA256: digest.Of(data).Hex()}); err != nil {
		t.Fatal(err)
	}
}

// TestCILogRerunRecord: the failed job of a rerun record, which no result's logs name, is served by job id, so the
// log command of a flake message works (d210), whether the result naming the record passed or failed; a check name
// still means the latest failed result's job first.
func TestCILogRerunRecord(t *testing.T) {
	dir, d, f := failedValidate(t)
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	rerun := func(round int, verdict, job string) {
		name := fmt.Sprintf("validate-rerun-%d", round)
		rec, err := json.Marshal(map[string]any{"schema": "guildhall.check-rerun/1", "step": "validate", "head": head, "check": "ci",
			"failed_run": "CR_" + job, "job": job, "conclusion": "cancelled", "tests": []string{}, "requested_at": "2026-11-04T10:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		putDocument(t, d, name, "tasks/app/t1/"+name+"/rerun.json", rec)
		result, err := json.Marshal(map[string]any{"schema": "guildhall.validation-result/1", "verdict": verdict, "remote": remote, "head": head,
			"reruns": []map[string]any{{"check": "ci", "record": name, "failed_run": "CR_" + job}}})
		if err != nil {
			t.Fatal(err)
		}
		putDocument(t, d, fmt.Sprintf("validate-%d-result", round), fmt.Sprintf("tasks/app/t1/validate-%d/result.json", round), result)
		f.SetLog(job, "log of job "+job+"\n")
	}
	rerun(2, "passed", "456")
	rerun(3, "failed", "789")
	for _, job := range []string{"456", "789"} {
		if code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", job); code != 0 || out != "log of job "+job+"\n" {
			t.Errorf("ci log %s = %d, %q, %s; want the rerun record's job log", job, code, out, stderr)
		}
	}
	cached, err := os.ReadFile(filepath.Join(d.state, "app", "t1", "ci-logs", "123.log"))
	if err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", "ci"); code != 0 || out != string(cached) {
		t.Errorf("ci log ci = %d, %d bytes, %s; want the failed result's job 123", code, len(out), stderr)
	}
	code, _, stderr := ciRun(t, d, "", "--copy", dir, "app/t1", "999")
	if i, j, k := strings.Index(stderr, "123\tci"), strings.Index(stderr, "789\tci\t(validate-rerun-3)"), strings.Index(stderr, "456\tci\t(validate-rerun-2)"); code != 2 || i < 0 || j < i || k < j {
		t.Errorf("ci log 999 = %d, %s; want the failed result's job, then the rerun records' jobs latest first", code, stderr)
	}
}

// logCommits is a fake forge that records the commit of every log read.
type logCommits struct {
	*forgetest.Fake
	mu      sync.Mutex
	commits []forge.Commit
}

func (f *logCommits) CheckLog(ctx context.Context, c forge.Commit, job string, limit int64) (forge.CheckLog, error) {
	f.mu.Lock()
	f.commits = append(f.commits, c)
	f.mu.Unlock()
	return f.Fake.CheckLog(ctx, c, job, limit)
}

const (
	baselineRemote = "git@github.com:example/app-d80.git"
	baselineMerge  = "89abcdef0123456789abcdef0123456789abcdef"
)

// hallmarkBaseline is failedValidate's copy with a hallmark task app/t2 whose recorded baseline-hallmark lists the
// failed job 7781 of check lint on merge d80's commit, on d80's remote, besides a failed status with no job and a
// passed check; the forge records each log read's commit. It also returns the baseline document's file.
func hallmarkBaseline(t *testing.T) (string, runDirector, *forgetest.Fake, *logCommits, string) {
	t.Helper()
	dir, d, f := failedValidate(t)
	rec := &logCommits{Fake: f}
	d.forge = rec
	newTask(t, d.store, d.config, "app", "Hallmark")
	block, err := json.Marshal(map[string]any{"schema": interp.BaselineJobsSchema, "jobs": []interp.BaselineJob{
		{Task: "d80", Remote: baselineRemote, Head: baselineMerge, Check: "lint", Job: "7781"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc := "# Hallmark baseline for app/t2\n\n## Checks\n\n### d80 `" + baselineMerge + "`\n\n| Check | State | URL | Log |\n|---|---|---|---|\n" +
		"| lint | failed | | `guildhall ci log app/t2 7781` |\n| status | failed | | |\n| test | passed | | |\n\n## Failed jobs\n\n```json\n" + string(block) + "\n```\n"
	file := filepath.Join(d.state, "documents", "tasks", "app", "t2", "baseline-hallmark.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, doc)
	s, err := store.Open(d.store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.PutDocument("doc:t2:baseline", store.DocumentRef{Repository: "app", TaskID: "t2", Name: "baseline-hallmark",
		Path: "tasks/app/t2/baseline-hallmark.md", SHA256: digest.Of([]byte(doc)).Hex()}); err != nil {
		t.Fatal(err)
	}
	f.SetLog("7781", "lint: unused variable x\n")
	return dir, d, f, rec, file
}

// TestCILogBaseline: ci log serves a failed job a hallmark task's baseline lists, by job id or check name, fetched
// once for the merge commit on the merged task's remote into the hallmark task's own cache, and from that cache
// afterwards until it is removed.
func TestCILogBaseline(t *testing.T) {
	dir, d, f, rec, _ := hallmarkBaseline(t)
	reads := f.LogCalls()
	for _, arg := range []string{"7781", "lint"} {
		if code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t2", arg); code != 0 || out != "lint: unused variable x\n" {
			t.Errorf("ci log %s = %d, %q, %s; want the log", arg, code, out, stderr)
		}
	}
	if n := f.LogCalls(); n != reads+1 {
		t.Errorf("forge read %d logs, want 1 then the cache", n-reads)
	}
	if len(rec.commits) != 1 || rec.commits[0].Head != baselineMerge || rec.commits[0].Remote != baselineRemote || rec.commits[0].Repository != appRepo {
		t.Errorf("log read commits = %+v, want d80's merge commit on its remote", rec.commits)
	}
	cache := filepath.Join(d.state, "app", "t2", "ci-logs")
	if data, err := os.ReadFile(filepath.Join(cache, "7781.log")); err != nil || string(data) != "lint: unused variable x\n" {
		t.Errorf("cached log = %q, %v", data, err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := ciRun(t, d, "", "--copy", dir, "app/t2", "7781"); code != 0 || f.LogCalls() != reads+2 {
		t.Errorf("ci log after the cache was removed = %d, %s, %d reads; want the log fetched again", code, stderr, f.LogCalls()-reads)
	}
}

// TestCILogBaselineRefusals: a job another task records, a passed check and a failed check with no job are refused
// with exit 2, listing the task's failed checks, the baseline's among them, and read no log; a baseline whose file
// does not match its recorded digest is an error.
func TestCILogBaselineRefusals(t *testing.T) {
	dir, d, f, _, file := hallmarkBaseline(t)
	reads := f.LogCalls()
	for _, arg := range []string{"123", "ci", "test", "status"} {
		code, out, stderr := ciRun(t, d, "", "--copy", dir, "app/t2", arg)
		if code != 2 || out != "" || !strings.Contains(stderr, "7781\tlint\t(baseline-hallmark)") || strings.Contains(stderr, "123\t") {
			t.Errorf("ci log %s = %d, %q, %s; want exit 2 listing only the baseline's failed check", arg, code, out, stderr)
		}
	}
	if n := f.LogCalls(); n != reads {
		t.Errorf("refusals read %d logs, want none", n-reads)
	}
	writeFile(t, file, "# edited\n")
	if code, _, stderr := ciRun(t, d, "", "--copy", dir, "app/t2", "7781"); code != 1 || !strings.Contains(stderr, "does not match its recorded digest") {
		t.Errorf("ci log over an edited baseline = %d, %s; want the digest error", code, stderr)
	}
}
