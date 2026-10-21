package interp

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// ResolutionPort carries a code review's resolution diff when its round reviews only a rebase.
const ResolutionPort = "resolution"

// Rebases classifies a code review's new range against the range last reviewed; git.Program implements it
// (its contract is git.Program.Classify), and gittest.Script is its double.
type Rebases interface {
	Classify(ctx context.Context, repo string, last, next git.Range) (git.Classification, error)
}

var _ Rebases = git.Program{}

// rebase sets the range and scope of attempt n of code review s, on head, in b and returns its resolution input.
// The range is the repository's current base and head; without a base the round is full and records no range.
// A pending full-review request makes the round full and is consumed by it, before anything is classified.
// The round is a rebase only when Rebases classifies the range against the one the step's latest outcome
// reviewed as one; the resolution diff is then recorded first, as document <step>-<n>-resolution.
func (r *run) rebase(s workflow.Step, n int, head string, b *binding) ([]render.Input, error) {
	b.Scope = git.ScopeFull
	requested, err := r.requestedFull(s.ID, n)
	if err != nil || r.e.Base == nil {
		return nil, err
	}
	base, err := r.e.Base(r.task.Repository)
	if err != nil || !digest.ValidHead(base) {
		return nil, nil
	}
	b.Range = &git.Range{Base: base, Head: head}
	if requested {
		return nil, nil
	}
	last, err := r.reviewedRange(s.ID, b.Revisions)
	i := slices.IndexFunc(r.e.Config.Repositories, func(c config.Repository) bool { return c.ID == r.task.Repository })
	if err != nil || last == nil || last.Head == head || r.e.Rebases == nil || i < 0 {
		return nil, err
	}
	c, err := r.e.Rebases.Classify(r.ctx, r.e.Config.Repositories[i].Path, *last, *b.Range)
	if err != nil || c.Scope != git.ScopeRebase {
		return nil, err
	}
	name := documentName(place{step: s.ID, round: n, part: ResolutionPort})
	d := r.document(name)
	if d == nil {
		ref := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
			Path: path.Join("tasks", r.task.Repository, r.task.ID, fmt.Sprintf("%s-%d.resolution.diff", s.ID, n)), SHA256: digest.Of(c.Diff).Hex()}
		if err := r.put(ref, c.Diff); err != nil {
			return nil, err
		}
		d = r.document(name)
	} else if d.SHA256 != digest.Of(c.Diff).Hex() {
		// A replay whose base moved since the diff was recorded: uncertain, so full.
		return nil, nil
	}
	b.Scope = git.ScopeRebase
	return []render.Input{{Port: ResolutionPort, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))}}, nil
}

// requestedFull reports whether a full-review request makes attempt n of step full: it consumes the pending
// request, or found it consumed by this attempt when the advance is a replay.
func (r *run) requestedFull(step string, n int) (bool, error) {
	by := r.identity(step, n).AttemptID
	f := r.fullReview(by)
	if f == nil {
		return false, nil
	}
	if f.ConsumedBy == "" {
		if _, err := r.e.Records.ConsumeFullReview(r.mutation("full-review", strconv.Itoa(f.Number)), r.task.Repository, r.task.ID, f.Number, by); err != nil {
			return false, err
		}
		f.ConsumedBy = by
	}
	return true, nil
}
