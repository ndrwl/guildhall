package cli

import (
	"errors"
	"fmt"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/config"
)

// The shared configuration-admission boundary (docs/director-modes.md#mode) lives in internal/admit, so the Artisan
// adapters launch attempts through it too; these names keep the CLI's callers short.
const (
	managedFile       = admit.ManagedFile
	directorStoreFile = admit.StoreFile
	modeStandalone    = admit.ModeStandalone
	modeManaged       = admit.ModeManaged
	modeRefused       = admit.ModeRefused
)

type (
	admission    = admit.Admission
	refusalError = admit.RefusalError
	copyFiles    = admit.Files
)

var (
	readCopyFiles   = admit.ReadFiles
	judge           = admit.Judge
	inspectDirector = admit.Inspect
	// reportedConfig is for commands that only read and show settings; appliedConfig for those that act on them
	// but never refuse, such as a manual prune.
	reportedConfig    = admit.Reported
	appliedConfig     = admit.Applied
	admitDirectorMode = admit.Admit
)

// managedRefusal is why a command that acts only on a standalone copy refuses the copy in dir, with what saying how
// a managed copy's state changes instead: "" when the boundary's verdict (admit.Managed) is standalone. A copy the
// mode table refuses is managed, with the boundary's reason appended; a copy whose verdict cannot be read is
// refused too.
func managedRefusal(dir, what string) string {
	managed, err := admit.Managed(dir)
	var refused refusalError
	switch {
	case errors.As(err, &refused):
		return fmt.Sprintf("copy %s is managed: %s; %s", dir, what, refused.Text)
	case err != nil:
		return fmt.Sprintf("copy %s may be managed, as whether it is cannot be read (%v); if it is managed, %s", dir, err, what)
	case managed:
		return fmt.Sprintf("copy %s is managed: %s", dir, what)
	}
	return ""
}

// admitDirector is the boundary's refusing form, returning only the effective config.
func admitDirector(cfgPath string) (*config.Config, error) {
	cfg, _, err := admit.Admit(cfgPath)
	return cfg, err
}

// admitConfig admits the copy's config for a Director session at start and at each reload: the shared boundary
// (admitDirector) for a director's config.
func admitConfig(_, cfgPath string) (*config.Config, error) {
	cfg, err := admitDirector(cfgPath)
	if err != nil {
		return nil, err
	}
	if cfg.Mode != config.ModeDirector {
		return nil, fmt.Errorf("%s is a %s config, not a director's", cfgPath, cfg.Mode)
	}
	return cfg, nil
}
