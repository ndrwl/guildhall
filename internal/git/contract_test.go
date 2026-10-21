package git_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
)

// Classify is the operation under the contract: git.Program.Classify, or a double's.
type Classify func(ctx context.Context, repo string, last, next git.Range) (git.Classification, error)

// Case is a history the contract suite asks a harness to arrange.
type Case string

// The histories every classifier under test must classify.
const (
	// RebaseOnly: the reviewed commits replayed unchanged onto a base that moved in other files.
	RebaseOnly Case = "rebase-only"
	// RebaseAndEdit: a rebase whose head also edits a file the task changes.
	RebaseAndEdit Case = "rebase-and-edit"
	// BaseTouchesTaskFile: the base moved in a file the task changes, and the task was rebased onto it.
	BaseTouchesTaskFile Case = "base-touches-task-file"
	// SameBase: a new head on the reviewed base.
	SameBase Case = "same-base"
	// UnknownCommit: the new head is not in the repository.
	UnknownCommit        Case = "unknown-commit"
	SubmoduleRebase      Case = "submodule-rebase"
	BaseTouchesSubmodule Case = "base-touches-submodule"
)

// Harness is one classifier under the contract suite.
type Harness struct {
	Classify Classify
	// Arrange creates the history c and returns its repository, reviewed range and new range.
	Arrange func(t *testing.T, c Case) (repo string, last, next git.Range)
}

// RunClassifierContract runs the classification contract (git.Program.Classify) against harnesses from newHarness.
func RunClassifierContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	for _, c := range []Case{RebaseOnly, RebaseAndEdit, BaseTouchesTaskFile, SameBase, UnknownCommit, SubmoduleRebase, BaseTouchesSubmodule} {
		t.Run(string(c), func(t *testing.T) {
			h := newHarness(t)
			repo, last, next := h.Arrange(t, c)
			got, err := h.Classify(context.Background(), repo, last, next)
			if err != nil {
				t.Fatalf("Classify = %v", err)
			}
			t.Logf("%s: %s", got.Scope, got.Reason)
			want := git.ScopeFull
			if c == RebaseOnly || c == SubmoduleRebase {
				want = git.ScopeRebase
			}
			switch {
			case got.Scope != want:
				t.Fatalf("scope = %q (%s), want %q", got.Scope, got.Reason, want)
			case want == git.ScopeRebase && (len(got.Diff) == 0 || got.Reason != ""):
				t.Errorf("rebase = %+v, want a resolution diff and no reason", got)
			case want == git.ScopeFull && (got.Diff != nil || got.Reason == ""):
				t.Errorf("full = %+v, want a reason and no diff", got)
			}
			if c == SubmoduleRebase && !bytes.Contains(got.Diff, []byte("Subproject commit")) {
				t.Errorf("resolution diff omits the submodule pointer: %s", got.Diff)
			}
			again, err := h.Classify(context.Background(), repo, last, next)
			if err != nil || again.Scope != got.Scope || string(again.Diff) != string(got.Diff) {
				t.Errorf("repeated Classify = %+v, %v; want %+v", again, err, got)
			}
		})
	}
	t.Run("ended context", func(t *testing.T) {
		h := newHarness(t)
		repo, last, next := h.Arrange(t, RebaseOnly)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := h.Classify(ctx, repo, last, next); err == nil {
			t.Fatal("Classify with an ended context succeeded")
		}
	})
}

// Script is a scripted classifier: each (repository, reviewed range, new range) answers what was added for it,
// and anything else is a full review. It records every call. It is not safe for concurrent use.
type Script struct {
	answers map[[3]any]git.Classification
	Calls   [][2]git.Range
}

// Add scripts the classification of next against last in repo.
func (s *Script) Add(repo string, last, next git.Range, c git.Classification) {
	if s.answers == nil {
		s.answers = map[[3]any]git.Classification{}
	}
	s.answers[[3]any{repo, last, next}] = c
}

// Classify implements the contract from the script.
func (s *Script) Classify(ctx context.Context, repo string, last, next git.Range) (git.Classification, error) {
	if err := ctx.Err(); err != nil {
		return git.Classification{}, err
	}
	s.Calls = append(s.Calls, [2]git.Range{last, next})
	if c, ok := s.answers[[3]any{repo, last, next}]; ok {
		return c, nil
	}
	return git.Classification{Scope: git.ScopeFull, Reason: "not scripted"}, nil
}
