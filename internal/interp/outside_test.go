package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// outsideCommit is the commit a pull request merged outside Guildhall landed on.
var outsideCommit = strings.Repeat("9", 40)

// retireRecorder is the workspace double recording the head each retirement was asked at, keeping the workspace of
// a head Guildhall never fetched as git.Program does.
type retireRecorder struct {
	*gittest.Workspaces
	known map[string]bool
	heads []string
}

func (r *retireRecorder) Retire(_ context.Context, rt git.Retirement) ([]string, error) {
	r.heads = append(r.heads, rt.Head)
	if !r.known[rt.Head] {
		return []string{"commit " + rt.Head + " is not in the repository"}, nil
	}
	return nil, nil
}

// wantMergedOutside checks the records of task t7 closed by the outside-merge rule for pull request 1 merged at
// forgeHead after Guildhall recorded recorded, and that no decision is left open and none is an audit.
func (e *env) wantMergedOutside(t *testing.T, f *forgetest.Fake, forgeHead, recorded string) {
	t.Helper()
	url := f.PullRequests(repoRemote)[0].URL
	pr := e.pullRequestDoc(t, "outside-1-merge")
	if pr["schema"] != "guildhall.pull-request/1" || pr["state"] != "merged" || pr["head"] != forgeHead || pr["merge_commit"] != outsideCommit ||
		pr["url"] != url || pr["remote"] != repoRemote || pr["number"] != float64(1) {
		t.Errorf("outside-1-merge = %v", pr)
	}
	text := e.messages(t)["app.t7.merged.outside.1"]
	for _, part := range []string{"merged outside Guildhall", url, forgeHead, outsideCommit, "last recorded head " + recorded} {
		if !strings.Contains(text, part) {
			t.Errorf("merged message %q does not name %q", text, part)
		}
	}
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Open() {
			t.Errorf("decision %s is still open", d.ID)
		}
		if d.Kind == store.DecisionAudit {
			t.Errorf("decision %s is an audit", d.ID)
		}
		if d.Void && d.VoidReason != "Task done: pull request "+url+" merged outside Guildhall" {
			t.Errorf("decision %s void reason = %q", d.ID, d.VoidReason)
		}
	}
	if got := e.task(t).State; got != store.TaskDone {
		t.Errorf("task state = %q, want done", got)
	}
	if _, merges := f.Calls(); merges != 0 {
		t.Errorf("merges = %d, want none: the pull request merged outside Guildhall", merges)
	}
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, interp.ErrTaskClosed) {
		t.Errorf("advance after the outside merge = %v, want ErrTaskClosed", err)
	}
}

// TestMergedOutsideAtTheMergeDecision: a pull request merged by hand on the forge while the task waits at the merge
// decision after publish closes the task at the next advance: the forge's merged pull request is recorded as
// outside-1-merge, the merge decision is voided, the Director is told it merged outside Guildhall, the workspace is
// retired at the forge's head and the task is done, with no merge of Guildhall's.
func TestMergedOutsideAtTheMergeDecision(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			ws := &retireRecorder{Workspaces: gittest.NewWorkspaces(), known: map[string]bool{head: true}}
			e.eng.Workspaces = ws
			f.MergeOutside(repoRemote, 1, head, outsideCommit)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			e.wantMergedOutside(t, f, head, head)
			if d := e.decision(t, "deliver-1"); !d.Void {
				t.Errorf("deliver-1 = %+v, want it voided", d)
			}
			if !slices.Equal(ws.heads, []string{head}) {
				t.Errorf("retired at %q, want %s", ws.heads, head)
			}
			if got := e.inbox(t); got[len(got)-1] != "app.t7.merged.outside.1" {
				t.Errorf("inbox = %q, want the outside merge last", got)
			}
			if publishes, _ := f.Calls(); publishes != 1 {
				t.Errorf("publishes = %d, want 1", publishes)
			}
		})
	}
}

// TestMergedOutsideWhileValidateWaits: a pull request merged by hand while validate waits for its checks closes the
// task, whether it merged at the head Guildhall published or after the head moved on the forge (d65): the moved
// head is one Guildhall never fetched, so the workspace is kept and the Director told.
func TestMergedOutsideWhileValidateWaits(t *testing.T) {
	for _, b := range backends {
		for _, moved := range []bool{false, true} {
			t.Run(b.name+map[bool]string{false: "/at the published head", true: "/at a moved head"}[moved], func(t *testing.T) {
				e, f := newValidateEnv(t, b)
				want(t, e.toValidate(t), waitChecks)
				ws := &retireRecorder{Workspaces: gittest.NewWorkspaces(), known: map[string]bool{head: true}}
				e.eng.Workspaces = ws
				at := head
				if moved {
					at = head3
					f.SetMergeability(forge.Mergeability{Head: head3})
				}
				f.MergeOutside(repoRemote, 1, at, outsideCommit)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
				e.wantMergedOutside(t, f, at, head)
				if !slices.Equal(ws.heads, []string{at}) {
					t.Errorf("retired at %q, want the forge's head %s", ws.heads, at)
				}
				kept := ""
				for id, text := range e.messages(t) {
					if strings.HasPrefix(id, "app.t7.workspace-kept.outside-1.") {
						kept = text
					}
				}
				if moved != (kept != "") || moved && !strings.Contains(kept, "merged outside Guildhall") {
					t.Errorf("workspace-kept = %q, want one only for the moved head, naming the outside merge", kept)
				}
			})
		}
	}
}

// TestMergedOutsideHeldAtTheFront: the d51 case. The task's first pull request was closed on the forge, so the front's
// rebase opened a second; its merge was then held in the merge queue. The first pull request merged by hand and the
// second closed: the next advance records the earlier merged one, takes the task out of the queue, clears its hold
// and closes it.
func TestMergedOutsideHeldAtTheFront(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	s := e.h.Records.(*store.Store)
	base := baseN(1)
	clean(ws, base, head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
	// A pull request only closed never closes the task.
	f.CloseOutside(repoRemote, 1)
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), deliverWait)
	if prs := f.PullRequests(repoRemote); len(prs) != 2 || prs[1].State != forge.Open || prs[1].Head != head2 {
		t.Fatalf("pull requests = %+v, want the rebase in a second", prs)
	}
	if _, err := s.SetMergeHold("hold-1", "app", "t7", true); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"})

	f.MergeOutside(repoRemote, 1, head, outsideCommit)
	f.CloseOutside(repoRemote, 2)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	e.wantMergedOutside(t, f, head, head)
	view, err := s.MergeQueue("app")
	if err != nil || view.Position("t7") != 0 || view.Front != nil || view.Holds["t7"] ||
		slices.ContainsFunc(view.Out, func(q store.MergeQueueEntry) bool { return q.TaskID == "t7" }) {
		t.Errorf("merge queue = %+v, %v, want t7 out of it with no hold", view, err)
	}
}

// TestMergedOutsideAtTheApprovedHeadWhileHeld: a held merge merged by hand at the very head the Guildmaster approved
// is not deliver's own, which merges nothing while held, so the rule closes the task; unheld, deliver records it.
func TestMergedOutsideAtTheApprovedHeadWhileHeld(t *testing.T) {
	for _, held := range []bool{true, false} {
		t.Run(map[bool]string{true: "held", false: "not held"}[held], func(t *testing.T) {
			e, f, _ := queueEnv(t, backends[1])
			s := e.h.Records.(*store.Store)
			f.SetMergeability(forge.Mergeability{})
			if _, err := s.SetMergeHold("hold-1", "app", "t7", true); err != nil {
				t.Fatal(err)
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"})
			f.MergeOutside(repoRemote, 1, head, outsideCommit)
			if !held {
				if _, err := s.SetMergeHold("hold-2", "app", "t7", false); err != nil {
					t.Fatal(err)
				}
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			_, outside := e.documents(t)["outside-1-merge"]
			_, own := e.documents(t)["deliver-1-merge"]
			if outside != held || own == held {
				t.Errorf("outside-1-merge recorded %t, deliver-1-merge recorded %t; want the outside merge only while held", outside, own)
			}
			if view, err := s.MergeQueue("app"); err != nil || view.Holds["t7"] || view.Position("t7") != 0 {
				t.Errorf("merge queue = %+v, %v, want t7 out of it with no hold", view, err)
			}
		})
	}
}

// TestMergedOutsideWhileBuildRuns: a pull request merged by hand after the Director sent the build back waits while
// the build's attempt runs, then closes the task at the advance its result brings, before publish could open a new
// pull request for the rebuilt head.
func TestMergedOutsideWhileBuildRuns(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			want(t, e.toValidate(t), waitChecks)
			feedback := "Resolve the conflict however appropriate."
			e.script.Expect(buildExp(t, 2, head2, "sha256:"+sum([]byte(feedback))))
			running := interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2}
			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}), running)
			f.MergeOutside(repoRemote, 1, head, outsideCommit)
			views := f.Views()
			// A running attempt defers the rule.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), running)
			if _, ok := e.documents(t)["outside-1-merge"]; ok || e.task(t).State != store.TaskOpen || f.Views() != views {
				t.Fatalf("the rule ran while the build's attempt ran")
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), done)
			e.wantMergedOutside(t, f, head, head)
			if prs := f.PullRequests(repoRemote); len(prs) != 1 {
				t.Errorf("pull requests = %+v, want no new one for the rebuilt head", prs)
			}
		})
	}
}

// TestPullRequestClosedOutsideKeepsTheTaskOpen: a pull request closed on the forge without merging is not landed work.
func TestPullRequestClosedOutsideKeepsTheTaskOpen(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			e.toSignOff(t)
			f.CloseOutside(repoRemote, 1)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if _, ok := e.documents(t)["outside-1-merge"]; ok || e.task(t).State != store.TaskOpen || !e.decision(t, "deliver-1").Open() {
				t.Errorf("a closed pull request closed the task")
			}
		})
	}
}

// noReader is a forge that cannot read a pull request.
type noReader struct{ interp.Forge }

// TestMergedOutsideWithoutAReader: a forge that cannot read pull requests leaves the task as it was.
func TestMergedOutsideWithoutAReader(t *testing.T) {
	e, f := newPublishEnv(t, backends[0])
	e.toSignOff(t)
	e.eng.Forge = noReader{f}
	f.MergeOutside(repoRemote, 1, head, outsideCommit)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if e.task(t).State != store.TaskOpen {
		t.Errorf("task closed without a reader")
	}
	if _, err := e.eng.Reconcile(context.Background(), "app", "t7"); !errors.Is(err, interp.ErrUnsupported) {
		t.Errorf("Reconcile without a reader = %v, want ErrUnsupported", err)
	}
}

// failingView is a forge whose pull request reads fail.
type failingView struct{ *forgetest.Fake }

func (failingView) PullRequest(context.Context, forge.View) (forge.PullRequest, error) {
	return forge.PullRequest{}, errUnreachable
}

// TestMergedOutsideReadFails: a pull request read that fails fails the advance and records nothing.
func TestMergedOutsideReadFails(t *testing.T) {
	e, f := newPublishEnv(t, backends[0])
	e.toSignOff(t)
	e.eng.Forge = failingView{f}
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errUnreachable) {
		t.Fatalf("advance = %v, want the read's error", err)
	}
	if e.task(t).State != store.TaskOpen {
		t.Errorf("task closed after a failed read")
	}
}

// crashAfter is records and an outbox that fail once just after the write op commits.
type crashAfter struct {
	interp.Records
	outbox interp.Outbox
	op     string
	done   bool
}

func (c *crashAfter) crash(op string, rc store.Receipt, err error) (store.Receipt, error) {
	if err != nil || op != c.op || c.done {
		return rc, err
	}
	c.done = true
	return store.Receipt{}, errInjected
}

func (c *crashAfter) PutDocument(m string, d store.DocumentRef) (store.Receipt, error) {
	rc, err := c.Records.PutDocument(m, d)
	return c.crash("document:"+d.Name, rc, err)
}

func (c *crashAfter) VoidDecision(m, repo, task, id, reason string) (store.Receipt, error) {
	rc, err := c.Records.VoidDecision(m, repo, task, id, reason)
	return c.crash("void:"+id, rc, err)
}

func (c *crashAfter) OpenDecision(m string, d store.Decision) (store.Receipt, error) {
	rc, err := c.Records.OpenDecision(m, d)
	return c.crash("decision:"+d.ID, rc, err)
}

func (c *crashAfter) SetTaskState(m, repo, task string, s store.TaskState) (store.Receipt, error) {
	rc, err := c.Records.SetTaskState(m, repo, task, s)
	return c.crash("state", rc, err)
}

func (c *crashAfter) Send(to store.Recipient, m messaging.Envelope) (store.Receipt, error) {
	rc, err := c.outbox.Send(to, m)
	return c.crash("message:"+m.ID, rc, err)
}

// TestMergedOutsideReplay: a crash just after each write of the rule replays, at the next advance, to the same records:
// one outside-1-merge, one merged message and the merge decision voided, with no audit even at quality_audit_sample 1.
func TestMergedOutsideReplay(t *testing.T) {
	for _, b := range backends {
		for _, op := range []string{"document:outside-1-merge", "void:deliver-1", "message:app.t7.merged.outside.1", "state"} {
			t.Run(b.name+"/"+op, func(t *testing.T) {
				e, f := newPublishEnv(t, b)
				e.eng.Config.Local = auditEveryTask
				e.toSignOff(t)
				c := &crashAfter{Records: e.eng.Records, outbox: e.eng.Outbox, op: op}
				e.eng.Records, e.eng.Outbox = c, c
				f.MergeOutside(repoRemote, 1, head, outsideCommit)
				views := f.Views()
				if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, errInjected) {
					t.Fatalf("advance = %v, want the injected crash", err)
				}
				if op == "state" {
					// The crash came after the task closed: there is nothing left to replay.
					if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, interp.ErrTaskClosed) {
						t.Fatalf("advance after the close = %v, want ErrTaskClosed", err)
					}
				} else {
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
				}
				e.wantMergedOutside(t, f, head, head)
				var merged []string
				for _, id := range e.inbox(t) {
					if strings.HasPrefix(id, "app.t7.merged.") {
						merged = append(merged, id)
					}
				}
				if !slices.Equal(merged, []string{"app.t7.merged.outside.1"}) {
					t.Errorf("merged messages = %q, want one", merged)
				}
				e.wantNoAudit(t)
				if d := e.decision(t, "deliver-1"); !d.Void {
					t.Errorf("deliver-1 = %+v, want it voided", d)
				}
				if got := f.Views() - views; got != 1 {
					t.Errorf("views = %d, want one: a replay reads the recorded merge, not the forge", got)
				}
			})
		}
	}
}

// TestReconcile: Engine.Reconcile changes nothing while no recorded pull request merged, closes the task once one
// merged outside Guildhall, and refuses a closed task.
func TestReconcile(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			e.toSignOff(t)
			ctx := context.Background()
			if got, err := e.eng.Reconcile(ctx, "app", "t7"); err != nil || got.Done {
				t.Fatalf("Reconcile before any merge = %+v, %v, want no change", got, err)
			}
			if e.task(t).State != store.TaskOpen || !e.decision(t, "deliver-1").Open() {
				t.Fatalf("Reconcile with nothing merged changed the task")
			}
			f.MergeOutside(repoRemote, 1, head, outsideCommit)
			got, err := e.eng.Reconcile(ctx, "app", "t7")
			if err != nil || !got.Done || got.URL != f.PullRequests(repoRemote)[0].URL {
				t.Fatalf("Reconcile = %+v, %v, want done with the pull request", got, err)
			}
			e.wantMergedOutside(t, f, head, head)
			if _, err := e.eng.Reconcile(ctx, "app", "t7"); !errors.Is(err, interp.ErrTaskClosed) {
				t.Errorf("Reconcile of a done task = %v, want ErrTaskClosed", err)
			}
		})
	}
}
