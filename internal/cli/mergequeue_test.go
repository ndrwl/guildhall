package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestMergeQueueViews: a queued task shows its merge-queue entry in task list and director status, task hold-merge
// toggles its hold, recorded in the timeline, and a task not queued has no entry.
func TestMergeQueueViews(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Queued", "t-1")
	newTask(t, path, cfg, "app", "Not queued", "t-2")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("doc", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "deliver-1-authorized", Path: "tasks/app/t-1/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("join", "app", "t-1", "deliver-1", "document/deliver-1-authorized", "https://forge.invalid/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("front", "app", "t-1"); err != nil {
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

	code, out, stderr = runTask(t, "hold-merge", "--store", path, "--repo", "app", "t-1")
	if rc := decodeJSON[store.Receipt](t, out); code != 0 || rc.Record != store.MergeHoldRecord || rc.MutationID != "merge-hold:app:t-1:1" {
		t.Fatalf("hold-merge: exit %d, %s, stderr %s", code, out, stderr)
	}
	code, out, _ = directorCmd(t, nil, "status", "--store", path, "--state", t.TempDir(), "--json")
	st := decodeJSON[struct {
		MergeQueues []mergeQueue `json:"merge_queues"`
	}](t, out)
	if code != 0 || len(st.MergeQueues) != 1 || st.MergeQueues[0].Repository != "app" || len(st.MergeQueues[0].Entries) != 1 {
		t.Fatalf("status: exit %d, %s", code, out)
	}
	if e := st.MergeQueues[0].Entries[0]; e.Task != "t-1" || !e.Held || e.Front || e.WaitingOn != "queue" {
		t.Fatalf("status entry after hold = %+v", e)
	}

	if code, out, _ := runTask(t, "hold-merge", "--store", path, "--repo", "app", "t-1"); code != 0 || !strings.Contains(out, `"merge-hold:app:t-1:2"`) {
		t.Fatalf("unhold: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-1")
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
		{"hold-merge", "--store", path, "t-1"},
	} {
		if code, _, stderr := runTask(t, args...); code != 2 {
			t.Errorf("%v: exit %d, stderr %s; want usage error", args, code, stderr)
		}
	}
	if code, _, stderr := runTask(t, "hold-merge", "--store", path, "--repo", "app", "t-9"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown task: exit %d, stderr %s", code, stderr)
	}
}
