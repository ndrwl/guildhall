package interp_test

import (
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// frontOf is the store's front of app's merge queue and t7's entry in it.
func frontOf(t *testing.T, s *store.Store) (*store.MergeFront, store.QueuedEntry) {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range q.Entries {
		if en.TaskID == "t7" {
			return q.Front, en
		}
	}
	return q.Front, store.QueuedEntry{}
}

// claimed fails unless the store holds a receipt for task under mutation id.
func claimed(t *testing.T, s *store.Store, id, task string) {
	t.Helper()
	rc, err := s.Receipt(id)
	if err != nil || rc.TaskID != task {
		t.Fatalf("receipt %s = %+v, %v, want one for %s", id, rc, err, task)
	}
}

// TestQueueLaterSliceClaimsItsOwnFront: d148. Slice 2 of a multi-PR task claims the merge front under a mutation id
// keyed on its deliver step, not slice 1's merge-front:app:t7:1, so it waits behind another task's front instead of
// replaying slice 1's receipt and merging past it; once first it holds a real front other tasks cannot claim, a
// replayed advance keeps that one stay, and a hold releases it under the slice's own release id.
func TestQueueLaterSliceClaimsItsOwnFront(t *testing.T) {
	e, f, _ := newSliceEnv(t, backends[1], true, sliceScript(t, threeSlices, 1, 2)...)
	s := e.h.Records.(*store.Store)
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
	// Slice 1 and every one-slice task keep today's id.
	claimed(t, s, "merge-front:app:t7:1", "t7")

	want(t, e.toMergeDecision(t, 2), interp.Next{Wait: interp.WaitDecision, Step: "deliver--s2", Decision: "deliver--s2-1"})
	peer(t, e, "t0")
	if _, err := s.ClaimMergeFront("front-t0", "app", "t0"); err != nil {
		t.Fatal(err)
	}
	slice2Wait := interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver--s2"}
	want(t, e.mergeSlice(t, 2), slice2Wait)
	if pr := f.PullRequests(repoRemote)[1]; pr.State == forge.Merged {
		t.Fatal("slice 2 merged past t0's front")
	}
	if front, _ := frontOf(t, s); front == nil || front.TaskID != "t0" {
		t.Fatalf("front = %+v, want t0", front)
	}

	// t0 merges; slice 2 is first and claims a real front, held there by a forge refusing the merge.
	if _, err := s.LeaveMergeQueue("leave-t0", "app", "t0", false); err != nil {
		t.Fatal(err)
	}
	f.Refuse("blocked by a test")
	refused := interp.Next{Wait: interp.WaitDirector, Step: "deliver--s2", Decision: "deliver--s2-1"}
	for range 2 { // the second advance replays the claim in the same stay
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), refused)
		front, entry := frontOf(t, s)
		if front == nil || *front != (store.MergeFront{TaskID: "t7", Stay: 1}) || entry.Stays != 1 || !entry.Front {
			t.Fatalf("front = %+v, entry = %+v, want t7 at the front in stay 1", front, entry)
		}
	}
	claimed(t, s, "merge-front:app:t7:deliver--s2:1", "t7")
	peer(t, e, "t1")
	if _, err := s.ClaimMergeFront("front-t1", "app", "t1"); !errors.Is(err, store.ErrMergeQueued) {
		t.Fatalf("claim by t1 behind slice 2's front = %v, want ErrMergeQueued", err)
	}

	// A hold releases slice 2's front under its own release id, and t1 may claim it.
	if _, err := s.SetMergeHold("hold-t7", "app", "t7", true); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), slice2Wait)
	claimed(t, s, "merge-release:app:t7:deliver--s2:1", "t7")
	if _, err := s.ClaimMergeFront("front-t1", "app", "t1"); err != nil {
		t.Fatalf("claim by t1 after slice 2 was held = %v", err)
	}
}

// TestQueueReplayedClaimMakesNoFront: d148. A claim whose mutation id the store already answered, while another task
// holds the front, replays that receipt and writes nothing; the task waits behind the store's front and pushes and
// merges nothing.
func TestQueueReplayedClaimMakesNoFront(t *testing.T) {
	e, f, _ := queueEnv(t, backends[1])
	s := e.h.Records.(*store.Store)
	e.answer(t, "deliver-1", "merge")
	// A receipt under the exact id t7's claim will send, from an earlier stay the store no longer records as front.
	if _, err := s.JoinMergeQueue("earlier-join", "app", "t7", "deliver-0", "decision/deliver-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("merge-front:app:t7:1", "app", "t7"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveMergeQueue("earlier-leave", "app", "t7", false); err != nil {
		t.Fatal(err)
	}
	peer(t, e, "t0")
	if _, err := s.ClaimMergeFront("front-t0", "app", "t0"); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, answeredMerge), queuedWait)
	if got := mergedHead(f); got != "" {
		t.Fatalf("merged %s while t0 holds the front", got)
	}
	if front, entry := frontOf(t, s); front == nil || front.TaskID != "t0" || entry.Front {
		t.Fatalf("front = %+v, t7 entry = %+v, want t0 at the front", front, entry)
	}
}
