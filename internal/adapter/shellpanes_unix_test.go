//go:build unix

package adapter_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/herdr"
)

// TestShellPanesContract runs the terminal boundary's contract test against
// adaptertest.ShellPanes, so a runtime that passes the conformance suite in a
// shell pane has passed it over a host that keeps the Herdr tab contract.
func TestShellPanesContract(t *testing.T) {
	adapter.RunTabHostContract(t, func(t *testing.T) adapter.TabHostHarness {
		p := adaptertest.NewShellPanes(t, "lab", "w1")
		return adapter.TabHostHarness{Host: p, Foreign: adaptertest.NewShellPanes(t, "other-lab", "w1"), Session: "lab", Sent: p.Sent,
			Reopen: func() adapter.TabHost { return p.Reopen() }}
	})
}

// TestShellPanesAbandonedCloserContract runs the abandoned workspace closer's
// contract against adaptertest.ShellPanes, whose workspaces hold no starting
// tab, so launch recovery tested over it is tested over HerdrTabs' rules.
func TestShellPanesAbandonedCloserContract(t *testing.T) {
	adaptertest.AbandonedCloserContract(t, func(t *testing.T) adaptertest.AbandonedCloserHarness {
		p := adaptertest.NewShellPanes(t, "lab", "w1")
		ctx := context.Background()
		foreign := func(ws string) {
			if _, err := p.Reopen().CreateTab(ctx, herdr.TabSpec{WorkspaceID: ws, Label: "foreign"}); err != nil {
				t.Error(err)
			}
		}
		return adaptertest.AbandonedCloserHarness{
			Host:    p.Reopen(),
			Session: "lab",
			Abandon: func(t *testing.T, wsLabel, tabLabel string, _ bool) (string, []adapter.TabRef) {
				t.Helper()
				w, err := p.CreateWorkspace(ctx, wsLabel, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				var tabs []adapter.TabRef
				if tabLabel != "" {
					ref, err := p.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: tabLabel})
					if err != nil {
						t.Fatal(err)
					}
					tabs = append(tabs, ref)
				}
				return w.ID, tabs
			},
			Listed: func(ws string) bool {
				list, err := p.Workspaces(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return slices.ContainsFunc(list, func(w herdr.Workspace) bool { return w.ID == ws })
			},
			Closed:        func() bool { return p.Closes() > 0 },
			AddForeignTab: foreign,
			// The workspace holds one tab, so a change can only add one.
			ChangeTabsDuringClose: func(ws string) { p.DuringClose(func() { foreign(ws) }) },
			SplitPane:             p.SplitPane,
			Busy: func(ref adapter.TabRef) {
				if err := p.RunInTab(ctx, ref, "sleep 60"); err != nil {
					t.Fatal(err)
				}
			},
			RemoveAllTabs: p.RemoveTabs,
			Gone:          p.Gone,
		}
	})
}
