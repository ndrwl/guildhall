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

var auditEveryTask = map[string]string{config.QualitySelfReviewKey: "on", config.QualityAuditSampleKey: "1"}

// TestAuditAfterMerge: when the task falls in the audit sample, the merge that closes it opens the Guildmaster's
// three-question audit, kind audit, bound to the head that merged and naming its pull request, and tells the Director;
// the task is still done and its wait is still done.
func TestAuditAfterMerge(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			want(t, e.merged(t, auditEveryTask), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
			d := e.decision(t, "audit")
			if d.Kind != store.DecisionAudit || d.Subject == nil || *d.Subject != (store.Subject{Kind: store.SubjectHead, Digest: head}) ||
				!slices.Equal(d.Options, []string{"yes", "no"}) || d.Answered {
				t.Errorf("audit = %+v, want an open audit of head %s offering yes and no", d, head)
			}
			for _, part := range []string{f.PullRequests(repoRemote)[0].URL, head, "Would you have merged this as is?",
				"Was anything asked that should not have been?", "Was anything decided that should have been asked?"} {
				if !strings.Contains(d.Question, part) {
					t.Errorf("audit question %q does not contain %q", d.Question, part)
				}
			}
			if got := e.inbox(t); !slices.Equal(got[len(got)-2:], []string{"app.t7.merged.deliver-1", "app.t7.decision.audit"}) {
				t.Errorf("inbox = %q, want the merge then the audit last", got)
			}
			// The answer is recorded like any decision's, and the task stays closed.
			e.answer(t, "audit", "yes\n\nNothing asked needlessly; the port choice should have been asked.")
			if got := e.decision(t, "audit"); got.Choice != "yes" || got.Note != "Nothing asked needlessly; the port choice should have been asked." {
				t.Errorf("answered audit = %+v", got)
			}
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "audit"}); !errors.Is(err, interp.ErrTaskClosed) {
				t.Errorf("advance after the audit = %v, want ErrTaskClosed", err)
			}
		})
	}
}

// TestNoAuditOutsideTheSample: the audit is off by default, off while self-review is off whatever the sample,
// off at a sample of 0, and not opened for a task outside the sample; the merge closes the task the same way.
func TestNoAuditOutsideTheSample(t *testing.T) {
	outside := qualityOf(t, map[string]string{config.QualitySelfReviewKey: "on", config.QualityAuditSampleKey: "1000000"})
	if outside.Audits("app", "t7") {
		t.Fatal("app/t7 falls in a one-in-a-million sample; choose a larger sample")
	}
	for name, local := range map[string]map[string]string{
		"defaults":           nil,
		"self-review off":    {config.QualitySelfReviewKey: "off", config.QualityAuditSampleKey: "1"},
		"sample 0":           {config.QualitySelfReviewKey: "on", config.QualityAuditSampleKey: "0"},
		"outside the sample": {config.QualitySelfReviewKey: "on", config.QualityAuditSampleKey: "1000000"},
	} {
		for _, b := range backends {
			t.Run(name+"/"+b.name, func(t *testing.T) {
				e, _ := newPublishEnv(t, b)
				want(t, e.merged(t, local), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
				if got := e.task(t).State; got != store.TaskDone {
					t.Errorf("task state = %q, want done", got)
				}
				ds, err := e.h.Records.Decisions("app", "t7")
				if err != nil {
					t.Fatal(err)
				}
				if slices.ContainsFunc(ds, func(d store.Decision) bool { return d.Kind == store.DecisionAudit }) {
					t.Errorf("decisions = %+v, want no audit", ds)
				}
			})
		}
	}
}

// failClose fails the first SetTaskState, after the audit is opened.
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

// TestAuditReplay: a merge that fails to close the task after opening the audit opens it once when advanced again.
func TestAuditReplay(t *testing.T) {
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
			e.decision(t, "audit")
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			ds, err := e.h.Records.Decisions("app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			if n := len(slices.DeleteFunc(ds, func(d store.Decision) bool { return d.Kind != store.DecisionAudit })); n != 1 {
				t.Errorf("audits = %d, want 1", n)
			}
			if got := e.inbox(t); slices.Index(got, "app.t7.decision.audit") != len(got)-1 {
				t.Errorf("inbox = %q, want one audit message last", got)
			}
		})
	}
}

// qualityOf reads the effective quality settings of a config with local set.
func qualityOf(t *testing.T, local map[string]string) config.Quality {
	t.Helper()
	q, err := (&config.Config{Local: local}).Quality()
	if err != nil {
		t.Fatal(err)
	}
	return q
}
