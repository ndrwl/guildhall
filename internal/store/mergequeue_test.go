package store

import (
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// queueStore is a store whose clock moves a second per commit, so ready times order receipts.
func queueStore(t *testing.T, tasks ...string) *Store {
	t.Helper()
	var mu sync.Mutex
	now := testTime
	opts := testOptions()
	opts.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
	s, err := Open(filepath.Join(t.TempDir(), "guildhall.store"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, id := range tasks {
		mustCreate(t, s, "create-"+id, sampleTask("app", id))
	}
	return s
}

// authorize records task id's deliver-<n>-authorized and queues it under that authorization.
func authorize(t *testing.T, s *Store, id string, n int) {
	t.Helper()
	name := "deliver-" + string(rune('0'+n)) + "-authorized"
	if _, err := s.PutDocument("doc-"+id+name, DocumentRef{Repository: "app", TaskID: id, Name: name, Path: "tasks/app/" + id + "/" + name + ".json", SHA256: sampleDigest('d')[7:]}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("join-"+id+name, "app", id, name[:len(name)-len("-authorized")], "document/"+name, "https://forge.invalid/"+id); err != nil {
		t.Fatal(err)
	}
}

func order(t *testing.T, s *Store) []string {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range q.Entries {
		out = append(out, e.TaskID)
	}
	return out
}

// TestMergeQueueOrder: entries are in priority, then ready time, then task id order; a priority change reorders
// only the entries behind a standing front.
func TestMergeQueueOrder(t *testing.T) {
	s := queueStore(t, "a", "b", "c")
	authorize(t, s, "c", 1)
	authorize(t, s, "b", 1)
	authorize(t, s, "a", 1)
	if got := order(t, s); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatalf("order by ready time = %v", got)
	}
	if _, err := s.ClaimMergeFront("front-c", "app", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskPriority("p-a", "app", "a", PriorityUrgent); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("order after a priority change = %v, want the front kept and a ahead of b", got)
	}
	if _, err := s.ClaimMergeFront("front-a", "app", "a"); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("claim behind a standing front = %v", err)
	}
}

// TestMergeQueueClaimRace: of two tasks claiming the front at once, only the first in queue order holds it.
func TestMergeQueueClaimRace(t *testing.T) {
	s := queueStore(t, "a", "b")
	authorize(t, s, "a", 1)
	authorize(t, s, "b", 1)
	var wg sync.WaitGroup
	errs := map[string]error{}
	var mu sync.Mutex
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimMergeFront("front-"+id, "app", id)
			mu.Lock()
			errs[id] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	if errs["a"] != nil || !errors.Is(errs["b"], ErrMergeQueued) {
		t.Fatalf("claims = %v", errs)
	}
	q, err := s.MergeQueue("app")
	if err != nil || q.Front == nil || q.Front.TaskID != "a" || q.Front.Stay != 1 {
		t.Fatalf("front = %+v, %v", q.Front, err)
	}
}

// TestMergeQueueRejoinKeepsReadyTime: a task taken out after a major send-back keeps its first ready time, and,
// authorized again, rejoins ahead of a same-priority task that became ready later; a merge removes its entry.
func TestMergeQueueRejoinKeepsReadyTime(t *testing.T) {
	s := queueStore(t, "z", "b")
	authorize(t, s, "z", 1)
	if _, err := s.ClaimMergeFront("front-z", "app", "z"); err != nil {
		t.Fatal(err)
	}
	first, _ := s.MergeQueue("app")
	ready := first.Entries[0].ReadyAt
	if _, err := s.LeaveMergeQueue("leave-z", "app", "z", true); err != nil {
		t.Fatal(err)
	}
	authorize(t, s, "b", 1)
	q, _ := s.MergeQueue("app")
	if q.Front != nil || len(q.Out) != 1 || q.Out[0].TaskID != "z" {
		t.Fatalf("after leaving = %+v", q)
	}
	authorize(t, s, "z", 2)
	q, _ = s.MergeQueue("app")
	if got := order(t, s); !slices.Equal(got, []string{"z", "b"}) || !q.Entries[0].ReadyAt.Equal(ready) || q.Entries[0].Authorization != "deliver-2" {
		t.Fatalf("rejoined = %+v", q.Entries)
	}
	if _, err := s.ClaimMergeFront("front-z-2", "app", "z"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if q.Front.Stay != 2 {
		t.Fatalf("stay = %d, want 2", q.Front.Stay)
	}
	if _, err := s.LeaveMergeQueue("merged-z", "app", "z", false); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("after merge = %v", got)
	}
	if q, _ := s.MergeQueue("app"); len(q.Out) != 0 || q.Front != nil {
		t.Fatalf("after merge = %+v", q)
	}
}

// TestMergeQueueHoldAndCancel: a held front stands until its task releases it at its next advance, so no other task
// can claim it before then; after the release the next task not held may claim it, and the held task keeps its place
// by ready time. A cancelled front's task leaves the queue.
func TestMergeQueueHoldAndCancel(t *testing.T) {
	s := queueStore(t, "a", "b", "c")
	authorize(t, s, "a", 1)
	authorize(t, s, "b", 1)
	authorize(t, s, "c", 1)
	if _, err := s.ClaimMergeFront("front-a", "app", "a"); err != nil {
		t.Fatal(err)
	}
	rc, err := s.SetMergeHold("hold-a", "app", "a", true)
	if err != nil || rc.Record != MergeHoldRecord {
		t.Fatalf("SetMergeHold = %+v, %v", rc, err)
	}
	q, _ := s.MergeQueue("app")
	if q.Front == nil || q.Front.TaskID != "a" || !q.Entries[0].Held || !q.Entries[0].Front {
		t.Fatalf("held front before its next advance = %+v", q)
	}
	if _, err := s.ClaimMergeFront("front-b-early", "app", "b"); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("claim by b while the held front stands = %v", err)
	}
	// The held task's own release, at its next advance, frees the front.
	if _, err := s.ReleaseMergeFront("release-a", "app", "a"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if q.Front != nil || !q.Entries[0].Held || q.Entries[0].TaskID != "a" {
		t.Fatalf("held = %+v", q)
	}
	if _, err := s.ClaimMergeFront("front-a-2", "app", "a"); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("held claim = %v", err)
	}
	if _, err := s.ClaimMergeFront("front-c", "app", "c"); !errors.Is(err, ErrMergeQueued) {
		t.Fatalf("claim by c, behind b = %v", err)
	}
	if _, err := s.ClaimMergeFront("front-b", "app", "b"); err != nil {
		t.Fatalf("claim by b, the first task not held = %v", err)
	}
	if _, err := s.SetMergeHold("unhold-a", "app", "a", false); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatalf("after unhold = %v, want b keeping the front", got)
	}
	h, err := s.MergeHold("app", "a")
	if err != nil || !slices.Equal(h.History, []bool{true, false}) {
		t.Fatalf("hold history = %+v, %v", h, err)
	}
	if _, err := s.SetTaskState("cancel-b", "app", "b", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	q, _ = s.MergeQueue("app")
	if q.Front != nil || !slices.Equal(order(t, s), []string{"a", "c"}) {
		t.Fatalf("after cancel = %+v", q)
	}
	if _, err := s.ClaimMergeFront("front-a-3", "app", "a"); err != nil {
		t.Fatalf("claim after the cancelled front = %v", err)
	}
	if _, err := s.SetMergeHold("hold-b", "app", "b", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hold of a closed task = %v", err)
	}
}
