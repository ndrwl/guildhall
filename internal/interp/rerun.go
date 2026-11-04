package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The schemas of a rerun record and its outcome.
const (
	rerunSchema        = "guildhall.check-rerun/1"
	rerunOutcomeSchema = "guildhall.check-rerun-outcome/1"
)

// rerunDoc is a rerun record: validate's or deliver's one rerun of a failed GitHub Actions check of one head, written
// once before the forge is asked to re-run its job (docs/interpreter.md#validate); deliver's are those of a rebased
// merge-queue front's checks. It is the flake's evidence: the run
// that failed, its job, and the failing test names read from that job's log.
type rerunDoc struct {
	Schema string `json:"schema"`
	Step   string `json:"step"`
	Head   string `json:"head"`
	Check  string `json:"check"`
	// FailedRun, Job and URL are the failed run's id, Actions job id and link.
	FailedRun string `json:"failed_run"`
	Job       string `json:"job"`
	URL       string `json:"url,omitempty"`
	// Conclusion is how the failed run finished, as forge.Check.Conclusion gives it, such as failure or cancelled;
	// empty when the forge gave none, as in records written before it was kept.
	Conclusion string `json:"conclusion,omitempty"`
	// Tests are the failing test names of the failed job's log, from its "--- FAIL: <Name>" lines only, so a test
	// that was merely running when a go test timeout fired is never named here; Timeout is that timeout, when the
	// log has one; LogUnavailable is why the log could not be read.
	Tests          []string      `json:"tests"`
	Timeout        *rerunTimeout `json:"timeout,omitempty"`
	LogUnavailable string        `json:"log_unavailable,omitempty"`
	RequestedAt    time.Time     `json:"requested_at"`
}

// rerunTimeout is a go test package timeout read from a failed job's log: the duration its panic names, the package
// its FAIL line names, or "", and the tests its "running tests:" block lists, empty when the log's tail lacks them.
type rerunTimeout struct {
	After   string   `json:"after"`
	Package string   `json:"package,omitempty"`
	Running []string `json:"running"`
}

// RerunRecordJob returns the check a rerun record re-ran and the GitHub Actions job of its failed run, whose log
// guildhall ci log serves. A document that is not a rerun record is an error.
func RerunRecordJob(data []byte) (check, job string, err error) {
	var d rerunDoc
	if json.Unmarshal(data, &d) != nil || d.Schema != rerunSchema {
		return "", "", fmt.Errorf("not a %s record", rerunSchema)
	}
	return d.Check, d.Job, nil
}

// rerunOutcomeDoc records that the forge refused or failed the rerun of its record, and why; a rerun the forge
// accepted has none.
type rerunOutcomeDoc struct {
	Schema string `json:"schema"`
	Record string `json:"record"`
	Error  string `json:"error"`
}

// rerunEntry is one check of a validation result that validate or deliver re-ran on its head: in a passed result, a flake,
// with its failed and passing run ids; in a failed result, a check that failed again after its rerun.
type rerunEntry struct {
	Check string `json:"check"`
	// Record names the check's rerun record.
	Record    string `json:"record"`
	FailedRun string `json:"failed_run"`
	PassedRun string `json:"passed_run,omitempty"`
}

// maxRerunTests bounds the test names one rerun record keeps.
const maxRerunTests = 20

// failedTest matches the line go test prints for a failed test, after any timestamp the job log prefixes it with.
var failedTest = regexp.MustCompile(`--- FAIL: (\S+)`)

// failedTests returns the test names of log's "--- FAIL: <Name>" lines, in order and without repeats, at most
// maxRerunTests.
func failedTests(log []byte) []string {
	out := []string{}
	for _, m := range failedTest.FindAllSubmatch(log, -1) {
		if name := string(m[1]); !slices.Contains(out, name) && len(out) < maxRerunTests {
			out = append(out, name)
		}
	}
	return out
}

// The lines of a go test timeout: its panic, the header of the tests it lists, one listed test, and the FAIL line
// of the package, each matched after the job log's timestamp.
var (
	timeoutPanic  = regexp.MustCompile(`^panic: test timed out after (\S+)`)
	runningHeader = regexp.MustCompile(`^\s*running tests:$`)
	runningTest   = regexp.MustCompile(`^\s+(\S+) \([^)]*\)$`)
	failedPackage = regexp.MustCompile(`^FAIL\t(\S+)\t`)
)

// testTimeout returns the go test timeout of log, read from its first "panic: test timed out after <d>" line: the
// duration, the tests of the "running tests:" block that follows it, as printed, in order, without repeats and at
// most maxRerunTests, and the package of the first "FAIL\t<pkg>\t" line after it. A log whose fetched tail lacks the
// block has no running tests; a log with no such panic has no timeout, and nil is returned.
func testTimeout(log []byte) *rerunTimeout {
	lines := strings.Split(string(log), "\n")
	for i := range lines {
		lines[i] = cilog.Timestamp.ReplaceAllString(strings.TrimSuffix(lines[i], "\r"), "")
	}
	for i, l := range lines {
		m := timeoutPanic.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		t := &rerunTimeout{After: m[1], Running: []string{}}
		rest := lines[i+1:]
		if len(rest) > 0 && runningHeader.MatchString(rest[0]) {
			for _, l := range rest[1:] {
				n := runningTest.FindStringSubmatch(l)
				if n == nil {
					break
				}
				if !slices.Contains(t.Running, n[1]) && len(t.Running) < maxRerunTests {
					t.Running = append(t.Running, n[1])
				}
			}
		}
		for _, l := range rest {
			if p := failedPackage.FindStringSubmatch(l); p != nil {
				t.Package = p[1]
				break
			}
		}
		return t
	}
	return nil
}

// text describes t for a flake message: the package that timed out, after how long, and the tests it was running.
func (t *rerunTimeout) text() string {
	var b strings.Builder
	if t.Package != "" {
		fmt.Fprintf(&b, "package %s ", t.Package)
	}
	fmt.Fprintf(&b, "timed out after %s, running ", t.After)
	if len(t.Running) == 0 {
		b.WriteString("tests not in the log tail")
	} else {
		b.WriteString(strings.Join(t.Running, ", "))
	}
	return b.String()
}

// checkHash is a short digest of a check name, for the names and message ids that carry one.
func checkHash(check string) string {
	return digest.Of([]byte(check)).Hex()[:8]
}

// rerunName names the rerun record of check on head h for validate or deliver step: one per step, head and check, never per
// run, so a check that fails again after its rerun is not re-run again. A name too long for a document id is
// hashed whole.
func rerunName(step, h, check string) string {
	name := fmt.Sprintf("%s-rerun-%s%s", step, h[:12], checkHash(check))
	if len(name+"-outcome") > 63 {
		name = "rerun-" + digest.Of([]byte(step + "\n" + h + "\n" + check)).Hex()[:24]
	}
	return name
}

// rerunPolicy is the flaky_checks policy validate and deliver follow for repo: off, as it is, for a forge that cannot re-run a
// job.
func (r *run) rerunPolicy(repo config.Repository) config.FlakyChecks {
	if _, ok := r.e.Forge.(forge.CheckRerunner); !ok {
		return config.FlakyChecksOff
	}
	return repo.FlakyChecksPolicy()
}

// rerunRecord returns check's rerun record on head h of step s, and whether the forge's rerun of it failed, or nil.
func (r *run) rerunRecord(s workflow.Step, h, check string) (*rerunDoc, bool, error) {
	name := rerunName(s.ID, h, check)
	var d rerunDoc
	ok, err := r.readJSONDoc(name, &d)
	if err != nil || !ok {
		return nil, false, err
	}
	if d.Schema != rerunSchema || d.Head != h || d.Check != check {
		return nil, false, fmt.Errorf("document %s is not a %s record of %s at %s", name, rerunSchema, check, h)
	}
	return &d, r.document(name+"-outcome") != nil, nil
}

// putOnce records v as document name at <dir>/<file> unless it is recorded; a file a crash left behind before it was
// recorded is recorded as it is, so a record that holds a time is written once.
func (r *run) putOnce(name, dir, file string, v any) error {
	if r.document(name) != nil {
		return nil
	}
	p := path.Join("tasks", r.task.Repository, r.task.ID, dir, file)
	data, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		if data, err = json.Marshal(v); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: p, SHA256: digest.Of(data).Hex()}, data)
}

// rerunFailed gives the failed round v, about to be recorded for head h of validate or deliver step s, its one rerun
// under repo's flaky_checks policy. A round is re-run only when every failing check is a GitHub Actions job not re-run
// on this head before; the step then waits for every check of the head to finish, sending pendingText as its
// checks-pending message, and re-runs each failing job once, recording its rerun record first, and waits for the new
// runs. While some check of the head is pending, a round with a rerun record on this head that the forge did not
// refuse also waits, eligible or not, so a rerun still running is never counted on a read that lags it. stop reports
// that the step waits, with next, and records nothing; otherwise the round is a real failure, recorded as it is, with
// v.Reruns naming the checks that failed again after their rerun. A rerun the forge refuses is recorded with why and
// nothing more is re-run; the round is a real failure in the same advance, unless an earlier rerun of the round was
// accepted, when the step waits for it and records the failure from a read once it has finished.
func (r *run) rerunFailed(s workflow.Step, repo config.Repository, pr *pullRequestDoc, h string, v *validationDoc, pendingText string) (stop bool, next Next, err error) {
	policy := r.rerunPolicy(repo)
	if policy == config.FlakyChecksOff {
		return false, Next{}, nil
	}
	var failing []forge.Check
	pending := false
	for _, c := range v.Checks {
		switch c.State {
		case forge.CheckFailed:
			failing = append(failing, c)
		case forge.CheckPending:
			pending = true
		}
	}
	eligible := true
	for _, c := range failing {
		d, refused, err := r.rerunRecord(s, h, c.Name)
		if err != nil {
			return false, Next{}, err
		}
		if d != nil && !refused && c.ID != d.FailedRun {
			v.Reruns = append(v.Reruns, rerunEntry{Check: c.Name, Record: rerunName(s.ID, h, c.Name), FailedRun: d.FailedRun})
		}
		// A record whose check still shows the run it failed with is a rerun spent, whether or not its call landed.
		if c.Job == "" || d != nil {
			eligible = false
		}
	}
	if len(v.Reruns) > 0 {
		v.FlakyChecks = policy
	}
	wait := Next{Wait: WaitChecks, Step: s.ID}
	waitPending := func() (bool, Next, error) {
		return true, wait, r.notify(fmt.Sprintf("checks-pending.%s.%s", s.ID, h[:12]), store.MessageChecksPending, pendingText)
	}
	if pending && !eligible {
		// A rerun asked for on this head and not refused may still be running: the round waits for it, whatever
		// state its check shows now, rather than being counted on a read that lags it.
		for _, c := range v.Checks {
			d, refused, err := r.rerunRecord(s, h, c.Name)
			if err != nil {
				return false, Next{}, err
			}
			if d != nil && !refused {
				return waitPending()
			}
		}
	}
	if !eligible {
		return false, Next{}, nil
	}
	if pending {
		// GitHub re-runs no job of a workflow run still in progress: the rerun waits for the head's other checks.
		return waitPending()
	}
	c := forge.Commit{Repository: repo.Path, Remote: pr.Remote, Head: h}
	reader, _ := r.e.Forge.(CheckLogReader)
	rerunner := r.e.Forge.(forge.CheckRerunner)
	dir := cilog.Dir(r.e.StateDir, r.task.Repository, r.task.ID)
	accepted := false
	for i, ch := range failing {
		d := rerunDoc{Schema: rerunSchema, Step: s.ID, Head: h, Check: ch.Name, FailedRun: ch.ID, Job: ch.Job, URL: ch.URL, Conclusion: ch.Conclusion, Tests: []string{}, RequestedAt: r.now().UTC()}
		switch {
		case r.e.CILogLimit <= 0:
			d.LogUnavailable = "log fetching is off"
		case i >= maxRoundLogs:
			d.LogUnavailable = fmt.Sprintf("log limit of %d per round reached", maxRoundLogs)
		default:
			got, err := cilog.Get(r.ctx, reader, dir, c, ch.Job, r.e.CILogLimit)
			if err != nil {
				if r.ctx.Err() != nil {
					return false, Next{}, r.ctx.Err()
				}
				d.LogUnavailable = forge.LogReason(err)
				break
			}
			d.Tests = failedTests(got.Text)
			d.Timeout = testTimeout(got.Text)
		}
		name := rerunName(s.ID, h, ch.Name)
		if err := r.putOnce(name, name, "rerun.json", d); err != nil {
			return false, Next{}, err
		}
		if err := rerunner.RerunJob(r.ctx, c, ch.Job); err != nil {
			if r.ctx.Err() != nil {
				return false, Next{}, r.ctx.Err()
			}
			// The refused check makes the round a real failure, so nothing more is re-run. CI trouble never stalls a
			// task: the failure is recorded now, or once the reruns already accepted have finished.
			out := rerunOutcomeDoc{Schema: rerunOutcomeSchema, Record: name, Error: err.Error()}
			if err := r.putOnce(name+"-outcome", name, "outcome.json", out); err != nil {
				return false, Next{}, err
			}
			if accepted {
				return true, wait, nil
			}
			return false, Next{}, nil
		}
		accepted = true
	}
	return true, wait, nil
}

// passedReruns sets, on the passed result v of head h of validate or deliver step s, the flakes among its checks: each check with a rerun record
// on h that the forge accepted, with its failed and passing run ids, and the policy they were re-run under.
func (r *run) passedReruns(s workflow.Step, repo config.Repository, h string, v *validationDoc) error {
	policy := r.rerunPolicy(repo)
	if policy == config.FlakyChecksOff {
		return nil
	}
	for _, c := range v.Checks {
		d, refused, err := r.rerunRecord(s, h, c.Name)
		if err != nil {
			return err
		}
		if d != nil && !refused && c.ID != d.FailedRun {
			v.Reruns = append(v.Reruns, rerunEntry{Check: c.Name, Record: rerunName(s.ID, h, c.Name), FailedRun: d.FailedRun, PassedRun: c.ID})
		}
	}
	if len(v.Reruns) > 0 {
		v.FlakyChecks = policy
	}
	return nil
}

// platform is the parenthesised part a matrix check's name ends with, such as macos-latest, or "".
var platform = regexp.MustCompile(`\(([^()]+)\)\s*$`)

// notifyFlakes sends, for the passed result v recorded as round id of validate or deliver step s under flaky_checks = "rerun-and-file",
// one flake message per check that passed on its rerun, its text built from the rerun record and the result alone,
// so a replay sends the same message.
func (r *run) notifyFlakes(s workflow.Step, id string, v validationDoc) error {
	if v.Verdict != validationPassed || v.FlakyChecks != config.FlakyChecksRerunAndFile {
		return nil
	}
	for _, e := range v.Reruns {
		var d rerunDoc
		if ok, err := r.readJSONDoc(e.Record, &d); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("validation result %s names rerun record %s, which is not recorded", id, e.Record)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "policy: %s\n", config.FlakyChecksRerunAndFile)
		fmt.Fprintf(&b, "Check %q of head %s on pull request %s failed and then passed on its one rerun (step %s, %s): a flake.\n", d.Check, v.Head, v.URL, s.ID, id)
		if m := platform.FindStringSubmatch(d.Check); m != nil {
			fmt.Fprintf(&b, "platform: %s\n", m[1])
		}
		fmt.Fprintf(&b, "job: %s\n", d.Job)
		if d.Conclusion != "" {
			fmt.Fprintf(&b, "conclusion: %s\n", d.Conclusion)
		}
		fmt.Fprintf(&b, "failed run: %s", d.FailedRun)
		if d.URL != "" {
			fmt.Fprintf(&b, " (%s)", d.URL)
		}
		fmt.Fprintf(&b, "\npassing run: %s\n", e.PassedRun)
		switch {
		case len(d.Tests) > 0:
			fmt.Fprintf(&b, "tests: %s\n", strings.Join(d.Tests, ", "))
			if d.Timeout != nil {
				fmt.Fprintf(&b, "timeout: %s\n", d.Timeout.text())
			}
		case d.Timeout != nil:
			fmt.Fprintf(&b, "tests: none failed; %s\n", d.Timeout.text())
		default:
			if d.Conclusion != "" && d.Conclusion != "failure" {
				// A cancelled or timed-out job prints no failed test: no name is missing.
				fmt.Fprintf(&b, "tests: none; the failed run was %s, not a test failure", d.Conclusion)
			} else {
				b.WriteString("tests: no test name found in the log")
			}
			if d.LogUnavailable != "" {
				fmt.Fprintf(&b, " (%s)", d.LogUnavailable)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "log: %s", ciLogCommand(r.e.Copy, r.task.Repository, r.task.ID, d.Job))
		if err := r.notify(fmt.Sprintf("flake.%s.%s.%s", s.ID, v.Head[:12], checkHash(d.Check)), store.MessageFlake, b.String()); err != nil {
			return err
		}
	}
	return nil
}
