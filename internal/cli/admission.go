package cli

import (
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
