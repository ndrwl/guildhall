package store

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

func batchMembers(b *MergeBatch) []string {
	if b == nil {
		return nil
	}
	var out []string
	for _, m := range b.Members {
		out = append(out, m.TaskID)
	}
	return out
}

func stays(t *testing.T, s *Store) map[string]int {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range q.Entries {
		out[e.TaskID] = e.Stays
	}
	return out
}

var (
	linkA = strings.Repeat("a", 40)
	linkB = strings.Repeat("b", 40)
	linkC = strings.Repeat("c", 64)
	tipM  = strings.Repeat("0", 40)
)

// TestMergeBatchOffIsTheSerialFront: a batch claim of size 1 or less claims the front alone, records no batch and
// counts only the front's stay, exactly as ClaimMergeFront.
func TestMergeBatchOffIsTheSerialFront(t *testing.T) {
	for _, size := range []int{0, 1} {
		s := queueStore(t, "a", "b")
		authorize(t, s, "a", 1)
		authorize(t, s, "b", 1)
		if _, err := s.ClaimMergeBatch("front-b", "app", "b", 1, size); !errors.Is(err, ErrMergeQueued) {
			t.Fatalf("size %d: claim behind a = %v", size, err)
		}
		if _, err := s.ClaimMergeBatch("front-a", "app", "a", 1, size); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		q, err := s.MergeQueue("app")
		if err != nil || q.Front == nil || q.Front.TaskID != "a" || q.Front.Stay != 1 || q.Batch != nil {
			t.Fatalf("size %d: queue = %+v, %v", size, q, err)
		}
		if got := stays(t, s); got["a"] != 1 || got["b"] != 0 {
			t.Fatalf("size %d: stays = %v", size, got)
		}
	}
	// The serial claim never records a batch either.
	s := queueStore(t, "a", "b")
	authorize(t, s, "a", 1)
	authorize(t, s, "b", 1)
	if _, err := s.ClaimMergeFront("front-a", "app", "a"); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil {
		t.Fatalf("serial front leads a batch: %+v", q.Batch)
	}
}

// TestMergeBatchClaim: the first task not held claims the front and leads a batch of the next tasks not held, in
// queue order, up to the size, each counting a stay in the same write; a held task is skipped and keeps its place,
// and tasks past the size are left out. Replaying the claim, or the leader claiming again, changes nothing, and a
// member's own front claim is refused.
func TestMergeBatchClaim(t *testing.T) {
	s := queueStore(t, "a", "b", "c", "d", "e")
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		authorize(t, s, id, 1)
	}
	if _, err := s.SetMergeHold("hold-b", "app", "b", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeBatch("batch-c", "app", "c", 1, 3); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("claim by c, behind a = %v", err)
	}
	rc, err := s.ClaimMergeBatch("batch-a", "app", "a", 2, 3)
	if err != nil || rc.Record != MergeQueueRecord {
		t.Fatalf("claim = %+v, %v", rc, err)
	}
	q, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	if q.Front == nil || q.Front.TaskID != "a" || q.Batch == nil {
		t.Fatalf("queue = %+v", q)
	}
	want := MergeBatch{Leader: "a", Stay: 1, State: BatchAssembling, Members: []MergeBatchMember{
		{TaskID: "a", Slice: 2, Stay: 1}, {TaskID: "c", Stay: 1}, {TaskID: "d", Stay: 1}}}
	if !batchEqual(*q.Batch, want) {
		t.Fatalf("batch = %+v, want %+v", *q.Batch, want)
	}
	if got := order(t, s); !slices.Equal(got, []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("order = %v, want the held b kept in its place", got)
	}
	before := stays(t, s)
	if before["a"] != 1 || before["b"] != 0 || before["c"] != 1 || before["d"] != 1 || before["e"] != 0 {
		t.Fatalf("stays = %v", before)
	}
	again, err := s.ClaimMergeBatch("batch-a", "app", "a", 2, 3)
	if err != nil || again.Sequence != rc.Sequence {
		t.Fatalf("replayed claim = %+v, %v; first %+v", again, err, rc)
	}
	if _, err := s.ClaimMergeBatch("batch-a-again", "app", "a", 2, 3); err != nil {
		t.Fatalf("the leader's second claim = %v", err)
	}
	if _, err := s.ClaimMergeFront("front-a-again", "app", "a"); err != nil {
		t.Fatalf("the leader's front claim = %v", err)
	}
	if got := stays(t, s); !maps.Equal(got, before) {
		t.Fatalf("stays after replays = %v, want %v", got, before)
	}
	if _, err := s.ClaimMergeBatch("batch-c-2", "app", "c", 1, 3); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("a member's claim = %v", err)
	}
	if _, err := s.ClaimMergeBatch("batch-bad", "app", "a", 0, 3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("slice 0 = %v", err)
	}
}

// TestMergeBatchLinksAndState: members record their slice and link; a changed link clears the links after it; the
// leader alone moves the batch along its allowed states and records the base; non-members, bad ids, unknown states
// and moves not allowed are refused.
func TestMergeBatchLinksAndState(t *testing.T) {
	s := queueStore(t, "a", "b", "c", "d")
	for _, id := range []string{"a", "b", "c", "d"} {
		authorize(t, s, id, 1)
	}
	if _, err := s.ClaimMergeBatch("batch-a", "app", "a", 1, 3); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ id, link string }{{"a", linkA}, {"b", linkB}, {"c", linkC}} {
		if _, err := s.SetMergeBatchLink("link-"+step.id, "app", step.id, 3, step.link); err != nil {
			t.Fatalf("link %s: %v", step.id, err)
		}
	}
	q, _ := s.MergeQueue("app")
	if l := q.Batch.Members; l[0].Link != linkA || l[1].Link != linkB || l[2].Link != linkC || l[1].Slice != 3 {
		t.Fatalf("links = %+v", l)
	}
	// b replays again: c's link, made on b's old one, is cleared; a's stays.
	if _, err := s.SetMergeBatchLink("link-b-2", "app", "b", 3, linkC); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if l := q.Batch.Members; l[0].Link != linkA || l[1].Link != linkC || l[2].Link != "" {
		t.Fatalf("links after b's replay = %+v", l)
	}
	for name, err := range map[string]error{
		"non-member": second(s.SetMergeBatchLink("link-d", "app", "d", 1, linkA)),
		"short link": second(s.SetMergeBatchLink("link-short", "app", "a", 1, "abc")),
		"upper link": second(s.SetMergeBatchLink("link-upper", "app", "a", 1, strings.Repeat("A", 40))),
		"slice 0":    second(s.SetMergeBatchLink("link-s0", "app", "a", 0, linkA)),
		"not leader": second(s.SetMergeBatchState("state-b", "app", "b", BatchTesting, "")),
		"unknown":    second(s.SetMergeBatchState("state-x", "app", "a", "merged", "")),
		"skip":       second(s.SetMergeBatchState("state-landed", "app", "a", BatchLanded, "")),
		"bad base":   second(s.SetMergeBatchState("state-base", "app", "a", BatchTesting, "main")),
		"no batch":   second(s.SetMergeBatchState("state-other", "lib", "a", BatchTesting, "")),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	for i, step := range []struct {
		state MergeBatchState
		base  string
	}{{BatchAssembling, tipM}, {BatchTesting, ""}, {BatchRed, ""}, {BatchAssembling, ""}, {BatchTesting, ""}, {BatchLanding, ""}, {BatchLanded, ""}} {
		if _, err := s.SetMergeBatchState(fmt.Sprintf("state-%d", i), "app", "a", step.state, step.base); err != nil {
			t.Fatalf("move to %s: %v", step.state, err)
		}
	}
	q, _ = s.MergeQueue("app")
	if q.Batch.State != BatchLanded || q.Batch.Base != tipM {
		t.Fatalf("batch = %+v", q.Batch)
	}
	if _, err := s.SetMergeBatchState("state-back", "app", "a", BatchAssembling, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("move from landed = %v", err)
	}
}

// TestMergeBatchLeaving: a member leaving the batch, or the queue, or closing drops it alone; the leader leaving the
// batch ends it but keeps its front; the leader releasing its front ends the batch, and the next claim, alone,
// records none.
func TestMergeBatchLeaving(t *testing.T) {
	s := queueStore(t, "a", "b", "c", "d", "e")
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		authorize(t, s, id, 1)
	}
	if _, err := s.ClaimMergeBatch("batch-a", "app", "a", 1, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveMergeBatch("leave-batch-b", "app", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveMergeQueue("leave-queue-c", "app", "c", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskState("cancel-d", "app", "d", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	q, _ := s.MergeQueue("app")
	if got := batchMembers(q.Batch); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("members = %v, want a alone", got)
	}
	if got := order(t, s); !slices.Equal(got, []string{"a", "b", "e"}) {
		t.Fatalf("order = %v, want b still queued", got)
	}
	if _, err := s.LeaveMergeBatch("leave-batch-e", "app", "e"); err != nil {
		t.Fatalf("a task in no batch leaving = %v", err)
	}
	if _, err := s.LeaveMergeBatch("leave-batch-a", "app", "a"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if q.Batch != nil || q.Front == nil || q.Front.TaskID != "a" {
		t.Fatalf("after the leader left the batch = %+v", q)
	}

	// A new batch is ended by its leader's release, and the next lone claim records none.
	s = queueStore(t, "a", "b", "c")
	for _, id := range []string{"a", "b", "c"} {
		authorize(t, s, id, 1)
	}
	if _, err := s.ClaimMergeBatch("batch-a", "app", "a", 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseMergeFront("release-b", "app", "b"); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.MergeQueue("app"); !slices.Equal(batchMembers(q.Batch), []string{"a", "b"}) {
		t.Fatalf("a member's release changed the batch: %+v", q.Batch)
	}
	if _, err := s.SetMergeHold("hold-a", "app", "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseMergeFront("release-a", "app", "a"); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil || q.Front != nil {
		t.Fatalf("after the leader's release = %+v", q)
	}
	if _, err := s.ClaimMergeFront("front-b", "app", "b"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if q.Batch != nil || q.Front.Stay != 2 {
		t.Fatalf("lone claim after a batch = %+v", q)
	}
}

func second(_ Receipt, err error) error { return err }

func batchEqual(a, b MergeBatch) bool {
	return a.Leader == b.Leader && a.Stay == b.Stay && a.Base == b.Base && a.State == b.State && slices.Equal(a.Members, b.Members)
}
