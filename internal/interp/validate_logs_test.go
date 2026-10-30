package interp_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// jobLog is a failed GitHub Actions job's log, whose excerpt is its go test step.
const jobLog = "2026-10-29T10:00:00.0000000Z ##[group]Run go build ./...\n2026-10-29T10:00:01.0000000Z ##[endgroup]\n" +
	"2026-10-29T10:00:02.0000000Z ##[group]Run go test ./...\n2026-10-29T10:00:03.0000000Z --- FAIL: TestX\n" +
	"2026-10-29T10:00:04.0000000Z ##[error]Process completed with exit code 1.\n"

func actionsCheck(name, job string, state forge.CheckState) forge.Check {
	return forge.Check{Name: name, State: state, URL: "https://ci.invalid/" + job, ID: "CR_" + job, Job: job}
}

func command(job string) string { return "guildhall ci log app/t7 " + job }

func available(name, job string, dropped int64) logEntry {
	excerpt := cilog.Excerpt([]byte(jobLog))
	return logEntry{Check: name, Job: job, Excerpt: &excerpt, Dropped: &dropped, Command: command(job)}
}

// recordedDigest is the digest of the recorded document name.
func (e *env) recordedDigest(t *testing.T, name string) string {
	t.Helper()
	d, ok := e.documents(t)[name]
	if !ok {
		t.Fatalf("%s not recorded", name)
	}
	return "sha256:" + d.SHA256
}

// TestValidateLogs: a failed round puts each failed check's log excerpt, or why it has none, into its result, in
// the order of the checks, and the build gets that result as its feedback; a passed check gets no entry.
func TestValidateLogs(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.eng.CILogLimit = 5 << 20
			checks := []forge.Check{actionsCheck("build", "200", forge.CheckPassed), {Name: "lint", State: forge.CheckFailed, ID: "SC_lint"},
				actionsCheck("test (macos)", "124", forge.CheckFailed), actionsCheck("test (ubuntu)", "123", forge.CheckFailed)}
			f.SetChecks(repoRemote, head, checks...)
			f.SetLog("123", jobLog)
			f.SetLogError("124", &forge.LogUnavailableError{Reason: "gh: Forbidden (HTTP 403)"})
			w := result(head, "failed", checks, "lint", "test (macos)", "test (ubuntu)")
			w.Logs = []logEntry{{Check: "lint", Unavailable: "not a GitHub Actions job"},
				{Check: "test (macos)", Job: "124", Unavailable: "gh: Forbidden (HTTP 403)", Command: command("124")},
				available("test (ubuntu)", "123", 0)}
			e.script.Expect(buildExp(t, 2, head2, w.digest(t)))
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
				t.Errorf("validate-1-result is %s, want %s: %+v", got, w.digest(t), e.pullRequestDoc(t, "validate-1-result"))
			}
			if !strings.Contains(*w.Logs[2].Excerpt, "--- FAIL: TestX") || strings.Contains(*w.Logs[2].Excerpt, "go build") {
				t.Errorf("excerpt = %q, want the go test step alone", *w.Logs[2].Excerpt)
			}
			if data, err := os.ReadFile(filepath.Join(e.eng.StateDir, "app", "t7", "ci-logs", "123.log")); err != nil || string(data) != jobLog {
				t.Errorf("cached log = %q, %v", data, err)
			}
			if n := f.LogCalls(); n != 2 {
				t.Errorf("forge read %d logs, want 2", n)
			}
		})
	}
}

// TestValidateLogsCommandNamesTheCopy: an advance from a copy records the command with --copy, quoted for a POSIX
// shell, since the Artisan runs it under the copy's state/, where copy discovery finds no copy.
func TestValidateLogsCommandNamesTheCopy(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	e.eng.CILogLimit = 5 << 20
	e.eng.Copy = "/copies/o'neil copy"
	checks := []forge.Check{actionsCheck("test (ubuntu)", "123", forge.CheckFailed)}
	f.SetChecks(repoRemote, head, checks...)
	f.SetLog("123", jobLog)
	w := result(head, "failed", checks, "test (ubuntu)")
	l := available("test (ubuntu)", "123", 0)
	l.Command = `guildhall ci log --copy '/copies/o'\''neil copy' app/t7 123`
	w.Logs = []logEntry{l}
	e.script.Expect(buildExp(t, 2, head2, w.digest(t)))
	want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
		t.Errorf("validate-1-result is %s, want %s: %+v", got, w.digest(t), e.pullRequestDoc(t, "validate-1-result"))
	}
}

// TestValidateLogsCapped:a round fetches at most 10 logs, in the order of the checks; the rest name the cap and
// keep their command, as does every failed check when log fetching is off or the forge gives no logs.
func TestValidateLogsCapped(t *testing.T) {
	var checks []forge.Check
	for i := range 12 {
		job := fmt.Sprint(300 + i)
		checks = append(checks, actionsCheck(fmt.Sprintf("job-%02d", i), job, forge.CheckFailed))
	}
	for _, tc := range []struct {
		name   string
		limit  int64
		plain  bool
		calls  int
		reason func(i int) string
	}{
		{name: "more than 10", limit: 1 << 20, calls: 10, reason: func(i int) string {
			if i < 10 {
				return ""
			}
			return "log limit of 10 per round reached"
		}},
		{name: "fetching off", limit: 0, reason: func(int) string { return "log fetching is off" }},
		{name: "no CheckLogReader", limit: 1 << 20, plain: true, reason: func(int) string { return "forge does not provide logs" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := editValidate(t, func(s *workflow.Step) { s.On = nil })
			e, f := newValidateEnvDefinition(t, backends[0], def)
			e.eng.CILogLimit = tc.limit
			if tc.plain {
				e.eng.Forge = plainForge{f}
			}
			f.SetChecks(repoRemote, head, checks...)
			w := result(head, "failed", checks)
			w.Logs = nil
			for i, c := range checks {
				f.SetLog(c.Job, jobLog)
				w.Failing = append(w.Failing, c.Name)
				l := logEntry{Check: c.Name, Job: c.Job, Unavailable: tc.reason(i), Command: command(c.Job)}
				if l.Unavailable == "" {
					l = available(c.Name, c.Job, 0)
				}
				w.Logs = append(w.Logs, l)
			}
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
				t.Errorf("validate-1-result is %s, want %s: %+v", got, w.digest(t), e.pullRequestDoc(t, "validate-1-result"))
			}
			if n := f.LogCalls(); n != tc.calls {
				t.Errorf("forge read %d logs, want %d", n, tc.calls)
			}
		})
	}
}

// plainForge is a forge without CheckLogReader.
type plainForge struct{ interp.Forge }

// TestValidateLogsOnlyForRecordedRounds: pending checks and a failed read with no new failed run fetch no log; a
// passed round has no logs, so its result is as it was.
func TestValidateLogsOnlyForRecordedRounds(t *testing.T) {
	def := editValidate(t, func(s *workflow.Step) { s.On = nil })
	e, f := newValidateEnvDefinition(t, backends[0], def)
	e.eng.CILogLimit = 1 << 20
	f.SetLog("123", jobLog)
	f.SetChecks(repoRemote, head, actionsCheck("test", "123", forge.CheckPending))
	want(t, e.toValidate(t), waitChecks)
	failed := []forge.Check{actionsCheck("test", "123", forge.CheckFailed)}
	f.SetChecks(repoRemote, head, failed...)
	director := interp.Next{Wait: interp.WaitDirector, Step: "validate"}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want 1: only the recorded round fetches", n)
	}
	passed := []forge.Check{actionsCheck("test", "125", forge.CheckPassed)}
	f.SetChecks(repoRemote, head, passed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if got, w := e.recordedDigest(t, "validate-2-result"), result(head, "passed", passed).digest(t); got != w {
		t.Errorf("passed validate-2-result is %s, want %s", got, w)
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge read %d logs, want 1", n)
	}
}

// TestValidateLogsReplay: a crash before the failed result is recorded leaves the log in the cache, so the next
// advance computes the same excerpt without fetching again; a cache pruned in between is fetched again.
func TestValidateLogsReplay(t *testing.T) {
	for _, pruned := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "pruned"}[pruned], func(t *testing.T) {
			def := editValidate(t, func(s *workflow.Step) { s.On = nil })
			e, f := newValidateEnvDefinition(t, backends[0], def)
			e.eng.CILogLimit = 1 << 20
			checks := []forge.Check{actionsCheck("test", "123", forge.CheckFailed)}
			f.SetChecks(repoRemote, head, checks...)
			f.SetLog("123", jobLog)
			fp := &failPut{Records: e.eng.Records, name: "validate-1-result"}
			e.eng.Records = fp
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); !errors.Is(err, errInjected) {
				t.Fatalf("advance = %v, want the injected failure", err)
			}
			if pruned {
				if err := os.RemoveAll(filepath.Join(e.eng.StateDir, "app", "t7", "ci-logs")); err != nil {
					t.Fatal(err)
				}
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			w := result(head, "failed", checks, "test")
			w.Logs = []logEntry{available("test", "123", 0)}
			if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
				t.Errorf("validate-1-result is %s, want %s", got, w.digest(t))
			}
			if n, want := f.LogCalls(), map[bool]int{false: 1, true: 2}[pruned]; n != want {
				t.Errorf("forge read %d logs, want %d", n, want)
			}
		})
	}
}
