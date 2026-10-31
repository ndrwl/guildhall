package adapter

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/herdr"
)

const (
	closeTestWorkspace = "└ d16 · build #1"
	closeTestTab       = "artisan guildhall/d16/build/1"
)

// closeFixture is an attempt's Herdr workspace created by one host and the
// record of its tab, adopted by a later host as the workspace close sweep does.
type closeFixture struct {
	dir  string
	host *HerdrTabs
	ref  TabRef
}

func newCloseFixture(t *testing.T, workspaceLabel, tabLabel string) closeFixture {
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
	ref, err := launcher.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: tabLabel, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	sweep := NewHerdrTabs(c)
	if err := sweep.AdoptTab(ctx, ref); err != nil {
		t.Fatal(err)
	}
	return closeFixture{dir: dir, host: sweep, ref: ref}
}

func (f closeFixture) tabPath(id string) string {
	return filepath.Join(f.dir, "lab-tab-"+strings.ReplaceAll(id, ":", "_"))
}

func (f closeFixture) listed(t *testing.T) bool {
	t.Helper()
	list, err := f.host.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(list, func(w herdr.Workspace) bool { return w.ID == f.ref.WorkspaceID })
}

func (f closeFixture) close() error {
	return f.host.CloseWorkspace(context.Background(), f.ref, []string{closeTestWorkspace, closeTestTab}, closeTestTab)
}

func TestCloseWorkspaceClosesOnlyAnExactIdleAttemptWorkspace(t *testing.T) {
	for _, label := range []string{closeTestWorkspace, closeTestTab} {
		t.Run(label, func(t *testing.T) {
			f := newCloseFixture(t, label, closeTestTab)
			if err := f.close(); err != nil {
				t.Fatal(err)
			}
			if f.listed(t) {
				t.Error("closed workspace still listed")
			}
			lines := readLines(t, filepath.Join(f.dir, "sent-lab"))
			if len(lines) == 0 || lines[len(lines)-1] != "close-workspace "+f.ref.WorkspaceID {
				t.Errorf("mutations = %q", lines)
			}
			if err := f.host.AdoptTab(context.Background(), f.ref); !herdr.IsNotFound(err) {
				t.Errorf("adopting the closed record = %v, want not found", err)
			}
		})
	}
}

func TestCloseWorkspaceLeavesAMismatchOpen(t *testing.T) {
	for _, tc := range []struct {
		name, workspaceLabel, tabLabel string
		change                         func(t *testing.T, f *closeFixture)
	}{
		{name: "another workspace label", workspaceLabel: "director"},
		{name: "another tab label", tabLabel: "artisan guildhall/d16/build/2"},
		{name: "another session", change: func(t *testing.T, f *closeFixture) { f.ref.Session = "other" }},
		{name: "an extra tab", change: func(t *testing.T, f *closeFixture) {
			if _, err := f.host.CreateTab(context.Background(), herdr.TabSpec{WorkspaceID: f.ref.WorkspaceID, Label: "mine"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an extra pane", change: func(t *testing.T, f *closeFixture) {
			if err := os.WriteFile(f.tabPath(f.ref.TabID)+"-split", nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a moved pane", change: func(t *testing.T, f *closeFixture) {
			if err := os.WriteFile(f.tabPath(f.ref.TabID)+"-moved-pane", nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a busy shell", change: func(t *testing.T, f *closeFixture) {
			if err := f.host.RunInTab(context.Background(), f.ref, "sleep 60"); err != nil {
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
			f := newCloseFixture(t, ws, tab)
			if tc.change != nil {
				tc.change(t, &f)
			}
			err := f.close()
			if !IsLeftOpen(err) {
				t.Fatalf("close = %v, want a left-open reason", err)
			}
			if !f.listed(t) {
				t.Error("refused close removed the workspace")
			}
			for _, line := range readLines(t, filepath.Join(f.dir, "sent-lab")) {
				if strings.HasPrefix(line, "close-workspace ") {
					t.Errorf("refused close reached Herdr: %q", line)
				}
			}
		})
	}
}

// growDuringCheck makes the stub Herdr add an idle tab labelled label to f's
// workspace the next time a close reads a pane's processes there.
func (f closeFixture) growDuringCheck(t *testing.T, label string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "lab-grow-"+f.ref.WorkspaceID), []byte(label), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWorkspaceRefusesAWorkspaceThatGainsATabWhileChecked(t *testing.T) {
	f := newCloseFixture(t, closeTestWorkspace, closeTestTab)
	f.growDuringCheck(t, closeTestTab)
	err := f.close()
	if !IsLeftOpen(err) {
		t.Fatalf("close = %v, want a left-open reason", err)
	}
	if !f.listed(t) {
		t.Error("refused close removed the workspace")
	}
	for _, line := range readLines(t, filepath.Join(f.dir, "sent-lab")) {
		if strings.HasPrefix(line, "close-workspace ") {
			t.Errorf("refused close reached Herdr: %q", line)
		}
	}
}

func TestCloseWorkspaceNeedsAnOwnedRecord(t *testing.T) {
	f := newCloseFixture(t, closeTestWorkspace, closeTestTab)
	f.host = NewHerdrTabs(f.host.client)
	if err := f.close(); err == nil || IsLeftOpen(err) {
		t.Errorf("close of an unadopted record = %v, want refusal", err)
	}
	if !f.listed(t) {
		t.Error("unadopted record closed its workspace")
	}
}

func TestCloseWorkspaceOfAGoneWorkspaceIsNotFound(t *testing.T) {
	f := newCloseFixture(t, closeTestWorkspace, closeTestTab)
	if err := os.WriteFile(filepath.Join(f.dir, "lab-closed-"+f.ref.WorkspaceID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); !herdr.IsNotFound(err) {
		t.Errorf("close = %v, want not found", err)
	}
}

func TestCloseLabelledTabChecksTheLabel(t *testing.T) {
	f := newCloseFixture(t, "lab", closeTestTab)
	ctx := context.Background()
	if err := f.host.CloseLabelledTab(ctx, f.ref, "artisan other"); !IsLeftOpen(err) {
		t.Errorf("close with another label = %v, want a left-open reason", err)
	}
	if _, err := os.Stat(f.tabPath(f.ref.TabID)); err != nil {
		t.Fatalf("mislabelled tab closed: %v", err)
	}
	if err := f.host.CloseLabelledTab(ctx, f.ref, closeTestTab); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.tabPath(f.ref.TabID)); !os.IsNotExist(err) {
		t.Errorf("tab still present: %v", err)
	}
	if !f.listed(t) {
		t.Error("tab close closed the workspace")
	}
}
