package interp_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// frontRecordName is the rerun record of check on head h of step deliver: the queue front's, apart from validate's.
func frontRecordName(h, check string) string {
	return "deliver-rerun-" + h[:12] + sum([]byte(check))[:8]
}

// frontChecks is the name of deliver's checks record of the rebased head head2.
var frontChecks = "deliver-1-checks-" + head2

// rebasedFront is queueEnv with the merge answered and the front rebased cleanly onto baseN(1) as head2, pushed and
// waiting on head2's checks, which are set to checks; mergeability is cleared so green checks merge.
func rebasedFront(t *testing.T, b backend, edit func(*env), checks ...forge.Check) (*env, *forgetest.Fake) {
	t.Helper()
	e, f, ws := queueEnv(t, b)
	e.eng.CILogLimit = 1 << 20
	base := baseN(1)
	clean(ws, base, head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), deliverWait)
	if edit != nil {
		edit(e)
	}
	f.SetChecks(repoRemote, head2, checks...)
	f.SetMergeability(forge.Mergeability{})
	f.SetLog("123", jobLog)
	return e, f
}

// expectEscalation scripts the build round the rebase-escalated path starts.
func expectEscalation(t *testing.T, e *env) interp.Next {
	t.Helper()
	exp := buildExp(t, 2, head3, "")
	exp.Inputs["feedback"] = interptest.AnyDigest
	e.script.Expect(exp)
	return interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2}
}

// checkNamed returns the check called name among checks.
func checkNamed(t *testing.T, checks []forge.Check, name string) forge.Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %v", name, checks)
	return forge.Check{}
}

// inQueue reports whether app/t7 is still queued, on a backend that keeps a queue.
func inQueue(t *testing.T, e *env) (queued, known bool) {
	t.Helper()
	s, ok := e.h.Records.(*store.Store)
	if !ok {
		return false, false
	}
	view, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(view.Entries, func(q store.QueuedEntry) bool { return q.TaskID == "t7" }), true
}

// TestQueueRerunFlake: a failed Actions check of the rebased front is re-run once on the same head instead of
// escalating; it passing records a passed checks result naming the flake, sends one flake message under the default
// rerun-and-file, and the front merges.
func TestQueueRerunFlake(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := rebasedFront(t, b, nil, flakyChecks()...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
				t.Fatalf("reruns = %q, want job 123 once", got)
			}
			if _, ok := e.documents(t)[frontChecks]; ok {
				t.Fatal("a rerun recorded the checks")
			}
			rec := e.pullRequestDoc(t, frontRecordName(head2, macos))
			if rec["step"] != "deliver" || rec["check"] != macos || rec["failed_run"] != "CR_123" || rec["head"] != head2 ||
				!slices.Equal(rec["tests"].([]any), []any{"TestX"}) {
				t.Errorf("rerun record = %v", rec)
			}
			if _, ok := e.documents(t)[recordName(head2, macos)]; ok {
				t.Error("the front's rerun wrote a validate rerun record")
			}
			// The rerun is pending: the front waits on it, and reruns nothing again.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			passed := settle(t, f, head2, forge.CheckPassed)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if got := mergedHead(f); got != head2 {
				t.Fatalf("merged head = %q, want the rebased head", got)
			}
			v := e.validationDoc(t, frontChecks)
			if v.Verdict != "passed" || v.Policy != "rerun-and-file" || len(v.Reruns) != 1 ||
				v.Reruns[0] != (rerunResult{Check: macos, Record: frontRecordName(head2, macos), FailedRun: "CR_123", PassedRun: checkNamed(t, passed, macos).ID}) {
				t.Errorf("%s = %+v", frontChecks, v)
			}
			flakes := withPrefix(e.messages(t), "app.t7.flake.")
			if len(flakes) != 1 || len(withPrefix(flakes, "app.t7.flake.deliver."+head2[:12]+".")) != 1 {
				t.Fatalf("flake messages = %v, want one of deliver", flakes)
			}
			for _, text := range flakes {
				for _, line := range []string{"policy: rerun-and-file\n", `Check "test (macos-latest)"`, "(step deliver, " + frontChecks + ")", "job: 123\n", "tests: TestX\n"} {
					if !strings.Contains(text, line) {
						t.Errorf("flake message = %q, want %q", text, line)
					}
				}
			}
			if got := f.Reruns(); len(got) != 1 {
				t.Errorf("reruns = %q, want one", got)
			}
		})
	}
}

// TestQueueRerunFailsAgain: a rerun that fails again is a real failure, recorded naming the check re-run, and
// escalates to build as red checks do today, saying the check failed again after one rerun; nothing is re-run twice.
func TestQueueRerunFailsAgain(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := rebasedFront(t, b, nil, flakyChecks()...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			settle(t, f, head2, forge.CheckFailed)
			escalated := expectEscalation(t, e)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), escalated)
			v := e.validationDoc(t, frontChecks)
			if v.Verdict != "failed" || len(v.Reruns) != 1 || v.Reruns[0].Record != frontRecordName(head2, macos) || v.Reruns[0].PassedRun != "" {
				t.Errorf("%s = %+v", frontChecks, v)
			}
			esc := e.pullRequestDoc(t, "deliver-1-escalated-1")
			if esc["from"] != head2 || !strings.Contains(esc["reason"].(string), "(test (macos-latest) failed again after one rerun)") {
				t.Errorf("escalation = %v", esc)
			}
			if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
				t.Errorf("reruns = %q, want job 123 once", got)
			}
			if _, merges := f.Calls(); merges != 0 {
				t.Errorf("merges = %d", merges)
			}
		})
	}
}

// TestQueueRerunIneligible: a red commit status, which has no Actions job, escalates in the same advance with no
// rerun, even while other checks are pending; so does every red check under off and on a forge that cannot re-run.
func TestQueueRerunIneligible(t *testing.T) {
	status := forge.Check{Name: "ci", State: forge.CheckFailed, ID: "SC_ci"}
	for _, tc := range []struct {
		name   string
		edit   func(*env)
		checks []forge.Check
	}{
		{name: "a status with no job", checks: []forge.Check{status}},
		{name: "a status beside a pending job", checks: []forge.Check{status, actionsCheck(macos, "123", forge.CheckPending)}},
		{name: "a job beside a status", checks: []forge.Check{actionsCheck(macos, "123", forge.CheckFailed), status}},
		{name: "off", edit: func(e *env) { e.eng.Config.Repositories[0].FlakyChecks = config.FlakyChecksOff }, checks: flakyChecks()},
		{name: "a forge without CheckRerunner", edit: func(e *env) { e.eng.Forge = plainForge{e.eng.Forge} }, checks: flakyChecks()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f := rebasedFront(t, backends[0], tc.edit, tc.checks...)
			escalated := expectEscalation(t, e)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), escalated)
			if got := f.Reruns(); len(got) != 0 {
				t.Errorf("reruns = %q, want none", got)
			}
			v := e.pullRequestDoc(t, frontChecks)
			if v["verdict"] != "failed" || v["reruns"] != nil || v["flaky_checks"] != nil {
				t.Errorf("%s = %v, want a failed read with no reruns", frontChecks, v)
			}
			if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); strings.Contains(esc["reason"].(string), "again") {
				t.Errorf("escalation = %v", esc)
			}
		})
	}
}

// TestQueueRerunWaitsForTheMatrix: a red Actions check while another check of the rebased head is pending records
// nothing, sends the rebased head's checks-pending text and keeps the front; once the matrix finishes the job is
// re-run.
func TestQueueRerunWaitsForTheMatrix(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			windows := "test (windows-latest)"
			e, f := rebasedFront(t, b, nil, actionsCheck(macos, "123", forge.CheckFailed), actionsCheck(windows, "124", forge.CheckPending))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			if got := f.Reruns(); len(got) != 0 {
				t.Fatalf("reruns = %q, want none while the matrix runs", got)
			}
			if _, ok := e.documents(t)[frontChecks]; ok {
				t.Error("a wait recorded the checks")
			}
			if text := e.messages(t)["app.t7.checks-pending.deliver."+head2[:12]]; !strings.Contains(text, "the checks of rebased head "+head2) {
				t.Errorf("checks-pending = %q, want the rebased head's text", text)
			}
			if queued, known := inQueue(t, e); known && !queued {
				t.Error("the front left the queue while it waited")
			}
			f.SetChecks(repoRemote, head2, actionsCheck(macos, "123", forge.CheckFailed), actionsCheck(windows, "124", forge.CheckPassed))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
			if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
				t.Errorf("reruns = %q, want job 123 once the matrix finished", got)
			}
		})
	}
}

// TestQueueRerunRefused: a rerun the forge refuses records why beside the rerun record, and the front escalates in
// the same advance.
func TestQueueRerunRefused(t *testing.T) {
	e, f := rebasedFront(t, backends[0], nil, flakyChecks()...)
	f.SetRerunError("123", errors.New("gh: This workflow run is too old to re-run (HTTP 403)"))
	escalated := expectEscalation(t, e)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), escalated)
	out := e.pullRequestDoc(t, frontRecordName(head2, macos)+"-outcome")
	if !strings.Contains(out["error"].(string), "too old to re-run") || out["record"] != frontRecordName(head2, macos) {
		t.Errorf("rerun outcome = %v", out)
	}
	if v := e.pullRequestDoc(t, frontChecks); v["verdict"] != "failed" || v["reruns"] != nil {
		t.Errorf("%s = %v, want a failed read naming no rerun", frontChecks, v)
	}
}

// TestQueueRerunCrash: a crash after the rerun record is written but before the forge is asked counts the rerun as
// spent: the replay asks no rerun and escalates.
func TestQueueRerunCrash(t *testing.T) {
	e, f := rebasedFront(t, backends[0], nil, flakyChecks()...)
	records := e.eng.Records
	e.eng.Records = &failPut{Records: records, name: frontRecordName(head2, macos), after: true}
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
		t.Fatalf("advance = %v, want the injected failure", err)
	}
	e.eng.Records = records
	escalated := expectEscalation(t, e)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), escalated)
	if got := f.Reruns(); len(got) != 0 {
		t.Errorf("reruns = %q, want none", got)
	}
	if v := e.pullRequestDoc(t, frontChecks); v["verdict"] != "failed" || v["reruns"] != nil {
		t.Errorf("%s = %v, want a failed read naming no rerun", frontChecks, v)
	}
}

// TestQueueRerunPolicyRerun: under rerun a flake on the front merges with its checks naming it, and sends no flake
// message.
func TestQueueRerunPolicyRerun(t *testing.T) {
	e, f := rebasedFront(t, backends[0], func(e *env) { e.eng.Config.Repositories[0].FlakyChecks = config.FlakyChecksRerun }, flakyChecks()...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
	settle(t, f, head2, forge.CheckPassed)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if v := e.validationDoc(t, frontChecks); v.Verdict != "passed" || v.Policy != "rerun" || len(v.Reruns) != 1 {
		t.Errorf("%s = %+v", frontChecks, v)
	}
	if got := withPrefix(e.messages(t), "app.t7.flake."); len(got) != 0 {
		t.Errorf("flake messages = %v, want none under rerun", got)
	}
}

// TestQueueRerunFlakeReplay: a crash after the passed checks record, before its flake message, still sends the flake
// once on replay; another advance sends nothing new.
func TestQueueRerunFlakeReplay(t *testing.T) {
	e, f := rebasedFront(t, backends[0], nil, flakyChecks()...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
	settle(t, f, head2, forge.CheckPassed)
	records := e.eng.Records
	e.eng.Records = &failPut{Records: records, name: frontChecks, after: true}
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
		t.Fatalf("advance = %v, want the injected failure", err)
	}
	e.eng.Records = records
	if got := withPrefix(e.messages(t), "app.t7.flake."); len(got) != 0 {
		t.Fatalf("flake messages before the record stood = %v", got)
	}
	// The replay reads the record back and sends the flake; it stops before the carried approval, so the next
	// advance reads the record back again.
	e.eng.Records = &failPut{Records: records, name: "deliver-2-authorized"}
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
		t.Fatalf("advance = %v, want the injected failure", err)
	}
	e.eng.Records = records
	first := withPrefix(e.messages(t), "app.t7.flake.")
	n := len(withPrefix(e.messages(t), "app.t7.flake."))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if got := withPrefix(e.messages(t), "app.t7.flake."); len(first) != 1 || len(got) != n || !maps.Equal(first, got) {
		t.Errorf("flake messages = %v then %v, want the same one", first, got)
	}
	if got := f.Reruns(); len(got) != 1 {
		t.Errorf("reruns = %q, want one", got)
	}
}
