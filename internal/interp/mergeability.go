package interp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
)

// MergeabilityReader observes a published PR without modifying its branch or
// worktree. Production GitHub and the forge test double implement this seam.
// Forges without this capability retain check/merge-refusal behaviour.
type MergeabilityReader interface {
	Mergeability(context.Context, forge.Merge) (forge.Mergeability, error)
}

// mergeBlocked records one immutable observation per head and state before
// notifying the Director. A crash between record and send replays the same text;
// a moving base cannot cause a mutation conflict or duplicate the notification.
func (r *run) mergeBlocked(pr *pullRequestDoc, head string) (bool, error) {
	reader, ok := r.e.Forge.(MergeabilityReader)
	if !ok || pr == nil {
		return false, nil
	}
	repo, err := r.repository()
	if err != nil {
		return false, err
	}
	m, err := reader.Mergeability(r.ctx, forge.Merge{Repository: repo.Path, Remote: pr.Remote, Number: pr.Number, Head: head})
	if err != nil {
		return false, err
	}
	if m.Head != head {
		return false, fmt.Errorf("%w: mergeability head %s is not %s", ErrEvent, m.Head, head)
	}
	if m.State != "conflicting" && m.State != "behind" {
		return false, nil
	}
	if !digest.ValidHead(m.Base) || m.Behind < 0 {
		return false, fmt.Errorf("%w: invalid mergeability evidence", ErrEvent)
	}
	m.URL = pr.URL
	id := "pr-blocked-" + head + "-" + m.State
	if d := r.document(id); d == nil {
		p := path.Join("tasks", r.task.Repository, r.task.ID, id+".json")
		data, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(p)))
		if errors.Is(err, os.ErrNotExist) {
			data, err = json.Marshal(m)
		}
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return false, err
		}
		if err := r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: id, Path: p, SHA256: digest.Of(data).Hex()}, data); err != nil {
			return false, err
		}
	} else {
		data, err := r.read(*d)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return false, err
		}
	}
	return true, r.notifyMergeBlocked(id, m)
}

// replayMergeBlocked completes a notification lost after the observation was
// committed, even if the forge has since become clear or work moved upstream.
func (r *run) replayMergeBlocked() error {
	for _, d := range r.documents {
		if !strings.HasPrefix(d.Name, "pr-blocked-") {
			continue
		}
		data, err := r.read(d)
		if err != nil {
			return err
		}
		var m forge.Mergeability
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if err := r.notifyMergeBlocked(d.Name, m); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) notifyMergeBlocked(id string, m forge.Mergeability) error {
	text := fmt.Sprintf("Pull request: %s\nHead: %s\nBase: %s\nState: %s\nCommits behind: %d\nConflicting files: %s\nThe task waits for the Director; Guildhall does not rebase automatically. Options include guildhall step send-back --repo %s --task %s --step <earlier-step> --attempt <succeeded-attempt> --feedback <instructions> (with the copy/store/config/state flags), leaving the task on hold, or abandoning this work by leaving it unadvanced (there is no task-cancellation command). Existing merge decisions also accept hold; running attempts can be stopped with guildhall artisan stop. Advancing with guildhall task start %s --repo %s rechecks the forge; choose any recovery appropriate to this task.", m.URL, m.Head, m.Base, m.State, m.Behind, strings.Join(m.Files, ", "), r.task.Repository, r.task.ID, r.task.ID, r.task.Repository)
	return r.notify(id, store.MessagePRBlocked, text)
}
