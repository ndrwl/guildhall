package cli

import (
	"context"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
)

// artisanPresentation selects only the Director's explicit non-default session
// and named workspace. No session retains the process-only launcher mode.
func artisanPresentation(ctx context.Context, cfg *config.Config, copyDir string) (*launch.Presentation, error) {
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
	host := adapter.NewHerdrTabs(client)
	workspace, err := adapter.EnsureWorkspace(ctx, host, cfg.Name, copyDir)
	if err != nil {
		return nil, err
	}
	return &launch.Presentation{Host: host, Workspace: workspace, Report: client.ReportAgent}, nil
}
