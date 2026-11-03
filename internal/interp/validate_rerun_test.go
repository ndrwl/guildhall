package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// macos is the matrix check the rerun tests fail: a GitHub Actions job, run CR_123 of job 123.
const macos = "test (macos-latest)"

// flakyChecks is a failed Actions job beside a passed check that is not one.
func flakyChecks() []forge.Check {
	return []forge.Check{actionsCheck(macos, "123", forge.CheckFailed), {Name: "lint", State: forge.CheckPassed, ID: "SC_lint"}}
}

// recordName is the rerun record of check on head h of step validate.
func recordName(h, check string) string {
	return "validate-rerun-" + h[:12] + sum([]byte(check))[:8]
}

// settle finishes every pending check the fake reports for h at state, and returns the checks.
func settle(t *testing.T, f interface {
	SetChecks(remote, head string, checks ...forge.Check)
	Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error)
}, h string, state forge.CheckState) []forge.Check {
	t.Helper()
	got, err := f.Checks(context.Background(), forge.Commit{Repository: repoPath, Remote: repoRemote, Head: h})
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i].State == forge.CheckPending {
			got[i].State = state
		}
	}
	f.SetChecks(repoRemote, h, got...)
	return got
}

// withPrefix returns the messages whose id starts with prefix.
func withPrefix(msgs map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for id, text := range msgs {
		if strings.HasPrefix(id, prefix) {
			out[id] = text
		}
	}
	return out
}

// TestValidateRerunFlake: under the default rerun-and-file, a failed Actions job is re-run once on the same head
// instead of going back to build; it passing makes a passed result naming the flake, one flake message carrying
// the policy and the evidence, no build round and no firing of the loop; replays send nothing new.
func TestValidateRerunFlake(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.eng.CILogLimit = 1 << 20
			f.SetChecks(repoRemote, head, flakyChecks()...)
			f.SetLog("123", jobLog)
			want(t, e.toValidate(t), waitChecks)
			if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
				t.Fatalf("reruns = %q, want job 123 once", got)
			}
			docs := e.documents(t)
			if _, ok := docs["validate-1-result"]; ok {
				t.Fatal("a rerun recorded a result")
			}
			rec := e.pullRequestDoc(t, recordName(head, macos))
			if rec["check"] != macos || rec["failed_run"] != "CR_123" || rec["job"] != "123" || rec["head"] != head ||
				!slices.Equal(rec["tests"].([]any), []any{"TestX"}) || rec["requested_at"] == "" {
				t.Errorf("rerun record = %v", rec)
			}
			if got := withPrefix(e.messages(t), "app.t7.checks-failed"); len(got) != 0 {
				t.Errorf("a rerun sent checks-failed: %v", got)
			}
			// The rerun is pending: validate waits on it, and reruns nothing again.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
			passed := settle(t, f, head, forge.CheckPassed)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			w := result(head, "passed", passed)
			w.Reruns = []rerunResult{{Check: macos, Record: recordName(head, macos), FailedRun: "CR_123", PassedRun: passed[1].ID}}
			w.Policy = "rerun-and-file"
			if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
				t.Errorf("validate-1-result = %+v, want %+v", e.pullRequestDoc(t, "validate-1-result"), w)
			}
			flakes := withPrefix(e.messages(t), "app.t7.flake.validate."+head[:12]+".")
			if len(flakes) != 1 {
				t.Fatalf("flake messages = %v, want one", flakes)
			}
			for _, text := range flakes {
				for _, line := range []string{"policy: rerun-and-file\n", `Check "test (macos-latest)"`, "platform: macos-latest\n", "job: 123\n",
					"failed run: CR_123 (https://ci.invalid/123)\n", "passing run: " + passed[1].ID + "\n", "tests: TestX\n", "log: guildhall ci log app/t7 123"} {
					if !strings.Contains(text, line) {
						t.Errorf("flake message = %q, want %q", text, line)
					}
				}
			}
			if got := f.Reruns(); len(got) != 1 {
				t.Errorf("reruns = %q, want one", got)
			}
			n := len(e.messages(t))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := len(e.messages(t)); got != n {
				t.Errorf("a replay sent %d new messages", got-n)
			}
		})
	}
}

// TestValidateRerunFailsAgain: a rerun that fails again is one real failed round, naming the check already re-run,
// that sends the task back to build, and the head is not re-run a second time; the next head gets its own rerun.
func TestValidateRerunFailsAgain(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	e.eng.CILogLimit = 1 << 20
	f.SetChecks(repoRemote, head, flakyChecks()...)
	f.SetLog("123", jobLog)
	want(t, e.toValidate(t), waitChecks)
	failed := settle(t, f, head, forge.CheckFailed)
	job := failed[1].Job
	f.SetLog(job, jobLog)
	w := result(head, "failed", failed, macos)
	w.Logs = []logEntry{available(macos, job, 0)}
	w.Reruns = []rerunResult{{Check: macos, Record: recordName(head, macos), FailedRun: "CR_123"}}
	w.Policy = "rerun-and-file"
	e.script.Expect(buildExp(t, 2, head2, w.digest(t)),
		reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil)))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	if got := e.recordedDigest(t, "validate-1-result"); got != w.digest(t) {
		t.Errorf("validate-1-result = %+v, want %+v", e.pullRequestDoc(t, "validate-1-result"), w)
	}
	if msg := e.messages(t)["app.t7.checks-failed.validate-1"]; !strings.Contains(msg, "(test (macos-latest) failed again after one rerun)") {
		t.Errorf("checks-failed message = %q, want it to say the check failed again after one rerun", msg)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
		t.Fatalf("reruns = %q, want job 123 once", got)
	}
	// The next head fails the same check: it is a new head, so it gets its own rerun.
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
	f.SetChecks(repoRemote, head2, actionsCheck(macos, "456", forge.CheckFailed))
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
	if got := f.Reruns(); !slices.Equal(got, []string{"123", "456"}) {
		t.Errorf("reruns = %q, want head 2's job re-run too", got)
	}
	if _, ok := e.documents(t)["validate-2-result"]; ok {
		t.Error("head 2's rerun recorded a result")
	}
}

// TestValidateRerunIneligible: a round whose failing checks are not all Actions jobs is a real failure at once,
// with no rerun, under every policy; so is every round under off and on a forge that cannot re-run a job.
func TestValidateRerunIneligible(t *testing.T) {
	mixed := []forge.Check{actionsCheck(macos, "123", forge.CheckFailed), {Name: "lint", State: forge.CheckFailed, ID: "SC_lint"}}
	for _, tc := range []struct {
		name   string
		policy config.FlakyChecks
		plain  bool
		checks []forge.Check
	}{
		{name: "a status with no job", checks: []forge.Check{{Name: "ci", State: forge.CheckFailed, ID: "SC_ci"}}},
		{name: "a job beside a status", checks: mixed},
		{name: "a job under rerun beside a status", policy: config.FlakyChecksRerun, checks: mixed},
		{name: "off", policy: config.FlakyChecksOff, checks: flakyChecks()},
		{name: "a forge without CheckRerunner", plain: true, checks: flakyChecks()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := editValidate(t, func(s *workflow.Step) { s.On = nil })
			e, f := newValidateEnvDefinition(t, backends[0], def)
			e.eng.Config.Repositories[0].FlakyChecks = tc.policy
			if tc.plain {
				e.eng.Forge = plainForge{f}
			}
			f.SetChecks(repoRemote, head, tc.checks...)
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			if got := f.Reruns(); len(got) != 0 {
				t.Errorf("reruns = %q, want none", got)
			}
			v := e.pullRequestDoc(t, "validate-1-result")
			if v["verdict"] != "failed" || v["reruns"] != nil || v["flaky_checks"] != nil {
				t.Errorf("validate-1-result = %v, want a failed round with no reruns", v)
			}
		})
	}
}

// TestValidateRerunWaitsForTheMatrix: a failed job while another check of the head is pending waits, recording
// nothing and sending checks-pending rather than checks-failed, since GitHub re-runs no job of a run in progress;
// once the matrix finishes the failed job is re-run.
func TestValidateRerunWaitsForTheMatrix(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	f.SetChecks(repoRemote, head, actionsCheck(macos, "123", forge.CheckFailed), actionsCheck("test (windows-latest)", "124", forge.CheckPending))
	want(t, e.toValidate(t), waitChecks)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
	if got := f.Reruns(); len(got) != 0 {
		t.Fatalf("reruns = %q, want none while the matrix runs", got)
	}
	msgs := e.messages(t)
	if len(withPrefix(msgs, "app.t7.checks-failed")) != 0 || msgs["app.t7.checks-pending.validate."+head[:12]] == "" {
		t.Errorf("messages = %v, want checks-pending and no checks-failed", msgs)
	}
	if _, ok := e.documents(t)["validate-1-result"]; ok {
		t.Error("a wait recorded a result")
	}
	f.SetChecks(repoRemote, head, actionsCheck(macos, "123", forge.CheckFailed), actionsCheck("test (windows-latest)", "124", forge.CheckPassed))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
	if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
		t.Errorf("reruns = %q, want job 123 once the matrix finished", got)
	}
}

// TestValidateRerunRefused: a rerun the forge refuses records why beside the rerun record and is a real failed
// round in the same advance, so CI trouble never stalls the task.
func TestValidateRerunRefused(t *testing.T) {
	def := editValidate(t, func(s *workflow.Step) { s.On = nil })
	e, f := newValidateEnvDefinition(t, backends[0], def)
	f.SetChecks(repoRemote, head, flakyChecks()...)
	f.SetRerunError("123", errors.New("gh: This workflow run is too old to re-run (HTTP 403)"))
	want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
	out := e.pullRequestDoc(t, recordName(head, macos)+"-outcome")
	if !strings.Contains(out["error"].(string), "too old to re-run") || out["record"] != recordName(head, macos) {
		t.Errorf("rerun outcome = %v", out)
	}
	v := e.pullRequestDoc(t, "validate-1-result")
	if v["verdict"] != "failed" || v["reruns"] != nil {
		t.Errorf("validate-1-result = %v, want a failed round naming no rerun", v)
	}
	if msg := e.messages(t)["app.t7.checks-failed.validate-1"]; msg == "" || strings.Contains(msg, "again") {
		t.Errorf("checks-failed message = %q", msg)
	}
}

// TestValidateRerunCrash: a crash after the rerun record is written but before the forge is asked leaves the check
// at its failed run with nothing pending: the rerun counts as spent, the forge is not asked twice, and the round is
// a real failure.
func TestValidateRerunCrash(t *testing.T) {
	def := editValidate(t, func(s *workflow.Step) { s.On = nil })
	e, f := newValidateEnvDefinition(t, backends[0], def)
	f.SetChecks(repoRemote, head, flakyChecks()...)
	fp := &failPut{Records: e.eng.Records, name: recordName(head, macos), after: true}
	e.eng.Records = fp
	e.accepted(t)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); !errors.Is(err, errInjected) {
		t.Fatalf("advance = %v, want the injected failure", err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
	if got := f.Reruns(); len(got) != 0 {
		t.Errorf("reruns = %q, want none", got)
	}
	if v := e.pullRequestDoc(t, "validate-1-result"); v["verdict"] != "failed" || v["reruns"] != nil {
		t.Errorf("validate-1-result = %v, want a failed round naming no rerun", v)
	}
}

// TestValidateRerunPolicyRerun: under rerun a flake passes as under rerun-and-file, its result naming it, but sends
// no flake message.
func TestValidateRerunPolicyRerun(t *testing.T) {
	e, f := newValidateEnv(t, backends[0])
	e.eng.Config.Repositories[0].FlakyChecks = config.FlakyChecksRerun
	f.SetChecks(repoRemote, head, flakyChecks()...)
	want(t, e.toValidate(t), waitChecks)
	passed := settle(t, f, head, forge.CheckPassed)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	v := e.validationDoc(t, "validate-1-result")
	if v.Verdict != "passed" || v.Policy != "rerun" || len(v.Reruns) != 1 || v.Reruns[0].PassedRun != passed[1].ID {
		t.Errorf("validate-1-result = %+v", v)
	}
	if got := withPrefix(e.messages(t), "app.t7.flake."); len(got) != 0 {
		t.Errorf("flake messages = %v, want none under rerun", got)
	}
}

// TestValidateRerunFlakeReplay: a crash around recording the passed result of a flake replays to one result and
// the same flake message, under the same id, with no rerun asked again.
func TestValidateRerunFlakeReplay(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			e, f := newValidateEnv(t, backends[0])
			f.SetChecks(repoRemote, head, flakyChecks()...)
			want(t, e.toValidate(t), waitChecks)
			settle(t, f, head, forge.CheckPassed)
			fp := &failPut{Records: e.eng.Records, name: "validate-1-result", after: after}
			e.eng.Records = fp
			if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
				t.Fatalf("advance = %v, want the injected failure", err)
			}
			if got := withPrefix(e.messages(t), "app.t7.flake."); len(got) != 0 {
				t.Fatalf("flake messages before the result stood = %v", got)
			}
			deliver := interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliver)
			first := withPrefix(e.messages(t), "app.t7.flake.")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliver)
			if got := withPrefix(e.messages(t), "app.t7.flake."); len(first) != 1 || len(got) != 1 {
				t.Errorf("flake messages = %v then %v, want the same one", first, got)
			}
			if got := f.Reruns(); len(got) != 1 {
				t.Errorf("reruns = %q, want one", got)
			}
		})
	}
}

// windows is the second matrix check the partial-rerun tests fail: run CR_124 of job 124.
const windows = "test (windows-latest)"

// twoFailedJobs is the macos and windows jobs of one round, both failed.
func twoFailedJobs() []forge.Check {
	return []forge.Check{actionsCheck(macos, "123", forge.CheckFailed), actionsCheck(windows, "124", forge.CheckFailed)}
}

// lagging returns checks with windows back at its old failed run CR_124, as a read does before GitHub registers its
// rerun's new attempt.
func lagging(checks []forge.Check) []forge.Check {
	out := slices.Clone(checks)
	for i := range out {
		if out[i].Name == windows {
			out[i] = actionsCheck(windows, "124", forge.CheckFailed)
		}
	}
	return out
}

// finished returns checks with every one at state.
func finished(checks []forge.Check, state forge.CheckState) []forge.Check {
	out := slices.Clone(checks)
	for i := range out {
		out[i].State = state
	}
	return out
}

// rerunChecks returns the check names of reruns.
func rerunChecks(reruns []rerunResult) []string {
	var out []string
	for _, r := range reruns {
		out = append(out, r.Check)
	}
	return out
}

// TestValidateRerunPartialRefusal: when the forge accepts 123's rerun and then refuses 124's, the refusal is recorded
// but no result is: validate waits for 123's rerun, then records one failed round from a fresh read, whose checks
// show the rerun's run and whose reruns name the check that failed again; windows' refusal keeps it failed either way.
func TestValidateRerunPartialRefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   forge.CheckState
		failing []string
		reruns  []string
	}{
		{name: "fails again", state: forge.CheckFailed, failing: []string{macos, windows}, reruns: []string{macos}},
		{name: "passes", state: forge.CheckPassed, failing: []string{windows}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := editValidate(t, func(s *workflow.Step) { s.On = nil })
			e, f := newValidateEnvDefinition(t, backends[0], def)
			f.SetChecks(repoRemote, head, twoFailedJobs()...)
			f.SetRerunError("124", errors.New("gh: This workflow run is too old to re-run (HTTP 403)"))
			want(t, e.toValidate(t), waitChecks)
			if got := f.Reruns(); !slices.Equal(got, []string{"123", "124"}) {
				t.Fatalf("reruns = %q, want both jobs asked once", got)
			}
			if out := e.pullRequestDoc(t, recordName(head, windows)+"-outcome"); !strings.Contains(out["error"].(string), "too old to re-run") {
				t.Errorf("rerun outcome = %v", out)
			}
			if _, ok := e.documents(t)["validate-1-result"]; ok {
				t.Fatal("a result was recorded while 123's rerun runs")
			}
			if got := withPrefix(e.messages(t), "app.t7.checks-failed"); len(got) != 0 {
				t.Errorf("checks-failed sent while 123's rerun runs: %v", got)
			}
			// The accepted rerun is still running: validate waits on it and asks nothing again.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
			if _, ok := e.documents(t)["validate-1-result"]; ok {
				t.Fatal("a result was recorded while 123's rerun is pending")
			}
			settle(t, f, head, tc.state)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
			v := e.validationDoc(t, "validate-1-result")
			if v.Verdict != "failed" || !slices.Equal(v.Failing, tc.failing) {
				t.Errorf("validate-1-result = %+v, want failed naming %q", v, tc.failing)
			}
			if c := checkNamed(t, v.Checks, macos); c.ID != "CR_123-rerun-1" || c.State != tc.state {
				t.Errorf("macos check = %+v, want its rerun's run", c)
			}
			if got := rerunChecks(v.Reruns); !slices.Equal(got, tc.reruns) {
				t.Errorf("reruns = %q, want %q", got, tc.reruns)
			}
			if got := f.Reruns(); len(got) != 2 {
				t.Errorf("reruns = %q, want two calls", got)
			}
		})
	}
}

// TestValidateRerunLaggingRead: after both reruns are accepted, a read showing 123's rerun pending beside 124's old
// failed run records nothing and waits on checks, replaying the same checks-pending message; once both reruns have
// finished the round is recorded as usual.
func TestValidateRerunLaggingRead(t *testing.T) {
	def := editValidate(t, func(s *workflow.Step) { s.On = nil })
	e, f := newValidateEnvDefinition(t, backends[0], def)
	// The matrix is still running at first, so the checks-pending message is sent before any rerun.
	f.SetChecks(repoRemote, head, actionsCheck(macos, "123", forge.CheckFailed), actionsCheck(windows, "124", forge.CheckPending))
	want(t, e.toValidate(t), waitChecks)
	id := "app.t7.checks-pending.validate." + head[:12]
	before := e.messages(t)[id]
	if before == "" {
		t.Fatalf("messages = %v, want %s", e.messages(t), id)
	}
	f.SetChecks(repoRemote, head, twoFailedJobs()...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
	if got := f.Reruns(); !slices.Equal(got, []string{"123", "124"}) {
		t.Fatalf("reruns = %q, want both jobs", got)
	}
	rerun, err := f.Checks(t.Context(), forge.Commit{Repository: repoPath, Remote: repoRemote, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	f.SetChecks(repoRemote, head, lagging(rerun)...)
	n := len(e.messages(t))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
	if _, ok := e.documents(t)["validate-1-result"]; ok {
		t.Fatal("a result was recorded while 123's rerun is pending")
	}
	msgs := e.messages(t)
	if msgs[id] != before || len(msgs) != n {
		t.Errorf("checks-pending = %q (%d new messages), want %q replayed", msgs[id], len(msgs)-n, before)
	}
	if got := withPrefix(msgs, "app.t7.checks-failed"); len(got) != 0 {
		t.Errorf("checks-failed sent while a rerun runs: %v", got)
	}
	if got := f.Reruns(); len(got) != 2 {
		t.Errorf("reruns = %q, want no new call", got)
	}
	// Both reruns finish, failing again: the round is recorded, naming both.
	f.SetChecks(repoRemote, head, finished(rerun, forge.CheckFailed)...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
	v := e.validationDoc(t, "validate-1-result")
	if v.Verdict != "failed" || !slices.Equal(rerunChecks(v.Reruns), []string{macos, windows}) || !slices.Equal(v.Failing, []string{macos, windows}) {
		t.Errorf("validate-1-result = %+v, want a failed round naming both reruns", v)
	}
}
