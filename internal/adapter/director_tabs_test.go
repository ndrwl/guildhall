package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/tabcontract"
	"github.com/ndrwl/guildhall/internal/herdr"
)

func TestHerdrIdleTabCloseContract(t *testing.T) {
	tabcontract.TestIdleClose(t, func(t *testing.T, scenario string) tabcontract.CloseFixture {
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
		h := NewHerdrTabs(c)
		ref, err := h.CreateTab(context.Background(), herdr.TabSpec{WorkspaceID: "w1", Label: "director"})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "lab-tab-"+strings.ReplaceAll(ref.TabID, ":", "_"))
		switch scenario {
		case "busy", "transient-busy":
			state := "busy"
			if scenario == "transient-busy" {
				state = "startup"
			}
			if err := os.WriteFile(filepath.Join(dir, "lab-pane-"+strings.ReplaceAll(ref.PaneID, ":", "_")), []byte(state), 0o600); err != nil {
				t.Fatal(err)
			}
		case "session":
			ref.Session = "other"
		case "workspace":
			ref.WorkspaceID = "w2"
		case "tab":
			ref.TabID = "w1:t999"
		case "pane":
			ref.PaneID = "w1:p999"
		}
		return tabcontract.CloseFixture{
			Close: func(ctx context.Context) error { return h.CloseTab(ctx, ref) },
			Present: func() bool {
				_, err := os.Stat(path)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				return err == nil
			},
		}
	})
}

func TestIdleTabCloseRefusesUnsafeIdentity(t *testing.T) {
	for _, kind := range []string{"unowned", "busy", "split", "moved", "moved-pane", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
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
			h := NewHerdrTabs(c)
			ctx := context.Background()
			ref, err := h.CreateTab(ctx, herdr.TabSpec{WorkspaceID: "w1", Label: "director"})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "lab-tab-"+strings.ReplaceAll(ref.TabID, ":", "_"))
			switch kind {
			case "unowned":
				h = NewHerdrTabs(c)
			case "busy":
				if err := h.RunInTab(ctx, ref, "sleep 60"); err != nil {
					t.Fatal(err)
				}
			case "split":
				if err := os.WriteFile(path+"-split", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "moved-pane":
				if err := os.WriteFile(path+"-moved-pane", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "moved":
				if err := os.WriteFile(path, []byte("w2"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := h.CloseTab(ctx, ref); err == nil {
				t.Error("unsafe close succeeded")
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("unsafe close removed tab: %v", err)
			}
		})
	}
}

func TestNewWorkspaceReplacesOnlyItsOwnedStartingShell(t *testing.T) {
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
	h := NewHerdrTabs(c)
	ctx := context.Background()
	w, err := h.CreateWorkspace(ctx, "director", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lab-tab-"+w.ID+"_t1")); err != nil {
		t.Fatal(err)
	}
	// The initial shell is still reading startup files on its first probe.
	if err := os.WriteFile(filepath.Join(dir, "lab-pane-"+w.ID+"_p1"), []byte("startup"), 0o600); err != nil {
		t.Fatal(err)
	}
	tab, err := h.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: "director alpha", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lab-tab-"+w.ID+"_t1")); !os.IsNotExist(err) {
		t.Errorf("starting shell still exists: %v", err)
	}
	if err := h.AdoptTab(ctx, tab); err != nil {
		t.Errorf("replacement lost: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, "sent-lab"))
	if len(lines) != 1 || lines[0] != "close "+w.ID+":t1" {
		t.Errorf("mutations = %q", lines)
	}
	// A later host must not infer ownership from the existing workspace's label.
	reopened := NewHerdrTabs(c)
	if _, err := reopened.CreateTab(ctx, herdr.TabSpec{WorkspaceID: w.ID, Label: "other"}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, filepath.Join(dir, "sent-lab")); len(got) != 1 {
		t.Errorf("closed an existing workspace's tab: %q", got)
	}
}
