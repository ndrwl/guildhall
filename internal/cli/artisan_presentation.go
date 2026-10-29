package cli

import (
	"context"
	"io"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
)

// artisanPresentation selects only the Director's explicit non-default session
// for each attempt's new workspace. No session retains the process-only launcher mode.
func artisanPresentation(ctx context.Context, cfg *config.Config, storePath, state string, warnings io.Writer) (*launch.Presentation, error) {
	session, err := cfg.HerdrSession()
	if err != nil || session == "" {
		return nil, err
	}
	client, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, herdrTimeout)
	defer cancel()
	if _, err := client.Status(ctx); err != nil {
		return nil, err
	}
	closeAfter, err := cfg.ArtisanWorkspaceCloseAfter()
	if err != nil {
		return nil, err
	}
	host := &artisanWorkspaces{TabHost: adapter.NewHerdrTabs(client), client: client, storePath: storePath, state: state, warnings: warnings}
	return &launch.Presentation{Host: host, Report: client.ReportAgentOutcome, ReadBack: client.PaneGet, Warnings: warnings, CloseAfter: closeAfter}, nil
}
