package adapter_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/herdr"
)

// TestHerdrTabsAbandonedCloserContract runs the abandoned workspace closer's
// contract against HerdrTabs over the stub Herdr, whose new workspaces hold a
// starting tab, as Herdr's do. Abandon's host is the failed launch's; Host is a
// later process's, owning none of its tabs.
func TestHerdrTabsAbandonedCloserContract(t *testing.T) {
	adaptertest.AbandonedCloserContract(t, func(t *testing.T) adaptertest.AbandonedCloserHarness {
		stub := adapter.NewHerdrStub(t, "lab")
		host := adapter.NewHerdrTabs(stub.Client())
		ctx := context.Background()
		foreign := func(ws string) {
			if _, err := adapter.NewHerdrTabs(stub.Client()).CreateTab(ctx, herdr.TabSpec{WorkspaceID: ws, Label: "foreign"}); err != nil {
				t.Fatal(err)
			}
		}
		return adaptertest.AbandonedCloserHarness{
			Host:        host,
			Session:     "lab",
			StartingTab: true,
			Abandon: func(t *testing.T, wsLabel, tabLabel string, keepStarter bool) (string, []adapter.TabRef) {
				t.Helper()
				launcher := adapter.NewHerdrTabs(stub.Client())
				w, err := launcher.CreateWorkspace(ctx, wsLabel, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				s := w.InitialTab
				tabs := []adapter.TabRef{{Session: s.Session(), WorkspaceID: s.WorkspaceID(), TabID: s.ID(), PaneID: s.PaneID()}}
				if tabLabel != "" {
					creator := launcher
					if keepStarter {
						// Another host does not know the starting shell, so leaves it open.
						creator = adapter.NewHerdrTabs(stub.Client())
					}
					ref, err := creator.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: tabLabel})
					if err != nil {
						t.Fatal(err)
					}
					tabs = append(tabs, ref)
				}
				return w.ID, tabs
			},
			Listed: func(ws string) bool {
				list, err := host.Workspaces(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return slices.ContainsFunc(list, func(w herdr.Workspace) bool { return w.ID == ws })
			},
			Closed: func() bool {
				return slices.ContainsFunc(stub.Sent(), func(line string) bool { return strings.HasPrefix(line, "close-workspace ") })
			},
			AddForeignTab:         foreign,
			ChangeTabsDuringClose: stub.ChangeOnNextProcessInfo,
			SplitPane:             func(ref adapter.TabRef) { stub.Split(ref.TabID) },
			Busy:                  func(ref adapter.TabRef) { stub.Busy(ref.PaneID) },
			RemoveAllTabs:         stub.RemoveTabs,
			Gone:                  stub.Gone,
			MovePane:              func(ref adapter.TabRef) { stub.Move(ref.TabID) },
		}
	})
}
