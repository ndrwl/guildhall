package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestBatchPlaceTrain: a batch landing, landed or red shows its train pull request's URL beside its state, and one
// assembling or testing, or without a train, its state alone; the queue's entries carry the train's URL.
func TestBatchPlaceTrain(t *testing.T) {
	const url = "https://forge.invalid/9"
	for state, want := range map[store.MergeBatchState]string{
		store.BatchAssembling: "in batch 2/3 (assembling)",
		store.BatchTesting:    "in batch 2/3 (testing)",
		store.BatchLanding:    "in batch 2/3 (landing, " + url + ")",
		store.BatchLanded:     "in batch 2/3 (landed, " + url + ")",
		store.BatchRed:        "in batch 2/3 (red, " + url + ")",
	} {
		if got := (batchPlace{Place: 2, Size: 3, Leader: "t1", State: state, Train: url}).String(); got != want {
			t.Errorf("%s: %q, want %q", state, got, want)
		}
	}
	if got := (batchPlace{Place: 1, Size: 2, State: store.BatchRed}).String(); got != "in batch 1/2 (red)" {
		t.Errorf("red without a train: %q", got)
	}

	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Leader")
	newTask(t, path, cfg, "app", "Member")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, id := range []string{"t1", "t2"} {
		if _, err := s.PutDocument("doc-"+id, store.DocumentRef{Repository: "app", TaskID: id, Name: "deliver-1-authorized", Path: "tasks/app/" + id + "/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.JoinMergeQueue("join-"+id, "app", id, "deliver-1", "document/deliver-1-authorized", ""); err != nil {
			t.Fatal(err)
		}
	}
	link1, link2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, step := range []func() error{
		func() error { _, err := s.ClaimMergeBatch("batch", "app", "t1", 1, 2); return err },
		func() error { _, err := s.SetMergeBatchLink("l1", "app", "t1", 1, link1); return err },
		func() error { _, err := s.SetMergeBatchLink("l2", "app", "t2", 1, link2); return err },
		func() error {
			_, err := s.SetMergeBatchState("testing", "app", "t1", store.BatchTesting, "")
			return err
		},
		func() error {
			_, err := s.SetMergeBatchTrain("train", "app", "t1", store.MergeTrain{Remote: "r", Number: 9, URL: url, Head: link2})
			return err
		},
		func() error { _, err := s.SetMergeBatchState("red", "app", "t1", store.BatchRed, ""); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	qs, err := mergeQueues(s, []string{"app"})
	if err != nil || len(qs) != 1 || len(qs[0].Entries) != 2 {
		t.Fatalf("queues = %+v, %v", qs, err)
	}
	if b := qs[0].Entries[1].Batch; b == nil || b.String() != "in batch 2/2 (red, "+url+")" {
		t.Fatalf("member's batch = %+v", b)
	}
}

// TestMergeQueueViews: a queued task shows its merge-queue entry in task list and director status, task hold-merge
// toggles its hold, recorded in the timeline, and a task not queued has no entry.
func TestMergeQueueViews(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Queued")
	newTask(t, path, cfg, "app", "Not queued")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("doc", store.DocumentRef{Repository: "app", TaskID: "t1", Name: "deliver-1-authorized", Path: "tasks/app/t1/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("join", "app", "t1", "deliver-1", "document/deliver-1-authorized", "https://forge.invalid/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("front", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runTask(t, "list", "--store", path, "--repo", "app", "--json")
	views := decodeJSON[[]struct {
		ID         string           `json:"id"`
		MergeQueue *mergeQueueEntry `json:"merge_queue"`
	}](t, out)
	if code != 0 || len(views) != 2 || views[1].MergeQueue != nil {
		t.Fatalf("list: exit %d, %s, stderr %s", code, out, stderr)
	}
	if e := views[0].MergeQueue; e == nil || !e.Front || e.Held || e.Position != 1 || e.PR != "https://forge.invalid/1" || e.WaitingOn != "ci" || e.Priority != store.PriorityNormal {
		t.Fatalf("list entry = %+v", e)
	}

	code, out, stderr = runTask(t, "hold-merge", "--store", path, "--repo", "app", "t1")
	if rc := decodeJSON[store.Receipt](t, out); code != 0 || rc.Record != store.MergeHoldRecord || rc.MutationID != "merge-hold:app:t1:1" {
		t.Fatalf("hold-merge: exit %d, %s, stderr %s", code, out, stderr)
	}
	code, out, _ = directorCmd(t, nil, "status", "--store", path, "--state", t.TempDir(), "--json")
	st := decodeJSON[struct {
		MergeQueues []mergeQueue `json:"merge_queues"`
	}](t, out)
	if code != 0 || len(st.MergeQueues) != 1 || st.MergeQueues[0].Repository != "app" || len(st.MergeQueues[0].Entries) != 1 {
		t.Fatalf("status: exit %d, %s", code, out)
	}
	// A held front keeps the front until its own next advance releases it.
	if e := st.MergeQueues[0].Entries[0]; e.Task != "t1" || !e.Held || !e.Front || e.WaitingOn != "ci" {
		t.Fatalf("status entry after hold = %+v", e)
	}

	if code, out, _ := runTask(t, "hold-merge", "--store", path, "--repo", "app", "t1"); code != 0 || !strings.Contains(out, `"merge-hold:app:t1:2"`) {
		t.Fatalf("unhold: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t1")
	tl := decodeJSON[struct {
		Holds []struct {
			Held  bool    `json:"held"`
			SetAt *string `json:"set_at"`
		} `json:"merge_holds"`
	}](t, out)
	if code != 0 || len(tl.Holds) != 2 || !tl.Holds[0].Held || tl.Holds[1].Held || tl.Holds[1].SetAt == nil {
		t.Fatalf("timeline: exit %d, %s", code, out)
	}
	for _, args := range [][]string{
		{"hold-merge", "--store", path, "--repo", "app"},
		{"hold-merge", "--store", path, "t1"},
	} {
		if code, _, stderr := runTask(t, args...); code != 2 {
			t.Errorf("%v: exit %d, stderr %s; want usage error", args, code, stderr)
		}
	}
	if code, _, stderr := runTask(t, "hold-merge", "--store", path, "--repo", "app", "t9"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown task: exit %d, stderr %s", code, stderr)
	}
}
