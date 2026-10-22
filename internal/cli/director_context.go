package cli

import (
	"errors"

	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// directorContextReport runs `guildhall director context-report [--copy <dir>]`, the Claude Code status-line
// command of a Director session (docs/director.md#automatic-handoff).
func (a *App) directorContextReport(args []string) int {
	return a.fail(errors.New("director context-report: not implemented"))
}

// handoffPolicy is the automatic handoff policy session directory dir records under cfg for runtime rt.
func handoffPolicy(cfg *config.Config, rt runtimes.Session, dir string) (store.HandoffPolicy, error) {
	return store.HandoffPolicy{}, errors.New("not implemented")
}
