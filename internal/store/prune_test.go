package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestPruneTaskRoundTrips: a prune record of a done task is read back, from TaskPrune and TaskHistory, with its
// receipt naming the prune record; a later run needs the count it read, and a complete prune is final.
func TestPruneTaskRoundTrips(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.SetTaskState("m-2", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskPrune("app", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TaskPrune before a prune: %v, want ErrNotFound", err)
	}
	closed, err := s.Receipt("m-2")
	if err != nil {
		t.Fatal(err)
	}
	if times, err := s.CloseTimes(); err != nil || len(times) != 1 || !times[TaskRef{Repository: "app", ID: "t-1"}].Equal(closed.CommittedAt) {
		t.Fatalf("CloseTimes = %v, %v; want t-1 at %v", times, err, closed.CommittedAt)
	}
	partial := TaskPrune{Repository: "app", TaskID: "t-1", BytesFreed: 2048, Worktree: WorktreeKept, Kept: "untracked notes.md"}
	r, err := s.PruneTask("prune:app:t-1", partial)
	if err != nil {
		t.Fatal(err)
	}
	if r.Record != PruneRecord || r.Repository != "app" || r.TaskID != "t-1" {
		t.Fatalf("receipt = %+v", r)
	}
	if again, err := s.PruneTask("prune:app:t-1", partial); err != nil || again.Sequence != r.Sequence {
		t.Fatalf("replay = %+v, %v; want the first receipt", again, err)
	}
	got, err := s.TaskPrune("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs != 1 || got.BytesFreed != 2048 || got.Worktree != WorktreeKept || got.Kept != "untracked notes.md" || got.Complete || !got.PrunedAt.Equal(r.CommittedAt) {
		t.Fatalf("record = %+v; receipt committed at %v", got, r.CommittedAt)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || h.Pruned == nil || *h.Pruned != got {
		t.Fatalf("history pruned = %+v, %v; want %+v", h.Pruned, err, got)
	}
	if last := h.Receipts[len(h.Receipts)-1]; last.Record != PruneRecord {
		t.Fatalf("last history receipt = %+v, want the prune", last)
	}

	if _, err := s.PruneTask("prune:app:t-1:2", TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved, Complete: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("a run with a stale count: %v, want ErrExists", err)
	}
	complete := TaskPrune{Repository: "app", TaskID: "t-1", BytesFreed: 4096, Worktree: WorktreeRemoved, Complete: true, Runs: 1}
	if _, err := s.PruneTask("prune:app:t-1:2", complete); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TaskPrune("app", "t-1"); err != nil || got.Runs != 2 || !got.Complete || got.Kept != "" {
		t.Fatalf("record = %+v, %v", got, err)
	}
	if _, err := s.PruneTask("prune:app:t-1:3", complete); !errors.Is(err, ErrExists) {
		t.Fatalf("a prune after a complete one with a stale count: %v, want ErrExists", err)
	}
	complete.Runs, complete.BytesFreed = 2, 512
	if _, err := s.PruneTask("prune:app:t-1:3", complete); err != nil {
		t.Fatalf("a prune after a complete one: %v", err)
	}
	if got, err := s.TaskPrune("app", "t-1"); err != nil || got.Runs != 3 || !got.Complete || got.BytesFreed != 512 {
		t.Fatalf("record after a catch-up = %+v, %v", got, err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || len(rep.Repositories) != 1 || rep.Repositories[0].Pruned != 1 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

// TestPruneTaskRefusals: only a done or cancelled task is pruned, and a record must say what happened to the
// worktree.
func TestPruneTaskRefusals(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, tc := range map[string]struct {
		p    TaskPrune
		want error
	}{
		"open task":          {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved}, ErrInvalid},
		"unknown task":       {TaskPrune{Repository: "app", TaskID: "t-9", Worktree: WorktreeRemoved}, ErrNotFound},
		"unknown worktree":   {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: "gone"}, ErrInvalid},
		"kept without why":   {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeKept}, ErrInvalid},
		"removed with a why": {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved, Kept: "x"}, ErrInvalid},
		"negative runs":      {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved, Runs: -1}, ErrInvalid},
		"negative bytes":     {TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved, BytesFreed: -1}, ErrInvalid},
	} {
		if _, err := s.PruneTask("prune:"+name[:4], tc.p); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if rep, err := s.Doctor(); err != nil || rep.Repositories[0].Pruned != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

// TestPruneCancelledTaskRecord: a cancelled task is pruned like a done one, and its close time is its cancel-task
// receipt's commit time.
func TestPruneCancelledTaskRecord(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	rc, err := s.CancelTask("m-2", "app", "t-1", "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.CloseTimes()
	if err != nil || !closed[TaskRef{Repository: "app", ID: "t-1"}].Equal(rc.CommittedAt) {
		t.Fatalf("close times = %v, %v; want the cancel's %v", closed, err, rc.CommittedAt)
	}
	if _, err := s.PruneTask("prune:app:t-1", TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeKept, Kept: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TaskPrune("app", "t-1"); err != nil || got.Runs != 1 || got.Complete {
		t.Fatalf("record = %+v, %v", got, err)
	}
}

// TestPruneTaskOnSchemaV1Fixture: a store written before prune records opens unchanged, reports no prunes and
// accepts one.
func TestPruneTaskOnSchemaV1Fixture(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	if _, err := s.TaskPrune("app", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TaskPrune on the fixture: %v", err)
	}
	if h, err := s.TaskHistory("app", "t-1"); err != nil || h.Pruned != nil {
		t.Fatalf("history = %+v, %v", h.Pruned, err)
	}
	if _, err := s.SetTaskState("fixture-done", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneTask("prune:app:t-1", TaskPrune{Repository: "app", TaskID: "t-1", Worktree: WorktreeRemoved, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 || rep.Repositories[0].Pruned != 1 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}
