package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The tests of the close decision (docs/tasks.md#an-attempt-that-reports-it-is-already-delivered): a build that
// reports a structured outcome whose evidence holds asks the Guildmaster to close or retry, over the fixtures of
// slice_test.go and one_pr_delivered_test.go.

// firstPR is the URL of the fake forge's first pull request, the one another branch merged.
const firstPR = "https://forge.invalid/1"

// closeBuild is build attempt n of the one-PR task reporting o.
func closeBuild(n int, o adapter.Outcome) interptest.Expectation {
	exp := failedOneBuild(firstPR)
	exp.Attempt = n
	exp.Respond = interptest.Report("Already delivered by "+firstPR+": every item is in its merge.", o)
	return exp
}

// toCloseDecision is app/t7 on the sliced workflow, or its copy with no range, accepted with a one-PR spec, with pull
// request 1 of the repository merged from another branch when merge is set, or left open, and its build reporting o,
// advanced to the result; then come the expectations after, such as a retried build.
func toCloseDecision(t *testing.T, ranged, merge bool, o adapter.Outcome, after ...interptest.Expectation) (*env, *sliceWorkspaces, interp.Next) {
	t.Helper()
	exps := append(append(sliceScript(t, onePR), closeBuild(1, o)), after...)
	e, f, ws := newSliceEnv(t, backends[1], ranged, exps...)
	want(t, e.toAccepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	other, err := f.Publish(t.Context(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: "elsewhere", Head: sliceHeads[1], Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if merge {
		if _, err := f.Merge(t.Context(), forge.Merge{Repository: repoPath, Remote: repoRemote, Number: other.Number, Head: other.Head, Title: "Elsewhere"}); err != nil {
			t.Fatal(err)
		}
	}
	return e, ws, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
}

var (
	byFirstPR   = adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, ByPR: firstPR}
	noChangeFor = adapter.Outcome{Kind: adapter.OutcomeNothingToDo, Reason: "The flag already defaults to off."}
	askClose    = interp.Next{Wait: interp.WaitDecision, Step: "build", Decision: "close-build-1"}
)

// TestCloseDecisionClosesTheTaskDone: a build reporting its change already delivered by a merged pull request of the
// repository opens close decision close-build-1, bound to the proposed delivery record, instead of attempt-failed;
// answering close records the delivery naming the decision, through the same close as `slice delivered`, and the
// task is done with nothing merged, its branch retired and the Director told the Guildmaster closed it.
func TestCloseDecisionClosesTheTaskDone(t *testing.T) {
	for _, ranged := range []bool{true, false} {
		t.Run(map[bool]string{true: "ranged", false: "unranged"}[ranged], func(t *testing.T) {
			e, ws, next := toCloseDecision(t, ranged, true, byFirstPR)
			want(t, next, askClose)
			msgs := e.messages(t)
			if _, ok := msgs["app.t7.attempt-failed.build-1"]; ok {
				t.Error("an outcome with its evidence holding sent attempt-failed")
			}
			if _, ok := msgs["app.t7.decision.close-build-1"]; !ok {
				t.Error("the Director was not told of the close decision")
			}
			d := e.decision(t, "close-build-1")
			proposal, ok := e.documents(t)["close-build-1-proposal"]
			switch {
			case !ok:
				t.Fatal("no proposal recorded")
			case d.Kind != store.DecisionClose || d.Subject == nil || d.Subject.Kind != store.SubjectDocument || d.Subject.Digest != "sha256:"+proposal.SHA256:
				t.Errorf("decision = %+v, want a close decision bound to the proposal %s", d, proposal.SHA256)
			case strings.Join(d.Options, ",") != "close,retry":
				t.Errorf("options = %v", d.Options)
			case !strings.HasPrefix(d.Question, "Close t7 as delivered by #1?\n") || !strings.Contains(d.Question, "the forge reports it merged at"):
				t.Errorf("question = %q", d.Question)
			}
			if got := e.task(t).State; got != store.TaskOpen {
				t.Fatalf("task state = %s before the answer, want open", got)
			}

			e.answer(t, "close-build-1", "close")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "close-build-1"}), done)
			if got := e.task(t); got.State != store.TaskDone || got.CancelReason != "" {
				t.Fatalf("task = %+v, want it done", got)
			}
			doc := e.pullRequestDoc(t, "delivered-1-delivery")
			by, _ := doc["by_pull_request"].(map[string]any)
			if by["url"] != firstPR || doc["decision"] != "close-build-1" || doc["kind"] != nil || !strings.HasPrefix(doc["reason"].(string), "Already delivered by "+firstPR) {
				t.Errorf("delivery record = %+v", doc)
			}
			if d := e.decision(t, "close-build-1"); d.Void || d.Choice != "close" {
				t.Errorf("close decision = %+v, want it answered close, not voided by the close it authorised", d)
			}
			text := e.messages(t)["app.t7.slice-delivered.delivered-1"]
			if !strings.HasPrefix(text, "The Guildmaster closed the task as already delivered by "+firstPR+" in decision close-build-1, with nothing merged for it:") {
				t.Errorf("slice-delivered message = %q", text)
			}
			if r := ws.Retired(); len(r) != 1 || r[0].Branch != "guildhall/t7" {
				t.Errorf("retired %+v, want the task's branch", r)
			}
			if got, ok, err := interp.TaskDeliveredBy(e.docList(t), e.eng.DocumentDir); err != nil || !ok || got != firstPR {
				t.Errorf("TaskDeliveredBy = %q, %v, %v", got, ok, err)
			}
		})
	}
}

// TestCloseDecisionRetryLaunchesTheNextAttempt: answering retry starts build attempt 2 as a Director retry does, with
// the reported outcome and the answer's note in its retry context.
func TestCloseDecisionRetryLaunchesTheNextAttempt(t *testing.T) {
	build2 := failedOneBuild(firstPR)
	build2.Attempt, build2.Respond = 2, interptest.Fail("still nothing")
	e, _, next := toCloseDecision(t, false, true, byFirstPR, build2)
	want(t, next, askClose)
	e.answer(t, "close-build-1", "retry\nThat PR only did half of it.")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "close-build-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	rt := boundRetry(t, e, "build", 2)
	if got, _ := rt["reported"].(map[string]any); got["by_pr"] != firstPR {
		t.Errorf("bound retry outcome = %v", rt)
	}
	if reason, _ := rt["reason"].(string); !strings.Contains(reason, "retry to close decision close-build-1") || !strings.Contains(reason, "That PR only did half of it.") {
		t.Errorf("bound retry reason = %q", reason)
	}
	if text := e.messages(t)["app.t7.attempt-retried.build-1"]; !strings.Contains(text, "close-build-1") {
		t.Errorf("attempt-retried = %q", text)
	}
	if got := e.task(t).State; got != store.TaskOpen {
		t.Errorf("task state = %s, want open", got)
	}
	if _, ok := e.documents(t)["delivered-1-delivery"]; ok {
		t.Error("a retry recorded a delivery")
	}
}

// TestCloseDecisionEvidenceFails: a pull request the forge reports open, or a forge that reads none, opens no close
// decision; the attempt fails to the Director, whose attempt-failed says why.
func TestCloseDecisionEvidenceFails(t *testing.T) {
	t.Run("not merged", func(t *testing.T) {
		e, _, next := toCloseDecision(t, false, false, byFirstPR)
		want(t, next, interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
		text := e.messages(t)["app.t7.attempt-failed.build-1"]
		for _, s := range []string{"It reports: already delivered by " + firstPR + ".", "Guildhall did not ask to close it:", "is open, not merged"} {
			if !strings.Contains(text, s) {
				t.Errorf("attempt-failed = %q; want %q", text, s)
			}
		}
		if ds := e.decisionIDs(t); strings.Contains(strings.Join(ds, ","), "close-") {
			t.Errorf("decisions = %v, want no close decision", ds)
		}
	})
	t.Run("forge reads no pull requests", func(t *testing.T) {
		exps := append(sliceScript(t, onePR), closeBuild(1, byFirstPR))
		e, _, _ := newSliceEnv(t, backends[1], false, exps...)
		e.eng.Forge = readerless{e.eng.Forge}
		e.toAccepted(t)
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
		if text := e.messages(t)["app.t7.attempt-failed.build-1"]; !strings.Contains(text, "the forge does not read pull requests") {
			t.Errorf("attempt-failed = %q", text)
		}
	})
}

// TestCloseDecisionNothingToDo: a nothing-to-do outcome needs no evidence: its close decision asks "no change
// needed?", and close ends the task done with a no-change delivery record naming no pull request.
func TestCloseDecisionNothingToDo(t *testing.T) {
	e, _, next := toCloseDecision(t, true, false, noChangeFor)
	want(t, next, askClose)
	if q := e.decision(t, "close-build-1").Question; !strings.HasPrefix(q, "Close t7: no change needed?\n") || !strings.Contains(q, noChangeFor.Reason) {
		t.Errorf("question = %q", q)
	}
	e.answer(t, "close-build-1", "close")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "close-build-1"}), done)
	doc := e.pullRequestDoc(t, "delivered-1-delivery")
	if doc["kind"] != interp.DeliveryNoChange || doc["by_pull_request"] != nil || doc["by_slice"] != nil || doc["reason"] != noChangeFor.Reason {
		t.Errorf("delivery record = %+v", doc)
	}
	if got := e.task(t).State; got != store.TaskDone {
		t.Errorf("task state = %s, want done", got)
	}
	if text := e.messages(t)["app.t7.slice-delivered.delivered-1"]; !strings.Contains(text, "closed the task as needing no change in decision close-build-1") {
		t.Errorf("slice-delivered message = %q", text)
	}
	if got, ok, err := interp.TaskDeliveredBy(e.docList(t), e.eng.DocumentDir); err != nil || !ok || got != interp.NoChangeNeeded {
		t.Errorf("TaskDeliveredBy = %q, %v, %v; want %q", got, ok, err, interp.NoChangeNeeded)
	}
}

// TestCloseDecisionCrashResumes: a crash after the close answer, once the delivery is recorded and before its message
// and the task's close, is finished by the next advance, which closes the task done.
func TestCloseDecisionCrashResumes(t *testing.T) {
	e, _, next := toCloseDecision(t, false, true, byFirstPR)
	want(t, next, askClose)
	e.answer(t, "close-build-1", "close")
	real := e.eng.Outbox
	e.eng.Outbox = failingOutbox{}
	if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: "t7", Decision: "close-build-1"}); err == nil {
		t.Fatal("advance with a failing outbox succeeded")
	}
	if _, ok := e.documents(t)["delivered-1-delivery"]; !ok {
		t.Fatal("the delivery was not recorded before the crash")
	}
	if got := e.task(t).State; got != store.TaskOpen {
		t.Fatalf("task state after the crash = %s, want open", got)
	}
	e.eng.Outbox = real
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if got := e.task(t).State; got != store.TaskDone {
		t.Errorf("task state = %s, want done", got)
	}
	if _, ok := e.messages(t)["app.t7.slice-delivered.delivered-1"]; !ok {
		t.Error("the resumed close sent no slice-delivered message")
	}
}

// TestCloseDecisionVoidedByADirectorRetry: the Director's retry while the close decision is open voids it, so a stale
// answer to it is refused, and starts attempt 2.
func TestCloseDecisionVoidedByADirectorRetry(t *testing.T) {
	build2 := failedOneBuild(firstPR)
	build2.Attempt, build2.Respond = 2, interptest.Fail("still nothing")
	e, _, next := toCloseDecision(t, false, true, byFirstPR, build2)
	want(t, next, askClose)
	want(t, e.advance(t, retried("build", 1, "look again")), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	if d := e.decision(t, "close-build-1"); !d.Void || !strings.Contains(d.VoidReason, "the Director retried attempt 1 of step build") {
		t.Errorf("close decision = %+v, want it voided by the retry", d)
	}
	if _, err := e.h.AnswerDecision("answer:stale", "app", "t7", "close-build-1", "close"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("answer to the voided close decision = %v, want ErrInvalid", err)
	}
}

// TestCloseDecisionClosesASlice: slice 2's build reporting slice 1 already delivered it asks to close slice 2 of the
// task; close records slice 2's delivery and slice 3's build starts in the same advance.
func TestCloseDecisionClosesASlice(t *testing.T) {
	digest := "sha256:" + sum([]byte(threeSlices))
	build2 := sliceBuild(t, digest, 2)[0]
	build2.Respond = interptest.Report("Already delivered by slice 1", adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, BySlice: 1})
	script := append(append(sliceScript(t, threeSlices, 1), build2), sliceBuild(t, digest, 3)[0])
	e, _, _ := newSliceEnv(t, backends[0], true, script...)
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	b2, b3 := workflow.SliceStep("build", 2), workflow.SliceStep("build", 3)
	want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitAttempt, Step: b2, Attempt: 1})
	id := interp.CloseDecision(b2, 1)
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: b2, Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: b2, Decision: id})
	if q := e.decision(t, id).Question; !strings.HasPrefix(q, "Close slice 2 of 3") || !strings.Contains(q, "as delivered by slice 1?") || !strings.Contains(q, "slice 3 starts") {
		t.Errorf("question = %q", q)
	}
	e.answer(t, id, "close")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: id}), interp.Next{Wait: interp.WaitAttempt, Step: b3, Attempt: 1})
	doc := e.pullRequestDoc(t, interp.DeliveryRecord(2))
	if doc["by_slice"] != float64(1) || doc["decision"] != id {
		t.Errorf("delivery record = %+v", doc)
	}
	if p := e.slices(t); p.Current != 3 || p.Items[1].State != interp.SliceAlreadyDelivered || p.Items[1].DeliveredBy != "slice 1" {
		t.Errorf("slice progress = %+v", p)
	}
}

// TestDirectorNoChangeClose: the Director's `slice delivered --no-change` closes a one-PR task done with the same
// no-change record, and a no-change close naming a pull request is refused.
func TestDirectorNoChangeClose(t *testing.T) {
	e, _, _, merged := toFailedOnePR(t, false)
	ev := interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 1, NoChange: true, PullRequest: merged.URL, Reason: "r"}
	if _, err := e.eng.Advance(t.Context(), ev); err == nil || !strings.Contains(err.Error(), "a no-change close names no earlier slice or pull request") {
		t.Errorf("no-change with a pull request = %v", err)
	}
	ev.PullRequest = ""
	ev.Decision = "forged"
	want(t, e.advance(t, ev), done)
	doc := e.pullRequestDoc(t, "delivered-1-delivery")
	if doc["kind"] != interp.DeliveryNoChange || doc["decision"] != nil || doc["by_pull_request"] != nil {
		t.Errorf("delivery record = %+v", doc)
	}
	if text := e.messages(t)["app.t7.slice-delivered.delivered-1"]; text != "The Director closed the task as needing no change, with nothing merged for it: r. The task is done." {
		t.Errorf("slice-delivered message = %q", text)
	}
}
