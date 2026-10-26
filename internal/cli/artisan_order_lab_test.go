package cli

import (
	"bytes"
	"context"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// TestArtisanChildWorkspaceHerdrLab exercises the presentation boundary without
// models or changing shell/prompt configuration. Provision and every CLI call
// must be routed by the caller through the guarded named-session lab helper.
func TestArtisanChildWorkspaceHerdrLab(t *testing.T) {
	session := os.Getenv("GUILDHALL_TEST_HERDR_LAB")
	if session == "" {
		t.Skip("requires a guarded Herdr lab")
	}
	if !strings.HasPrefix(session, "fm-lab-") {
		t.Fatal("refusing non-lab session")
	}
	client, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	parent, err := client.WorkspaceCreate(ctx, "lab-director", "")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := client.WorkspaceCreate(ctx, "lab-foreign", "")
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	path := filepath.Join(state, "director.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	startSession(t, path, store.DirectorPane{Session: session, Workspace: parent.ID, Tab: parent.InitialTab.ID(), Pane: parent.InitialTab.PaneID()})
	var warnings bytes.Buffer
	host := &artisanWorkspaces{TabHost: adapter.NewHerdrTabs(client), client: client, storePath: path, state: state, warnings: &warnings}
	for _, label := range []string{"└ d3 · build #1", "└ d3 · review #1"} {
		if _, err := host.CreateWorkspace(ctx, label, ""); err != nil {
			t.Fatal(err)
		}
	}
	if warnings.Len() != 0 {
		t.Fatalf("ordering warnings: %s", &warnings)
	}
	list, err := client.WorkspaceList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || list[0].ID != parent.ID || list[1].Label != "└ d3 · build #1" || list[2].Label != "└ d3 · review #1" || list[3].ID != foreign.ID {
		t.Fatalf("unexpected complete workspace order: %+v", list)
	}
	t.Logf("verified complete order with two Artisan workspaces below Director: %+v", list)

	// Exercise refusal guards against the same real server: a failed optional
	// placement must leave every existing workspace in order and the new child usable.
	for _, guard := range []string{"corrupt journal", "stale Director binding", "busy lock", "short startup budget"} {
		t.Run(guard, func(t *testing.T) {
			guardState := t.TempDir()
			guardPath := filepath.Join(guardState, "director.store")
			s, err := store.Open(guardPath, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			pane := store.DirectorPane{Session: session, Workspace: parent.ID, Tab: parent.InitialTab.ID(), Pane: parent.InitialTab.PaneID()}
			if guard == "stale Director binding" {
				pane.Tab = foreign.InitialTab.ID()
			}
			startSession(t, guardPath, pane)
			if guard == "corrupt journal" {
				if err := os.WriteFile(filepath.Join(guardState, "artisan-workspaces.json"), []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if guard == "busy lock" {
				release, err := lockWorkspaceOrder(session)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			createCtx := ctx
			if guard == "short startup budget" {
				var cancel context.CancelFunc
				createCtx, cancel = context.WithTimeout(ctx, time.Minute)
				defer cancel()
			}
			before, err := client.WorkspaceList(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var warning bytes.Buffer
			h := &artisanWorkspaces{TabHost: adapter.NewHerdrTabs(client), client: client, storePath: guardPath, state: guardState, warnings: &warning}
			child, err := h.CreateWorkspace(createCtx, "└ guarded · build #1", "")
			if err != nil || createCtx.Err() != nil || !strings.Contains(warning.String(), "ordering skipped") {
				t.Fatalf("creation=%+v err=%v ctx=%v warning=%s", child, err, createCtx.Err(), &warning)
			}
			after, err := client.WorkspaceList(ctx)
			if err != nil || len(after) != len(before)+1 || !reflect.DeepEqual(after[:len(before)], before) || after[len(before)].ID != child.ID {
				t.Fatalf("guard changed existing order: before=%+v after=%+v err=%v", before, after, err)
			}
			if _, err := client.RecordedTab(ctx, child.ID, child.InitialTab.ID(), child.InitialTab.PaneID()); err != nil {
				t.Fatalf("new child not usable after refusal: %v", err)
			}
			t.Logf("%s: existing order unchanged, child %s usable; %s", guard, child.ID, &warning)
		})
	}
}
