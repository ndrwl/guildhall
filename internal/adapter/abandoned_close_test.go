package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// abandonedFixture is the Herdr workspace a failed Artisan launch created and
// the tabs its launching claim records, in creation order, starting tab first;
// host is a later process's HerdrTabs, owning none of them.
type abandonedFixture struct {
	dir       string
	launcher  *HerdrTabs
	host      *HerdrTabs
	workspace string
	tabs      []TabRef
}

// newAbandonedFixture creates a workspace labelled workspaceLabel holding its
// starting shell and, when tabLabel is not empty, a tab labelled tabLabel.
// keepStarter leaves the starting shell open beside that tab, as a failed
// starting-shell cleanup does (d34); otherwise the tab replaces it.
func newAbandonedFixture(t *testing.T, workspaceLabel, tabLabel string, keepStarter bool) abandonedFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(tabsStubEnv, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := herdr.New(herdr.Options{Session: "lab", Command: []string{exe}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	launcher := NewHerdrTabs(c)
	w, err := launcher.CreateWorkspace(ctx, workspaceLabel, dir)
	if err != nil {
		t.Fatal(err)
	}
	s := w.InitialTab
	f := abandonedFixture{dir: dir, launcher: launcher, host: NewHerdrTabs(c), workspace: w.ID,
		tabs: []TabRef{{Session: s.Session(), WorkspaceID: s.WorkspaceID(), TabID: s.ID(), PaneID: s.PaneID()}}}
	if tabLabel != "" {
		creator := launcher
		if keepStarter {
			// Another host does not know the starting shell, so leaves it open.
			creator = NewHerdrTabs(c)
		}
		ref, err := creator.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: tabLabel, Cwd: dir})
		if err != nil {
			t.Fatal(err)
		}
		f.tabs = append(f.tabs, ref)
	}
	return f
}

func (f abandonedFixture) close() error {
	return f.host.CloseAbandonedWorkspace(context.Background(), f.workspace, f.tabs, []string{closeTestWorkspace, closeTestTab}, closeTestTab)
}

func (f abandonedFixture) listed(t *testing.T) bool {
	t.Helper()
	list, err := f.host.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(list, func(w herdr.Workspace) bool { return w.ID == f.workspace })
}

// closes reports whether a workspace close reached Herdr.
func (f abandonedFixture) closes(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "sent-lab"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Contains(string(b), "close-workspace ")
}

func TestCloseAbandonedWorkspaceClosesRecordedIdleTabs(t *testing.T) {
	for _, tc := range []struct {
		name, tabLabel string
		keepStarter    bool
		listedTabs     int
	}{
		{name: "starting shell and attempt tab", tabLabel: closeTestTab, keepStarter: true, listedTabs: 2},
		{name: "attempt tab only", tabLabel: closeTestTab, listedTabs: 1},
		{name: "starting shell only", listedTabs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAbandonedFixture(t, closeTestWorkspace, tc.tabLabel, tc.keepStarter)
			tabs, err := f.host.client.TabList(context.Background(), f.workspace)
			if err != nil || len(tabs) != tc.listedTabs {
				t.Fatalf("workspace holds %+v (%v), want %d tabs", tabs, err, tc.listedTabs)
			}
			if err := f.close(); err != nil {
				t.Fatal(err)
			}
			if f.listed(t) || !f.closes(t) {
				t.Error("abandoned workspace not closed")
			}
		})
	}
}

func TestCloseAbandonedWorkspaceOfAGoneWorkspaceDoesNothing(t *testing.T) {
	f := newAbandonedFixture(t, closeTestWorkspace, closeTestTab, true)
	if err := os.WriteFile(filepath.Join(f.dir, "lab-closed-"+f.workspace), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); err != nil {
		t.Fatalf("close of a gone workspace = %v", err)
	}
	if f.closes(t) {
		t.Error("close of a gone workspace reached Herdr")
	}
}

func TestCloseAbandonedWorkspaceLeavesAMismatchOpen(t *testing.T) {
	for _, tc := range []struct {
		name, workspaceLabel, tabLabel string
		change                         func(t *testing.T, f *abandonedFixture)
	}{
		{name: "another workspace label", workspaceLabel: "director"},
		{name: "another tab label", tabLabel: "artisan guildhall/d16/build/2"},
		{name: "another session", change: func(t *testing.T, f *abandonedFixture) {
			for i := range f.tabs {
				f.tabs[i].Session = "other"
			}
		}},
		{name: "a recorded tab in another workspace", change: func(t *testing.T, f *abandonedFixture) { f.tabs[1].WorkspaceID = "w1" }},
		{name: "a foreign tab", change: func(t *testing.T, f *abandonedFixture) {
			if _, err := NewHerdrTabs(f.host.client).CreateTab(context.Background(), herdr.TabSpec{WorkspaceID: f.workspace, Label: closeTestTab}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no recorded tabs", change: func(t *testing.T, f *abandonedFixture) { f.tabs = nil }},
		{name: "an extra pane", change: func(t *testing.T, f *abandonedFixture) {
			if err := os.WriteFile(filepath.Join(f.dir, "lab-tab-"+strings.ReplaceAll(f.tabs[1].TabID, ":", "_")+"-split"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a moved pane", change: func(t *testing.T, f *abandonedFixture) {
			if err := os.WriteFile(filepath.Join(f.dir, "lab-tab-"+strings.ReplaceAll(f.tabs[1].TabID, ":", "_")+"-moved-pane"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a busy attempt pane", change: func(t *testing.T, f *abandonedFixture) {
			if err := f.host.AdoptTab(context.Background(), f.tabs[1]); err != nil {
				t.Fatal(err)
			}
			if err := f.host.RunInTab(context.Background(), f.tabs[1], "sleep 60"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a busy starting shell", change: func(t *testing.T, f *abandonedFixture) {
			if err := f.launcher.RunInTab(context.Background(), f.tabs[0], "sleep 60"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, tab := closeTestWorkspace, closeTestTab
			if tc.workspaceLabel != "" {
				ws = tc.workspaceLabel
			}
			if tc.tabLabel != "" {
				tab = tc.tabLabel
			}
			f := newAbandonedFixture(t, ws, tab, true)
			if tc.change != nil {
				tc.change(t, &f)
			}
			if err := f.close(); !IsLeftOpen(err) {
				t.Fatalf("close = %v, want a left-open reason", err)
			}
			if !f.listed(t) || f.closes(t) {
				t.Error("refused close removed the workspace")
			}
		})
	}
}

// createdTabs is a TabHost whose CreateTab creates the tab and then fails, as
// HerdrTabs does when it leaves the workspace's starting shell open.
type createdTabs struct{ *fakeTabs }

func (h createdTabs) CreateTab(ctx context.Context, spec herdr.TabSpec) (TabRef, error) {
	ref, err := h.fakeTabs.CreateTab(ctx, spec)
	if err != nil {
		return TabRef{}, err
	}
	return ref, errors.New("replacement tab created, but starting shell left open")
}

// TestTabLauncherOpenReturnsTheCreatedTabWithALaterError: a failure after the
// tab exists, in the host's CreateTab or in waiting for the shell, still names
// the tab, so a launch can record what it created (d91).
func TestTabLauncherOpenReturnsTheCreatedTabWithALaterError(t *testing.T) {
	f := newFakeTabs("lab", "bash")
	l := TabLauncher{Host: createdTabs{f}, WorkspaceID: "w1", ShellReady: time.Minute, Poll: time.Millisecond}
	tab, _, err := l.Open(context.Background(), "artisan a", "/work", nil)
	if err == nil || tab.TabID == "" || len(f.created) != 1 {
		t.Errorf("Open after a cleanup failure = %+v, %v; created %+v", tab, err, f.created)
	}
	busy := newFakeTabs("lab", "bash")
	busy.busyReads = 1 << 30
	// timing:allow deliberate timeout of an in-memory pane which never becomes ready; no startup is expected.
	l = TabLauncher{Host: busy, WorkspaceID: "w1", ShellReady: 20 * time.Millisecond, Poll: time.Millisecond}
	tab, _, err = l.Open(context.Background(), "artisan a", "/work", nil)
	if err == nil || tab.TabID == "" || len(busy.created) != 1 {
		t.Errorf("Open of a never-idle shell = %+v, %v", tab, err)
	}
	if sent := busy.sentLines(); len(sent) != 0 {
		t.Errorf("Open typed %q", sent)
	}
}
