package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// advanceTask is env.advance for task id of app, beside t7.
func (e *env) advanceTask(t *testing.T, id string, ev interp.Event) interp.Next {
	t.Helper()
	ev.Repository, ev.Task = "app", id
	next, err := e.eng.Advance(context.Background(), ev)
	if err != nil {
		t.Fatalf("Advance(%+v) = %v", ev, err)
	}
	t.Logf("Advance(%+v) => %+v", ev, next)
	return next
}

// taskDoc reads the JSON document name recorded for task id of app.
func (e *env) taskDoc(t *testing.T, id, name string) map[string]any {
	t.Helper()
	docs, err := e.h.Records.Documents("app", id)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(docs, func(d store.DocumentRef) bool { return d.Name == name })
	if i < 0 {
		t.Fatalf("task %s: document %s not recorded", id, name)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(docs[i].Path)))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("task %s: document %s: %v", id, name, err)
	}
	return v
}

// toMergeAs creates task id of app beside t7 and runs it through validate.json as the engine runs t7, up to its
// merge decision deliver-1, which it answers merge; the task joins the merge queue and waits there.
func (e *env) toMergeAs(t *testing.T, s *store.Store, id string) {
	t.Helper()
	task, err := s.Task("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	task.ID, task.Title = id, "Peer "+id
	if _, err := s.CreateTask("create-"+id, task); err != nil {
		t.Fatal(err)
	}
	e.script.Expect(
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""),
		reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head, nil)))
	e.advanceTask(t, id, interp.Event{Kind: interp.TaskCreated})
	e.advanceTask(t, id, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	if _, err := s.AnswerDecision("answer:"+id+":accept-1", "app", id, "accept-1", "approved"); err != nil {
		t.Fatal(err)
	}
	e.advanceTask(t, id, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	e.advanceTask(t, id, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	want(t, e.advanceTask(t, id, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if _, err := s.AnswerDecision("answer:"+id+":deliver-1", "app", id, "deliver-1", "merge"); err != nil {
		t.Fatal(err)
	}
	want(t, e.advanceTask(t, id, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), batchWait)
}

// trainOfThree runs app/t7, t9 and t8 each through the engine into one merge batch of 3 that t7 leads, and on to
// the point where every member is ready and the leader's next advance merges the train. t7's pull request is #1,
// t9's #2 and t8's #3; t1 holds the front while t9 and t8 queue, so t7 claims the batch once it leaves. The leader
// is rebased onto baseN(1) as head2, t9 links at commitN(3) and t8 at commitN(4), and the train pull request #4 is
// at commitN(4) with its own checks green.
func trainOfThree(t *testing.T) (*env, *forgetest.Fake, *store.Store) {
	t.Helper()
	e, f, ws := queueEnv(t, backends[1])
	e.eng.Config.Repositories[0].Merge.MergeBatchSize = ptr(3)
	s := peer(t, e, "t1")
	if _, err := s.ClaimMergeFront("claim-t1", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t9", "t8"} {
		e.toMergeAs(t, s, id)
		if _, err := s.SetTaskPriority("later-"+id, "app", id, store.PrioritySomeday); err != nil {
			t.Fatal(err)
		}
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), batchWait)
	if _, err := s.LeaveMergeQueue("t1-gone", "app", "t1", false); err != nil {
		t.Fatal(err)
	}
	if got := queueOrder(t, s); !slices.Equal(got, []string{"t7", "t9", "t8"}) {
		t.Fatalf("queue = %v", got)
	}

	// The leader claims the batch and rebases onto the moved base.
	clean(ws, baseN(1), head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
	b := batchOf(t, s)
	if b.Leader != "t7" || len(b.Members) != 3 || b.Members[1].TaskID != "t9" || b.Members[2].TaskID != "t8" || b.Members[0].Link != head2 {
		t.Fatalf("batch = %+v", b)
	}
	f.SetChecks(repoRemote, head2, ciPassed...)
	f.SetMergeability(forge.Mergeability{})

	// Each member links its head onto the link before it, in queue order.
	clean(ws, head2, head, commitN(3))
	want(t, e.advanceTask(t, "t9", interp.Event{Kind: interp.TaskCreated}), batchWait)
	clean(ws, commitN(3), head, commitN(4))
	want(t, e.advanceTask(t, "t8", interp.Event{Kind: interp.TaskCreated}), batchWait)
	if b := batchOf(t, s); b.Members[1].Link != commitN(3) || b.Members[2].Link != commitN(4) {
		t.Fatalf("batch after the links = %+v", b)
	}

	// The leader opens the train and reads its checks; green, the batch lands.
	f.SetChecks(repoRemote, commitN(4), ciPendingOn...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), checksWait)
	if train := prNumbered(t, f, 4); train.Branch != trainBranch || train.Head != commitN(4) {
		t.Fatalf("train = %+v", train)
	}
	f.SetChecks(repoRemote, commitN(4), ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if b := batchOf(t, s); b.State != store.BatchLanding || !b.Members[0].Ready {
		t.Fatalf("batch after the green train = %+v", b)
	}

	// Each member authorizes its link and is ready.
	for _, id := range []string{"t9", "t8"} {
		want(t, e.advanceTask(t, id, interp.Event{Kind: interp.TaskCreated}), batchWait)
	}
	if b := batchOf(t, s); !b.Members[1].Ready || !b.Members[2].Ready {
		t.Fatalf("batch with every member authorized = %+v", b)
	}
	if _, merges := f.Calls(); merges != 0 {
		t.Fatalf("merged %d times before the leader's advance", merges)
	}
	return e, f, s
}

// wantTrainLanded checks the outcome of a landed train of t7, t9 and t8: the train #4 merged once, the three member
// pull requests closed each with one comment linking it, each task's -merge naming the train, one merged message
// each, every task done, the queue empty, and no outside merge announced.
func wantTrainLanded(t *testing.T, e *env, f *forgetest.Fake, s *store.Store) {
	t.Helper()
	if _, merges := f.Calls(); merges != 1 {
		t.Fatalf("merges = %d, want the train merged once", merges)
	}
	train := prNumbered(t, f, 4)
	if train.State != forge.Merged || train.Head != commitN(4) {
		t.Fatalf("train = %+v", train)
	}
	comment := "Landed by Guildhall in merge train " + train.URL + " as " + train.MergeCommit + "."
	if _, comments := f.Closes(); !slices.Equal(comments, []string{comment, comment, comment}) {
		t.Fatalf("close comments = %q", comments)
	}
	for _, m := range []struct {
		id, merge, link string
		own             int
	}{{"t7", "deliver-2-merge", head2, 1}, {"t9", "deliver-1-merge", commitN(3), 2}, {"t8", "deliver-1-merge", commitN(4), 3}} {
		if pr := prNumbered(t, f, m.own); pr.State != forge.Closed {
			t.Errorf("%s's pull request = %+v, want closed", m.id, pr)
		}
		d := e.taskDoc(t, m.id, m.merge)
		tr, _ := d["train"].(map[string]any)
		if d["number"] != float64(4) || d["merge_commit"] != train.MergeCommit || tr["leader"] != "t7" || tr["link"] != m.link {
			t.Errorf("%s's %s = %v", m.id, m.merge, d)
		}
		if own, _ := tr["pull_request"].(map[string]any); own["number"] != float64(m.own) {
			t.Errorf("%s's own pull request in the record = %v", m.id, tr["pull_request"])
		}
		task, err := s.Task("app", m.id)
		if err != nil || task.State != store.TaskDone {
			t.Errorf("task %s = %+v, %v; want done", m.id, task, err)
		}
		merged := 0
		for id, text := range e.messages(t) {
			if strings.HasPrefix(id, "app."+m.id+".merged.") {
				merged++
				if !strings.Contains(text, "merge train #4") {
					t.Errorf("%s's merged message = %q", m.id, text)
				}
			}
		}
		if merged != 1 {
			t.Errorf("%s sent %d merged messages, want 1", m.id, merged)
		}
	}
	wantNoOutside(t, e)
	if q, err := s.MergeQueue("app"); err != nil || q.Batch != nil || q.Front != nil || len(q.Entries) != 0 {
		t.Fatalf("queue after the train landed = %+v, %v", q, err)
	}
}

// TestTrainOfThreeLands: three tasks run through the engine into one batch: the leader opens one train pull request
// and merges it once, and each member records its -merge naming the train, closes its own pull request with the
// comment and ends done, the last clearing the batch.
func TestTrainOfThreeLands(t *testing.T) {
	e, f, s := trainOfThree(t)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if b := batchOf(t, s); b.State != store.BatchLanded {
		t.Fatalf("batch after the leader = %+v", b)
	}
	for _, id := range []string{"t9", "t8"} {
		want(t, e.advanceTask(t, id, interp.Event{Kind: interp.TaskCreated}), done)
	}
	wantTrainLanded(t, e, f, s)
}

// failingLeave fails the merge queue's leave of task, as a crash after a member's -merge and its pull request's
// close, before it leaves the queue, would.
type failingLeave struct {
	interp.Records
	queueRecords
	task string
}

func (r *failingLeave) LeaveMergeQueue(m, repo, id string, keepReady bool) (store.Receipt, error) {
	if id == r.task {
		return store.Receipt{}, errors.New("interrupted before leaving the queue")
	}
	return r.queueRecords.LeaveMergeQueue(m, repo, id, keepReady)
}

// TestTrainMemberCrashReplay: an advance of the leader or a member interrupted after its train -merge is recorded
// replays from that record alone: the same -merge, one close comment and one merged message per task, every task
// done, the batch cleared, and no outside merge announced.
func TestTrainMemberCrashReplay(t *testing.T) {
	for _, crashed := range []string{"t7", "t9"} {
		t.Run(crashed, func(t *testing.T) {
			e, f, s := trainOfThree(t)
			advance := func(id string) (interp.Next, error) {
				return e.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: id})
			}
			for _, id := range []string{"t7", "t9", "t8"} {
				if id != crashed {
					want(t, e.advanceTask(t, id, interp.Event{Kind: interp.TaskCreated}), done)
					continue
				}
				records := e.eng.Records
				e.eng.Records = &failingLeave{Records: records, queueRecords: records.(interp.MergeQueue), task: id}
				if next, err := advance(id); err == nil || !strings.Contains(err.Error(), "interrupted before leaving the queue") {
					t.Fatalf("interrupted advance of %s = %+v, %v", id, next, err)
				}
				e.eng.Records = records
				merge := "deliver-1-merge"
				if id == "t7" {
					merge = "deliver-2-merge"
				}
				if d := e.taskDoc(t, id, merge); d["number"] != float64(4) {
					t.Fatalf("%s recorded before the interruption = %v", merge, d)
				}
				if q, _ := s.MergeQueue("app"); !slices.Contains(queueOrder(t, s), id) || q.Batch == nil {
					t.Fatalf("queue after the interruption = %+v", q)
				}
				want(t, e.advanceTask(t, id, interp.Event{Kind: interp.TaskCreated}), done)
			}
			wantTrainLanded(t, e, f, s)
		})
	}
}
