package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// trainBatch is a store whose batch of a, b and c, a leading, is linked at linkA, linkB and linkC and testing; d
// waits behind it.
func trainBatch(t *testing.T) *Store {
	t.Helper()
	s := queueStore(t, "a", "b", "c", "d")
	for _, id := range []string{"a", "b", "c", "d"} {
		authorize(t, s, id, 1)
	}
	if _, err := s.ClaimMergeBatch("batch-a", "app", "a", 1, 3); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ id, link string }{{"a", linkA}, {"b", linkB}, {"c", linkC}} {
		if _, err := s.SetMergeBatchLink("link-"+step.id, "app", step.id, 1, step.link); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetMergeBatchState("testing", "app", "a", BatchTesting, tipM); err != nil {
		t.Fatal(err)
	}
	return s
}

func standingTrain(t *testing.T, s *Store) MergeBatch {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil || q.Batch == nil {
		t.Fatalf("batch = %+v, %v", q.Batch, err)
	}
	return *q.Batch
}

var (
	trainRemote = "https://github.com/acme/app.git"
	mergeCommit = strings.Repeat("f", 40)
)

// TestMergeBatchTrainAndReady: the leader records its train pull request, refused from anyone else or with an
// invalid number, head or merge commit; members mark themselves ready only while the batch is landing and at their
// own link; a train pushed to a new head while landing sends the batch back to testing and clears every readiness,
// as a member leaving sends it back to assembling.
func TestMergeBatchTrainAndReady(t *testing.T) {
	s := trainBatch(t)
	train := MergeTrain{Remote: trainRemote, Number: 7, URL: "https://github.com/acme/app/pull/7", Head: linkC}
	for name, err := range map[string]error{
		"not leader":   second(s.SetMergeBatchTrain("train-b", "app", "b", train)),
		"number 0":     second(s.SetMergeBatchTrain("train-0", "app", "a", MergeTrain{Remote: trainRemote, Head: linkC})),
		"no remote":    second(s.SetMergeBatchTrain("train-r", "app", "a", MergeTrain{Number: 7, Head: linkC})),
		"short head":   second(s.SetMergeBatchTrain("train-h", "app", "a", MergeTrain{Remote: trainRemote, Number: 7, Head: "abc"})),
		"bad commit":   second(s.SetMergeBatchTrain("train-m", "app", "a", MergeTrain{Remote: trainRemote, Number: 7, Head: linkC, MergeCommit: "main"})),
		"merged early": second(s.SetMergeBatchTrain("train-e", "app", "a", MergeTrain{Remote: trainRemote, Number: 7, Head: linkC, MergeCommit: mergeCommit})),
		"ready early":  second(s.SetMergeBatchReady("ready-early", "app", "b", linkB)),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.SetMergeBatchTrain("train", "app", "a", train); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("landing", "app", "a", BatchLanding, ""); err != nil {
		t.Fatal(err)
	}
	if err := second(s.SetMergeBatchReady("ready-wrong", "app", "b", linkA)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ready at another link = %v", err)
	}
	for _, m := range []struct{ id, link string }{{"a", linkA}, {"b", linkB}} {
		if _, err := s.SetMergeBatchReady("ready-"+m.id, "app", m.id, m.link); err != nil {
			t.Fatal(err)
		}
	}
	if b := standingTrain(t, s); b.Train == nil || *b.Train != train || !b.Members[0].Ready || !b.Members[1].Ready || b.Members[2].Ready {
		t.Fatalf("batch = %+v", b)
	}
	// The train re-pointed while landing: testing again, nobody ready.
	moved := train
	moved.Head = linkB
	if _, err := s.SetMergeBatchTrain("train-moved", "app", "a", moved); err != nil {
		t.Fatal(err)
	}
	if b := standingTrain(t, s); b.State != BatchTesting || slices.ContainsFunc(b.Members, func(m MergeBatchMember) bool { return m.Ready }) {
		t.Fatalf("batch after the train moved = %+v", b)
	}
	if _, err := s.SetMergeBatchTrain("train-back", "app", "a", train); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("landing-2", "app", "a", BatchLanding, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchReady("ready-b-2", "app", "b", linkB); err != nil {
		t.Fatal(err)
	}
	// c leaving while landing: back to assembling, nobody ready.
	if _, err := s.LeaveMergeBatch("leave-c", "app", "c"); err != nil {
		t.Fatal(err)
	}
	if b := standingTrain(t, s); b.State != BatchAssembling || len(b.Members) != 2 || b.Members[1].Ready {
		t.Fatalf("batch after c left = %+v", b)
	}
}

// TestMergeBatchLanded: a landed batch outlives its leader's front: the leader leaving the queue finishes it in the
// batch, the batch stands and refuses every front claim while a member that has not finished is queued, a member
// leaving the batch changes nothing and its links are fixed, and the last member to leave the queue removes it, so
// the next task claims the front. A merged train is never changed.
func TestMergeBatchLanded(t *testing.T) {
	s := trainBatch(t)
	train := MergeTrain{Remote: trainRemote, Number: 7, URL: "https://github.com/acme/app/pull/7", Head: linkC}
	if _, err := s.SetMergeBatchTrain("train", "app", "a", train); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("landing", "app", "a", BatchLanding, ""); err != nil {
		t.Fatal(err)
	}
	train.MergeCommit = mergeCommit
	if _, err := s.SetMergeBatchTrain("merged", "app", "a", train); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("landed", "app", "a", BatchLanded, ""); err != nil {
		t.Fatal(err)
	}
	other := train
	other.Head = linkB
	for name, err := range map[string]error{
		"train changed": second(s.SetMergeBatchTrain("changed", "app", "a", other)),
		"link":          second(s.SetMergeBatchLink("link-b-2", "app", "b", 1, linkA)),
		"back":          second(s.SetMergeBatchState("back", "app", "a", BatchAssembling, "")),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.LeaveMergeQueue("a-merged", "app", "a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveMergeBatch("b-leaves", "app", "b"); err != nil {
		t.Fatal(err)
	}
	q, _ := s.MergeQueue("app")
	if q.Front != nil || q.Batch == nil || q.Batch.State != BatchLanded || !q.Batch.Members[0].Finished || len(q.Batch.Members) != 3 {
		t.Fatalf("queue after the leader finished = %+v, batch %+v", q, q.Batch)
	}
	for _, id := range []string{"b", "d"} {
		if _, err := s.ClaimMergeFront("claim-"+id, "app", id); !errors.Is(err, ErrMergeQueued) {
			t.Errorf("claim by %s = %v, want ErrMergeQueued", id, err)
		}
	}
	for _, id := range []string{"b", "c"} {
		if _, err := s.LeaveMergeQueue(id+"-merged", "app", id, false); err != nil {
			t.Fatal(err)
		}
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil {
		t.Fatalf("batch after every member finished = %+v", q.Batch)
	}
	if _, err := s.ClaimMergeFront("claim-d-2", "app", "d"); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.MergeQueue("app"); q.Front == nil || q.Front.TaskID != "d" {
		t.Fatalf("front = %+v", q.Front)
	}
}
