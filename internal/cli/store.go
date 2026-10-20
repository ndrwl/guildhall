package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
)

// storeDoctor opens the store at path read-only, checks it and reports what it found.
// It exits 0 only when the store opened and showed no problems.
func (a *App) storeDoctor(path string) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: store doctor: %v\n", err)
		return 1
	}
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("no store at %s", path))
	} else if err != nil {
		return fail(err)
	}
	rep, err := s.Doctor()
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "store: %s\nschema_version: %d (supported: %d)\n", path, rep.SchemaVersion, store.SchemaVersion)
	if len(rep.Problems) == 0 {
		b.WriteString("integrity: ok\n")
	} else {
		fmt.Fprintf(&b, "integrity: %d problems\n", len(rep.Problems))
	}
	fmt.Fprintf(&b, "receipts: %d\nworkflow approvals: %d\n", rep.Receipts, rep.WorkflowApprovals)
	for _, r := range rep.Repositories {
		fmt.Fprintf(&b, "repository %s: tasks %d, attempts %d, documents %d, pane bindings %d, decisions %d, messages %d (%d unread)\n",
			r.Repository, r.Tasks, r.Attempts, r.Documents, r.PaneBindings, r.Decisions, r.Messages, r.UnreadMessages)
	}
	for _, p := range rep.Problems {
		fmt.Fprintf(&b, "problem: %s\n", p)
	}
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	if len(rep.Problems) > 0 {
		return 1
	}
	return 0
}
