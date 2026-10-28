package interp

import (
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// resolveMinor leaves a minor_conflicts rebase unresolved: its head is empty until a rebaser resolves it.
func (r *run) resolveMinor(l *rebaseLink) error { return nil }

// resolvePending handles the front's unresolved minor_conflicts rebase l: with no rebaser it escalates to
// major_conflicts, a build send-back.
func (r *run) resolvePending(s workflow.Step, k int, repo config.Repository, id string, l rebaseLink) (Next, bool, error) {
	files := []string{}
	for _, c := range l.doc.Conflicts {
		files = append(files, c.Path)
	}
	next, err := r.escalate(s, k, escalationDoc{Authorization: id, From: l.doc.From, Onto: l.doc.Onto, Kind: config.RebaseMajorConflicts, Files: files,
		Reason: l.doc.Reason + "; minor conflicts need a rebaser, which this copy does not run", Carry: true})
	return next, false, err
}
