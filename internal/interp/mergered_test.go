package interp_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

var advanced = interp.Event{Kind: interp.TaskCreated}

// movedBase is the fake forge with the base moved under some heads: a pull request at a head in behind is behind the
// base named there, and any other observation is the fake's.
type movedBase struct {
	*forgetest.Fake
	behind map[string]string
}

func (m *movedBase) Mergeability(ctx context.Context, mg forge.Merge) (forge.Mergeability, error) {
	if base, ok := m.behind[mg.Head]; ok {
		return forge.Mergeability{Head: mg.Head, Base: base, State: "behind", Behind: 1}, nil
	}
	return m.Fake.Mergeability(ctx, mg)
}

// messageWith reports whether any message the task's run sent contains text.
func messageWith(t *testing.T, e *env, text string) bool {
	t.Helper()
	return slices.ContainsFunc(mapValues(e.messages(t)), func(m string) bool { return strings.Contains(m, text) })
}

// TestTrainRerunGreenLands: a train whose failed Actions job passes on its one rerun is green, not red: the batch
// lands as one train.
func TestTrainRerunGreenLands(t *testing.T) {
	e, f, _, s := leadToTesting(t)
	e.eng.CILogLimit = 1 << 20
	f.SetLog("123", jobLog)
	f.SetChecks(repoRemote, commitN(4), flakyChecks()...)
	want(t, e.advance(t, advanced), checksWait)
	if got := f.Reruns(); !slices.Equal(got, []string{"123"}) {
		t.Fatalf("reruns = %q, want the train's failed job once", got)
	}
	if b := batchOf(t, s); b.State != store.BatchTesting {
		t.Fatalf("batch during the rerun = %+v", b)
	}
	settle(t, f, commitN(4), forge.CheckPassed)
	want(t, e.advance(t, advanced), batchWait)
	if b := batchOf(t, s); b.State != store.BatchLanding {
		t.Fatalf("batch after the green rerun = %+v", b)
	}
	for _, m := range []struct{ id, link string }{{"t9", commitN(3)}, {"t8", commitN(4)}} {
		if _, err := s.SetMergeBatchReady("ready-"+m.id, "app", m.id, m.link); err != nil {
			t.Fatal(err)
		}
	}
	want(t, e.advance(t, advanced), done)
	if train := prNumbered(t, f, 2); train.State != forge.Merged || train.Head != commitN(4) {
		t.Fatalf("train = %+v", train)
	}
}

// TestTrainCulpritLeader: when the leader's own link is red too, the leader is the culprit: the train is closed, the
// leader goes back to build through the failed-checks escalation naming batch link 1, and its members keep their
// places, batched again.
func TestTrainCulpritLeader(t *testing.T) {
	e, f, _ := queueEnv(t, backends[1])
	e.eng.Config.Repositories[0].Merge.MergeBatchSize = ptr(3)
	s := peer(t, e, "t9")
	peer(t, e, "t8")
	for _, id := range []string{"t9", "t8"} {
		if _, err := s.SetTaskPriority("later-"+id, "app", id, store.PrioritySomeday); err != nil {
			t.Fatal(err)
		}
	}
	// The leader's pull request is clear: its link is its own head.
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), batchWait)
	if b := batchOf(t, s); b.Members[0].Link != head {
		t.Fatalf("batch = %+v", b)
	}
	for _, m := range []struct{ id, link string }{{"t9", commitN(3)}, {"t8", commitN(4)}} {
		if _, err := s.SetMergeBatchLink("link-"+m.id, "app", m.id, 1, m.link); err != nil {
			t.Fatal(err)
		}
	}
	f.SetChecks(repoRemote, commitN(4), ciFailed...)
	f.SetChecks(repoRemote, head, ciFailed...)
	build := expectEscalation(t, e)
	want(t, e.advance(t, advanced), build)
	if train := prNumbered(t, f, 2); train.State != forge.Closed {
		t.Fatalf("train = %+v, want closed", train)
	}
	if _, comments := f.Closes(); len(comments) != 1 || !strings.Contains(comments[0], "the checks of batch link 1 ("+head+") failed: ci") {
		t.Fatalf("close comments = %q", comments)
	}
	if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); !strings.Contains(esc["reason"].(string), "batch link 1") || esc["from"] != head {
		t.Fatalf("escalation = %v", esc)
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil || !slices.Equal(queueOrder(t, s), []string{"t9", "t8"}) {
		t.Fatalf("queue after the leader's send-back = %+v", q)
	}
}

// TestTrainCulpritSecondLeaderAlone: the first red link is the second member's: it goes back to build keeping its
// ready time, the member after it leaves the batch and keeps its place, and the leader, the green prefix of one,
// closes the train and merges its own pull request, already at its green link.
func TestTrainCulpritSecondLeaderAlone(t *testing.T) {
	e, f, _, s := trainOfThreeTesting(t)
	f.SetChecks(repoRemote, commitN(4), ciFailed...)
	f.SetChecks(repoRemote, commitN(3), ciFailed...)
	want(t, e.advance(t, advanced), batchWait)
	if b := batchOf(t, s); b.State != store.BatchRed || b.Members[0].Verdict != store.VerdictPassed {
		t.Fatalf("red batch = %+v", b)
	}
	build := expectEscalation(t, e)
	want(t, e.advanceTask(t, "t9", advanced), build)
	if b := batchOf(t, s); len(b.Members) != 1 || b.State != store.BatchRed {
		t.Fatalf("batch after the culprit left = %+v", b)
	}
	if !messageWith(t, e, "the members after it, t8, leave the batch and keep their places") {
		t.Fatalf("no culprit message: %v", e.messages(t))
	}
	if esc := e.taskDoc(t, "t9", "deliver-1-escalated-1"); !strings.Contains(esc["reason"].(string), "the checks of batch link 2 ("+commitN(3)+") failed") {
		t.Fatalf("t9's escalation = %v", esc)
	}
	want(t, e.advanceTask(t, "t8", advanced), batchWait)
	want(t, e.advance(t, advanced), done)
	if own := prNumbered(t, f, 1); own.State != forge.Merged || own.Head != head2 {
		t.Fatalf("leader's pull request = %+v, want merged alone at its link", own)
	}
	if train := prNumbered(t, f, 4); train.State != forge.Closed {
		t.Fatalf("train = %+v, want closed", train)
	}
	q, _ := s.MergeQueue("app")
	if q.Batch != nil || !slices.Equal(queueOrder(t, s), []string{"t8"}) || !slices.ContainsFunc(q.Out, func(o store.MergeQueueEntry) bool { return o.TaskID == "t9" }) {
		t.Fatalf("queue after the leader merged = %+v", q)
	}
}

// TestTrainCulpritLastSmallerTrain: the first red link is the last member's: it goes back to build, and the two
// green members re-point the train to the last green link, whose own checks are read bound to the train pull
// request, and land it as a smaller train.
func TestTrainCulpritLastSmallerTrain(t *testing.T) {
	e, f, _, s := trainOfThreeTesting(t)
	f.SetChecks(repoRemote, commitN(4), ciFailed...)
	f.SetChecks(repoRemote, commitN(3), ciPassed...)
	want(t, e.advance(t, advanced), batchWait)
	want(t, e.advanceTask(t, "t9", advanced), batchWait)
	build := expectEscalation(t, e)
	want(t, e.advanceTask(t, "t8", advanced), build)
	if b := batchOf(t, s); len(b.Members) != 2 || b.Members[1].Verdict != store.VerdictPassed {
		t.Fatalf("batch after the culprit left = %+v", b)
	}
	if !messageWith(t, e, "no other member is after it") {
		t.Fatalf("no culprit message: %v", e.messages(t))
	}
	// The leader re-points the train to t9's link; its checks are green, and the batch lands.
	want(t, e.advance(t, advanced), batchWait)
	if train := prNumbered(t, f, 4); train.State != forge.Open || train.Head != commitN(3) {
		t.Fatalf("re-pointed train = %+v", train)
	}
	if b := batchOf(t, s); b.State != store.BatchLanding || b.Train.Head != commitN(3) {
		t.Fatalf("batch after the re-pointed train = %+v", b)
	}
	if !messageWith(t, e, "is down to its green members t7, t9") {
		t.Fatalf("no bisected message: %v", e.messages(t))
	}
	want(t, e.advanceTask(t, "t9", advanced), batchWait)
	// t9's read of its link checks was bound to its own pull request; the train's are read again, bound to the train.
	if v := e.taskDoc(t, "t9", "deliver-1-checks-"+commitN(3)+"-pr-4"); v["number"] != float64(4) {
		t.Fatalf("t9's read of the train = %v", v)
	}
	want(t, e.advance(t, advanced), done)
	want(t, e.advanceTask(t, "t9", advanced), done)
	if train := prNumbered(t, f, 4); train.State != forge.Merged || train.Head != commitN(3) {
		t.Fatalf("train = %+v", train)
	}
	if _, merges := f.Calls(); merges != 1 {
		t.Fatalf("merges = %d", merges)
	}
	if task, _ := s.Task("app", "t8"); task.State != store.TaskOpen {
		t.Fatalf("t8 = %+v, want open and back at build", task)
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil || len(q.Entries) != 0 {
		t.Fatalf("queue after the smaller train = %+v", q)
	}
}

// TestTrainBaseMovedReassembles: a train behind the base at its merge is not merged: the batch goes back to
// assembling, the leader rebases onto the new tip, the members replay onto it, and the train is re-pointed to the new
// last link and lands there.
func TestTrainBaseMovedReassembles(t *testing.T) {
	e, f, ws, s := trainOfThreeReady(t)
	moved := &movedBase{Fake: f, behind: map[string]string{commitN(4): baseN(2)}}
	e.eng.Forge = moved
	want(t, e.advance(t, advanced), batchWait)
	if b := batchOf(t, s); b.State != store.BatchAssembling {
		t.Fatalf("batch after the base moved = %+v", b)
	}
	if _, merges := f.Calls(); merges != 0 || !messageWith(t, e, "is assembled again") {
		t.Fatalf("merges = %d, messages %v", merges, e.messages(t))
	}

	// The leader rebases onto the new tip, and its members replay onto its new link.
	moved.behind = map[string]string{head2: baseN(2)}
	clean(ws, baseN(2), head2, commitN(5))
	f.SetChecks(repoRemote, commitN(5), ciPassed...)
	want(t, e.advance(t, advanced), batchWait)
	if b := batchOf(t, s); b.State != store.BatchAssembling || b.Members[0].Link != commitN(5) || b.Members[1].Link != "" {
		t.Fatalf("batch after the leader's rebase = %+v", b)
	}
	clean(ws, commitN(5), head, commitN(6))
	want(t, e.advanceTask(t, "t9", advanced), batchWait)
	clean(ws, commitN(6), head, commitN(7))
	want(t, e.advanceTask(t, "t8", advanced), batchWait)
	f.SetChecks(repoRemote, commitN(7), ciPassed...)
	want(t, e.advance(t, advanced), batchWait)
	if train := prNumbered(t, f, 4); train.Head != commitN(7) || train.State != forge.Open {
		t.Fatalf("re-pointed train = %+v", train)
	}
	for _, id := range []string{"t9", "t8"} {
		want(t, e.advanceTask(t, id, advanced), batchWait)
	}
	want(t, e.advance(t, advanced), done)
	for _, id := range []string{"t9", "t8"} {
		want(t, e.advanceTask(t, id, advanced), done)
	}
	if train := prNumbered(t, f, 4); train.State != forge.Merged || train.Head != commitN(7) {
		t.Fatalf("train = %+v", train)
	}
	if _, merges := f.Calls(); merges != 1 {
		t.Fatalf("merges = %d, want one", merges)
	}
	wantNoOutside(t, e)
}

// TestTrainRebaseCapEndsBatch: a base that keeps moving under a batch spends the leader's maxRebaseRounds; then the
// batch ends, its train closed, the members keep their places, and the leader goes on alone, waiting for the
// Director as a serial front does.
func TestTrainRebaseCapEndsBatch(t *testing.T) {
	e, f, ws, s := trainOfThreeReady(t)
	moved := &movedBase{Fake: f, behind: map[string]string{commitN(4): baseN(2)}}
	e.eng.Forge = moved
	want(t, e.advance(t, advanced), batchWait)
	moved.behind = map[string]string{head2: baseN(2), commitN(5): baseN(3), commitN(6): baseN(4)}
	clean(ws, baseN(2), head2, commitN(5))
	clean(ws, baseN(3), commitN(5), commitN(6))
	f.SetChecks(repoRemote, commitN(5), ciPassed...)
	f.SetChecks(repoRemote, commitN(6), ciPassed...)
	want(t, e.advance(t, advanced), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
	if train := prNumbered(t, f, 4); train.State != forge.Closed {
		t.Fatalf("train = %+v, want closed", train)
	}
	if _, comments := f.Closes(); len(comments) != 1 || !strings.Contains(comments[0], "already rebased 3 times") {
		t.Fatalf("close comments = %q", comments)
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil || !slices.Equal(queueOrder(t, s), []string{"t7", "t9", "t8"}) {
		t.Fatalf("queue after the cap = %+v", q)
	}
	if !slices.ContainsFunc(mapKeys(e.messages(t)), func(id string) bool { return strings.Contains(id, ".rebase-stuck.") }) {
		t.Fatalf("no rebase-stuck message: %v", e.messages(t))
	}
}

func mapKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
