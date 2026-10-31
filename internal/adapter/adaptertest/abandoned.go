package adaptertest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// AbandonedCloserHarness is one adapter.AbandonedWorkspaceCloser under
// AbandonedCloserContract, on a terminal of its own. Host closes the
// workspaces; the other fields make and change them on Host's terminal.
type AbandonedCloserHarness struct {
	Host    adapter.AbandonedWorkspaceCloser
	Session string
	// StartingTab says new workspaces hold a starting tab of their own, which
	// a launch records as tabs[0].
	StartingTab bool
	// Abandon makes a workspace labelled wsLabel as a failed launch leaves it,
	// and returns it and the tabs its launching claim records: the starting tab,
	// when the host has one, and, when tabLabel is not empty, a tab labelled
	// tabLabel. keepStarter leaves the starting tab open beside that tab, as a
	// failed starting-shell cleanup does; otherwise the tab replaces it.
	Abandon func(t *testing.T, wsLabel, tabLabel string, keepStarter bool) (workspaceID string, tabs []adapter.TabRef)
	// Listed reports whether the terminal lists workspace ws.
	Listed func(ws string) bool
	// Closed reports whether any workspace close reached the terminal.
	Closed func() bool
	// AddForeignTab opens a tab in ws that no launch recorded.
	AddForeignTab func(ws string)
	// ChangeTabsDuringClose changes ws's tab set, once, after Host's next close
	// has listed and checked ws's tabs and before it closes ws. Where it can,
	// it leaves only tabs the check would pass, such as by closing one, so
	// that only comparing a fresh listing with the first shows the change.
	ChangeTabsDuringClose func(ws string)
	// SplitPane gives ref's tab a second pane.
	SplitPane func(ref adapter.TabRef)
	// Busy starts a program in ref's pane.
	Busy func(ref adapter.TabRef)
	// RemoveAllTabs leaves ws listed but holding no tab.
	RemoveAllTabs func(ws string)
	// Gone closes ws by other means, so it is no longer listed.
	Gone func(ws string)
	// MovePane, when not nil, makes ref's pane id name a pane of another tab.
	MovePane func(ref adapter.TabRef)
}

// Labels the contract's abandoned workspaces and attempt tabs carry.
const (
	abandonedWorkspace = "└ d16 · build #1"
	abandonedTab       = "artisan guildhall/d16/build/1"
)

// AbandonedCloserContract runs adapter.AbandonedWorkspaceCloser's contract
// against hosts from newHost: a workspace holding nothing but a failed
// launch's recorded idle tabs, one pane each, closes; a workspace no longer
// listed succeeds with nothing done; anything else is refused with a
// LeftOpenError and the workspace is left listed and unclosed. The recorded
// tabs passed in are never changed.
func AbandonedCloserContract(t *testing.T, newHost func(t *testing.T) AbandonedCloserHarness) {
	labels := []string{abandonedWorkspace, abandonedTab}
	closeAbandoned := func(t *testing.T, h AbandonedCloserHarness, ws string, tabs []adapter.TabRef, tabLabel string) error {
		t.Helper()
		before := slices.Clone(tabs)
		err := h.Host.CloseAbandonedWorkspace(context.Background(), ws, tabs, labels, tabLabel)
		if !slices.Equal(tabs, before) {
			t.Errorf("close changed the recorded tabs %+v to %+v", before, tabs)
		}
		return err
	}

	for _, tc := range []struct {
		name, tabLabel string
		keepStarter    bool
		starting       bool
		closeLabel     string
	}{
		{name: "attempt tab only", tabLabel: abandonedTab},
		{name: "starting tab and attempt tab", tabLabel: abandonedTab, keepStarter: true, starting: true},
		{name: "starting tab only", starting: true},
		// The starting tab's label is the terminal's, never the attempt's.
		{name: "starting tab not labelled the tab label", starting: true, closeLabel: "artisan guildhall/d16/build/9"},
	} {
		t.Run("closes "+tc.name, func(t *testing.T) {
			h := newHost(t)
			if tc.starting && !h.StartingTab {
				t.Skip("host's workspaces hold no starting tab")
			}
			ws, tabs := h.Abandon(t, abandonedWorkspace, tc.tabLabel, tc.keepStarter)
			label := abandonedTab
			if tc.closeLabel != "" {
				label = tc.closeLabel
			}
			if err := closeAbandoned(t, h, ws, tabs, label); err != nil {
				t.Fatal(err)
			}
			if h.Listed(ws) || !h.Closed() {
				t.Errorf("abandoned workspace not closed: listed %v, closed %v", h.Listed(ws), h.Closed())
			}
		})
	}

	t.Run("does nothing for a gone workspace", func(t *testing.T) {
		h := newHost(t)
		ws, tabs := h.Abandon(t, abandonedWorkspace, abandonedTab, true)
		h.Gone(ws)
		if err := closeAbandoned(t, h, ws, tabs, abandonedTab); err != nil {
			t.Fatalf("close of a gone workspace = %v", err)
		}
		if h.Closed() {
			t.Error("close of a gone workspace reached the terminal")
		}
	})

	last := func(tabs []adapter.TabRef) int { return len(tabs) - 1 }
	for _, tc := range []struct {
		name, wsLabel, tabLabel string
		starting                bool
		moved                   bool
		change                  func(h AbandonedCloserHarness, ws string, tabs []adapter.TabRef) []adapter.TabRef
	}{
		{name: "a workspace with no tab", change: func(h AbandonedCloserHarness, ws string, tabs []adapter.TabRef) []adapter.TabRef {
			h.RemoveAllTabs(ws)
			return tabs
		}},
		{name: "an unrecorded tab", change: func(h AbandonedCloserHarness, ws string, tabs []adapter.TabRef) []adapter.TabRef {
			h.AddForeignTab(ws)
			return tabs
		}},
		{name: "no recorded tabs", change: func(_ AbandonedCloserHarness, _ string, _ []adapter.TabRef) []adapter.TabRef { return nil }},
		{name: "a recorded tab with another label", tabLabel: "artisan guildhall/d16/build/2"},
		{name: "another workspace label", wsLabel: "director"},
		{name: "a tab with two panes", change: func(h AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			h.SplitPane(tabs[last(tabs)])
			return tabs
		}},
		{name: "a busy attempt pane", change: func(h AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			h.Busy(tabs[last(tabs)])
			return tabs
		}},
		{name: "a busy starting tab", starting: true, change: func(h AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			h.Busy(tabs[0])
			return tabs
		}},
		{name: "a recorded tab in another session", change: func(_ AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			tabs = slices.Clone(tabs)
			for i := range tabs {
				tabs[i].Session = "other-" + tabs[i].Session
			}
			return tabs
		}},
		{name: "a recorded tab in another workspace", change: func(_ AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			tabs = slices.Clone(tabs)
			tabs[last(tabs)].WorkspaceID = "other-" + tabs[last(tabs)].WorkspaceID
			return tabs
		}},
		{name: "tabs that changed between the check and the close", change: func(h AbandonedCloserHarness, ws string, tabs []adapter.TabRef) []adapter.TabRef {
			h.ChangeTabsDuringClose(ws)
			return tabs
		}},
		{name: "a moved pane", moved: true, change: func(h AbandonedCloserHarness, _ string, tabs []adapter.TabRef) []adapter.TabRef {
			h.MovePane(tabs[last(tabs)])
			return tabs
		}},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			h := newHost(t)
			switch {
			case tc.starting && !h.StartingTab:
				t.Skip("host's workspaces hold no starting tab")
			case tc.moved && h.MovePane == nil:
				t.Skip("host cannot move a pane")
			}
			ws, tab := abandonedWorkspace, abandonedTab
			if tc.wsLabel != "" {
				ws = tc.wsLabel
			}
			if tc.tabLabel != "" {
				tab = tc.tabLabel
			}
			id, tabs := h.Abandon(t, ws, tab, true)
			if tc.change != nil {
				tabs = tc.change(h, id, tabs)
			}
			err := closeAbandoned(t, h, id, tabs, abandonedTab)
			t.Logf("left open: %v", err)
			var left *adapter.LeftOpenError
			switch {
			case tc.moved && !adapter.IsLeftOpen(err):
				t.Fatalf("close = %v, want a left-open reason", err)
			case !tc.moved && !errors.As(err, &left):
				t.Fatalf("close = %v, want a *adapter.LeftOpenError", err)
			}
			if !h.Listed(id) || h.Closed() {
				t.Errorf("refused close removed the workspace: listed %v, closed %v", h.Listed(id), h.Closed())
			}
		})
	}

	t.Run("refuses empty labels plainly", func(t *testing.T) {
		h := newHost(t)
		ws, tabs := h.Abandon(t, abandonedWorkspace, abandonedTab, false)
		for _, c := range []struct {
			labels []string
			tab    string
		}{{nil, abandonedTab}, {labels, ""}} {
			err := h.Host.CloseAbandonedWorkspace(context.Background(), ws, tabs, c.labels, c.tab)
			var left *adapter.LeftOpenError
			if err == nil || errors.As(err, &left) {
				t.Errorf("close with labels %q, tab label %q = %v, want a plain error", c.labels, c.tab, err)
			}
		}
		if !h.Listed(ws) || h.Closed() {
			t.Error("close without labels removed the workspace")
		}
	})
}
