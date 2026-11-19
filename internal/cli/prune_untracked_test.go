package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
)

// pruneUntracked turns the fixture repository's prune_untracked on.
func (f *pruneFixture) pruneUntracked() {
	f.t.Helper()
	for i := range f.cfg.Repositories {
		if f.cfg.Repositories[i].ID == "app" {
			f.cfg.Repositories[i].PruneUntracked = true
			return
		}
	}
	f.t.Fatal("no app repository in the fixture config")
}

// cancel cancels repo/id at f.at.
func (f *pruneFixture) cancel(repo, id string) {
	f.t.Helper()
	f.mutate(func(s *store.Store) error {
		_, err := s.CancelTask(f.id(), repo, id, "not needed")
		return err
	})
}

// onlyResult is the one result of a real prune of app/id.
func (f *pruneFixture) onlyResult() pruneResult {
	f.t.Helper()
	got := f.run(false)
	if len(got) != 1 || got[0].Skip != "" {
		f.t.Fatalf("prune = %+v, want one pruned task", got)
	}
	return got[0]
}

// TestPruneUntrackedDiscardsADoneTasksLogs: with prune_untracked on, a done task's merged worktree that holds only
// untracked files is removed with them, and the prune result, record and report name each one.
func TestPruneUntrackedDiscardsADoneTasksLogs(t *testing.T) {
	f := newPruneFixture(t)
	f.pruneUntracked()
	f.task("app", "t1")
	f.attempt("app", "t1", "build", store.AttemptSucceeded, nil)
	f.worktree("t1")
	w := f.workspace("t1")
	writeTree(t, filepath.Join(w.Dir, "ci", "job.log"), "failed\n")
	writeTree(t, filepath.Join(w.Dir, "notes.txt"), "mine\n")
	f.close("app", "t1")

	r := f.onlyResult()
	want := []string{"work/ci/job.log", "work/notes.txt"}
	if r.Worktree != store.WorktreeRemoved || !slices.Equal(r.Discarded, want) || !r.complete() {
		t.Fatalf("prune = %+v, want the worktree removed with %q discarded", r, want)
	}
	if _, err := os.Lstat(w.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree: %v, want it removed", err)
	}
	rec, err := f.record("t1")
	if err != nil || rec.Worktree != store.WorktreeRemoved || rec.Discarded != "work/ci/job.log, work/notes.txt" {
		t.Fatalf("record = %+v, %v; want the discarded files named", rec, err)
	}
	var out strings.Builder
	if err := writePruneReport(&out, []pruneResult{r}, false); err != nil ||
		!strings.Contains(out.String(), "pruned app/t1: ") ||
		!strings.Contains(out.String(), "(untracked files discarded under prune_untracked: work/ci/job.log, work/notes.txt)") {
		t.Errorf("report %q, want the discarded files named", out.String())
	}
}

// TestPruneUntrackedOffKeepsAsBefore: with prune_untracked off, the default, an untracked file keeps a done task's
// worktree with the same reason as before the setting existed, and nothing is discarded.
func TestPruneUntrackedOffKeepsAsBefore(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t1")
	f.worktree("t1")
	writeTree(t, filepath.Join(f.workspace("t1").Dir, "ci.log"), "failed\n")
	f.close("app", "t1")

	r := f.onlyResult()
	if r.Worktree != store.WorktreeKept || r.Kept != "untracked ci.log" || len(r.Discarded) > 0 {
		t.Fatalf("prune = %+v, want the worktree kept for untracked ci.log", r)
	}
	var out strings.Builder
	if err := writePruneReport(&out, []pruneResult{r}, false); err != nil || !strings.HasPrefix(out.String(), "skipped app/t1: worktree kept: untracked ci.log; ") ||
		strings.Contains(out.String(), "discarded") {
		t.Errorf("report %q", out.String())
	}
	if rec, err := f.record("t1"); err != nil || rec.Discarded != "" {
		t.Errorf("record = %+v, %v; want nothing discarded", rec, err)
	}
}

// TestPruneUntrackedKeepsCancelledWorktrees: with prune_untracked on, a cancelled task's worktree keeps its
// untracked files, whether it never merged or merged and was cancelled after.
func TestPruneUntrackedKeepsCancelledWorktrees(t *testing.T) {
	t.Run("cancelled unmerged", func(t *testing.T) {
		f := newPruneFixture(t)
		f.pruneUntracked()
		f.task("app", "t1")
		w := f.workspace("t1")
		if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := (git.Program{}).Ensure(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		writeTree(t, filepath.Join(w.Dir, "ci.log"), "failed\n")
		f.cancel("app", "t1")

		r := f.onlyResult()
		if r.Worktree != store.WorktreeKept || !strings.Contains(r.Kept, "cancelled unmerged") || len(r.Discarded) > 0 {
			t.Fatalf("prune = %+v, want the worktree kept as cancelled unmerged", r)
		}
		if _, err := os.Lstat(filepath.Join(w.Dir, "ci.log")); err != nil {
			t.Errorf("untracked file: %v, want it kept", err)
		}
	})
	t.Run("merged then cancelled", func(t *testing.T) {
		f := newPruneFixture(t)
		f.pruneUntracked()
		f.task("app", "t1")
		f.worktree("t1")
		w := f.workspace("t1")
		writeTree(t, filepath.Join(w.Dir, "ci.log"), "failed\n")
		f.cancel("app", "t1")

		r := f.onlyResult()
		if r.Worktree != store.WorktreeKept || r.Kept != "untracked ci.log" || len(r.Discarded) > 0 {
			t.Fatalf("prune = %+v, want the worktree kept for untracked ci.log", r)
		}
		if _, err := os.Lstat(filepath.Join(w.Dir, "ci.log")); err != nil {
			t.Errorf("untracked file: %v, want it kept", err)
		}
	})
}

// TestPruneUntrackedDecidesEachSlice: with prune_untracked on, each slice's worktree is decided on its own: slice
// 1's, with only an untracked file, is removed with it, and slice 2's, with a changed tracked file beside its
// untracked one, is kept naming both.
func TestPruneUntrackedDecidesEachSlice(t *testing.T) {
	f := newPruneFixture(t)
	f.pruneUntracked()
	f.task("app", "t1")
	f.worktree("t1")
	first := f.workspace("t1")
	writeTree(t, filepath.Join(first.Dir, "first.log"), "log\n")
	w := git.Workspace{Repository: f.repository, Remote: f.remote, Branch: "guildhall/t1.s2", Dir: filepath.Join(f.state, "app", "t1", "work.s2")}
	if err := (git.Program{}).Ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(w.Dir, "second.txt"), "second slice\n")
	f.git(w.Dir, "add", "second.txt")
	f.git(w.Dir, "commit", "-q", "-m", "second slice")
	second := f.git(w.Dir, "rev-parse", "HEAD")
	f.document("t1", "deliver--s2-1-merge", "deliver--s2-1/merge.json", `{"schema":"guildhall.pull-request/1","remote":"origin","number":2,"head":"`+second+`","state":"merged"}`)
	writeTree(t, filepath.Join(w.Dir, "second.txt"), "edited after the merge\n")
	writeTree(t, filepath.Join(w.Dir, "second.log"), "log\n")
	f.close("app", "t1")

	r := f.onlyResult()
	if r.Worktree != store.WorktreeKept || r.Kept != "slice 2: changed second.txt; untracked second.log" {
		t.Fatalf("prune = %+v, want slice 2 kept naming both its files", r)
	}
	if !slices.Equal(r.Discarded, []string{"work/first.log"}) {
		t.Errorf("Discarded = %q, want slice 1's log alone", r.Discarded)
	}
	if _, err := os.Lstat(first.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("slice 1's worktree: %v, want it removed", err)
	}
	if _, err := os.Lstat(filepath.Join(w.Dir, "second.log")); err != nil {
		t.Errorf("slice 2's untracked file: %v, want it kept", err)
	}
	if rec, err := f.record("t1"); err != nil || rec.Worktree != store.WorktreeKept || rec.Discarded != "work/first.log" {
		t.Errorf("record = %+v, %v", rec, err)
	}
}
