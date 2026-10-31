package interp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// merged runs the publish workflow to the Guildmaster's merge with the quality settings in local and returns the
// final advance's wait.
func (e *env) merged(t *testing.T, local map[string]string) interp.Next {
	t.Helper()
	e.eng.Config.Local = local
	e.toSignOff(t)
	e.answer(t, "deliver-1", "merge")
	return e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"})
}

// auditEveryTask is the retired audit's setting at its most eager: before the audit was retired it sampled every
// delivered task.
var auditEveryTask = map[string]string{config.QualitySelfReviewKey: "off", config.QualityAuditSampleKey: "1"}

// wantNoAudit fails when task t7 has an audit decision.
func (e *env) wantNoAudit(t *testing.T) {
	t.Helper()
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(ds, func(d store.Decision) bool { return d.Kind == store.DecisionAudit }) {
		t.Errorf("decisions = %+v, want no audit", ds)
	}
	if got := e.inbox(t); slices.Contains(got, "app.t7.decision.audit") {
		t.Errorf("inbox = %q, want no audit message", got)
	}
}

// TestNoAuditAfterMerge: the merge that closes a task opens no audit, whatever quality_audit_sample says, even 1,
// which once sampled every task; the task is done, its wait is done and the merged message is the Director's last.
func TestNoAuditAfterMerge(t *testing.T) {
	for name, local := range map[string]map[string]string{
		"unset":                {},
		"sample 1":             auditEveryTask,
		"sample 1 with review": {config.QualitySelfReviewKey: "on", config.QualityAuditSampleKey: "1"},
		"sample 0":             {config.QualityAuditSampleKey: "0"},
	} {
		for _, b := range backends {
			t.Run(name+"/"+b.name, func(t *testing.T) {
				e, _ := newPublishEnv(t, b)
				want(t, e.merged(t, local), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
				if got := e.task(t).State; got != store.TaskDone {
					t.Errorf("task state = %q, want done", got)
				}
				e.wantNoAudit(t)
				if got := e.inbox(t); got[len(got)-1] != "app.t7.merged.deliver-1" {
					t.Errorf("inbox = %q, want the merge last", got)
				}
			})
		}
	}
}

// failClose fails the first SetTaskState, after the merged message.
type failClose struct {
	interp.Records
	failed bool
}

func (f *failClose) SetTaskState(m, repo, id string, state store.TaskState) (store.Receipt, error) {
	if f.failed {
		return f.Records.SetTaskState(m, repo, id, state)
	}
	f.failed = true
	return store.Receipt{}, errInjected
}

// TestCloseReplayWithoutAudit: a merge that fails to close the task closes it when advanced again, with one merged
// message and no audit.
func TestCloseReplayWithoutAudit(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _ := newPublishEnv(t, b)
			e.eng.Config.Local = auditEveryTask
			e.toSignOff(t)
			e.eng.Records = &failClose{Records: e.eng.Records}
			e.answer(t, "deliver-1", "merge")
			ev := interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}
			if err := advanceErr(t, e, ev); !errors.Is(err, errInjected) {
				t.Fatalf("advance = %v, want the injected failure", err)
			}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
			e.wantNoAudit(t)
			var merged []string
			for _, id := range e.inbox(t) {
				if strings.HasPrefix(id, "app.t7.merged.") {
					merged = append(merged, id)
				}
			}
			if !slices.Equal(merged, []string{"app.t7.merged.deliver-1"}) {
				t.Errorf("merged messages = %q, want one", merged)
			}
		})
	}
}

// openEarlierAudit records the open audit an earlier release opened on task t7 at head before a close that failed.
func (e *env) openEarlierAudit(t *testing.T) {
	t.Helper()
	d := store.Decision{Repository: "app", TaskID: "t7", ID: "audit", Kind: store.DecisionAudit,
		Subject: &store.Subject{Kind: store.SubjectHead, Digest: head}, Options: []string{"yes", "no"}, Question: "Audit?"}
	if _, err := e.h.Records.OpenDecision("earlier-audit", d); err != nil {
		t.Fatal(err)
	}
}

// failSecondVoid fails the second decision void it is asked for, once, so a close stops part-way through its voids.
type failSecondVoid struct {
	interp.Records
	voids int
}

func (f *failSecondVoid) VoidDecision(m, repo, task, id, reason string) (store.Receipt, error) {
	if f.voids++; f.voids == 2 {
		return store.Receipt{}, errInjected
	}
	return f.Records.VoidDecision(m, repo, task, id, reason)
}

// TestCloseVoidsOpenDecisions: a task closed by the last Guildhall merge, whose merge answer overrode a question
// still open, or by the outside-merge rule ends done with no open decision: an audit an earlier release opened and the
// open question are both voided, with the close's reason, and the audit can no longer be answered. A close that fails
// part-way through its voids replays to the same voids and closes.
func TestCloseVoidsOpenDecisions(t *testing.T) {
	for _, outside := range []bool{false, true} {
		for _, b := range backends {
			t.Run(map[bool]string{false: "deliver", true: "outside"}[outside]+"/"+b.name, func(t *testing.T) {
				e, f := newPublishEnv(t, b)
				e.toSignOff(t)
				e.openEarlierAudit(t)
				q := store.Decision{Repository: "app", TaskID: "t7", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"}
				if _, err := e.h.Records.OpenDecision("question-q-1", q); err != nil {
					t.Fatal(err)
				}
				ev, reason := interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}, "Task done: pull request https://forge.invalid/1 merged"
				if outside {
					f.MergeOutside(repoRemote, 1, head, outsideCommit)
					ev, reason = interp.Event{Kind: interp.TaskCreated}, reason+" outside Guildhall"
				} else {
					e.answer(t, "deliver-1", "merge")
				}
				records := e.eng.Records
				e.eng.Records = &failSecondVoid{Records: records}
				if err := advanceErr(t, e, ev); !errors.Is(err, errInjected) {
					t.Fatalf("advance = %v, want the injected failure", err)
				}
				if got := e.task(t).State; got != store.TaskOpen {
					t.Fatalf("task state after the failed close = %q, want open", got)
				}
				want(t, e.advance(t, ev), done)
				e.eng.Records = records
				if got := e.task(t).State; got != store.TaskDone {
					t.Errorf("task state = %q, want done", got)
				}
				ds, err := e.h.Records.Decisions("app", "t7")
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range ds {
					if d.Open() {
						t.Errorf("decision %s is open on the done task", d.ID)
					}
				}
				for _, id := range []string{"audit", "q-1"} {
					if d := e.decision(t, id); !d.Void || d.VoidReason != reason {
						t.Errorf("%s = %+v, want it void with %q", id, d, reason)
					}
				}
				if _, err := e.h.AnswerDecision("answer:audit", "app", "t7", "audit", "yes"); !errors.Is(err, store.ErrInvalid) {
					t.Errorf("answer of the voided audit = %v, want ErrInvalid", err)
				}
			})
		}
	}
}

// TestNothingOpenSkipsASupersededAcceptance: an acceptance voided as superseded, here accept-1, with accept-2 answered
// in its place, is not unanswered: the merge decision's nothing_open holds and does not name it.
func TestNothingOpenSkipsASupersededAcceptance(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _ := newPublishEnv(t, b)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			if _, err := e.h.Records.VoidDecision("supersede-accept-1", "app", "t7", "accept-1", "Superseded: asked again as accept-2"); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			e.answer(t, "accept-2", "approved")
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			e.answer(t, "sign-off-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if q := e.decision(t, "deliver-1").Question; !strings.Contains(q, "- nothing_open: holds\n") || strings.Contains(q, "accept-1") {
				t.Errorf("merge question = %q, want nothing_open to hold with accept-1 superseded", q)
			}
		})
	}
}
