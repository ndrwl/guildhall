package interp

import (
	"path"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// recordWorkHead derives a missing code-head output from committed work. Once
// persisted, that document wins over subsequent workspace changes during replay.
// The synthetic head file belongs to the document directory, never the worktree.
func (r *run) recordWorkHead(s workflow.Step, n int, port string) error {
	name := documentName(place{step: s.ID, round: n, part: port})
	if r.document(name) != nil {
		return nil
	}
	head, err := (git.Program{}).FinalHead(r.ctx, r.workPath())
	if err != nil {
		return err
	}
	data := []byte(head + "\n")
	return r.put(store.DocumentRef{
		Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path:   path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), "head"),
		SHA256: digest.Of(data).Hex(),
	}, data)
}
