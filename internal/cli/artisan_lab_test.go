package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestWorkflowArtisanHerdrLab is opt-in, zero-model live evidence. Its caller
// must provision an isolated named lab and put a guarded Herdr helper shim on
// PATH; ordinary CI runs only the deterministic pane/process contract tests.
func TestWorkflowArtisanHerdrLab(t *testing.T) {
	session := os.Getenv("GUILDHALL_TEST_HERDR_LAB")
	binary := os.Getenv("GUILDHALL_TEST_BINARY")
	if session == "" || binary == "" {
		t.Skip("requires guarded named Herdr lab and built guildhall binary")
	}
	if !strings.HasPrefix(session, "fm-lab-") {
		t.Fatal("refusing non-lab session")
	}
	d, dir := newRunCopy(t)
	guide := filepath.Join(dir, "baseline", "stages", "plan.md")
	f, err := os.OpenFile(guide, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nguildhall-fake: delay=5s\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	// A short close delay, not "0", which would never close.
	b = []byte(strings.Replace(string(b), "[local]\n", "[local]\nherdr_session = \""+session+"\"\nartisan_workspace_close_after = \"1s\"\n", 1))
	if err := os.WriteFile(d.config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parent, err := client.WorkspaceCreate(ctx, "lab-director", "")
	if err != nil {
		t.Fatal(err)
	}
	startSession(t, d.store, store.DirectorPane{Session: session, Workspace: parent.ID, Tab: parent.InitialTab.ID(), Pane: parent.InitialTab.PaneID()})
	focusedWorkspace := func() string {
		out, err := exec.Command("herdr", "workspace", "list", "--session", session).CombinedOutput()
		if err != nil {
			t.Fatalf("read Director focus: %s, %v", out, err)
		}
		var list struct {
			Result struct {
				Workspaces []struct {
					ID      string `json:"workspace_id"`
					Focused bool   `json:"focused"`
				} `json:"workspaces"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &list); err != nil {
			t.Fatal(err)
		}
		for _, w := range list.Result.Workspaces {
			if w.Focused {
				return w.ID
			}
		}
		t.Fatal("no focused Director workspace")
		return ""
	}
	focusBefore := focusedWorkspace()
	cmd := exec.Command(binary, "task", "start", "--copy", dir, "--repo", "app", "t-1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workflow start: %v: %s", err, out)
	}
	if focusAfter := focusedWorkspace(); focusBefore != focusAfter {
		t.Fatalf("Artisan stole focus: before %s after %s", focusBefore, focusAfter)
	}
	at := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}
	b, err = os.ReadFile(filepath.Join(at.Dir(d.state), launch.PaneClaimFile))
	if err != nil {
		t.Fatal(err)
	}
	var ref adapter.TabRef
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	workspaces, err := client.WorkspaceList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspaceFound := false
	for _, w := range workspaces {
		if w.ID == ref.WorkspaceID && w.Label == "└ t-1 · plan #1" {
			workspaceFound = true
		}
	}
	if !workspaceFound {
		t.Fatalf("separate Artisan workspace missing: %+v", workspaces)
	}
	if len(workspaces) != 2 || workspaces[0].ID != parent.ID || workspaces[1].ID != ref.WorkspaceID {
		t.Fatalf("Artisan not directly below its Director: %+v", workspaces)
	}
	t.Logf("workflow workspace order: %+v", workspaces)
	// The PATH shim routes this through the guarded lab helper, just like the
	// client's API calls. Read the actual terminal, not merely launcher.log.
	var paneText []byte
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		paneText, err = exec.Command("herdr", "pane", "read", ref.PaneID, "--session", session).CombinedOutput()
		if err == nil && strings.Contains(string(paneText), "fake artisan:") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no readable live pane output: %s, %v", paneText, err)
		}
	}
	t.Logf("live pane output: %s", paneText)
	tabs, err := client.TabList(ctx, ref.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tab := range tabs {
		if tab.ID == ref.TabID && strings.Contains(tab.Label, "t-1/plan/1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("workflow Artisan tab missing: %+v", tabs)
	}
	if attempts := d.attempts(t); len(attempts) != 1 || attempts[0].Outcome != "" {
		t.Fatalf("expected running attempt visible in tab, got %+v", attempts)
	}
	t.Logf("running workflow Artisan appeared: session=%s workspace=%s tab=%s pane=%s", session, ref.WorkspaceID, ref.TabID, ref.PaneID)
	d.waitDecision(t, "accept-1")
	before := d.attempts(t)
	if len(before) != 1 || before[0].Outcome != store.AttemptSucceeded {
		t.Fatalf("attempts = %+v", before)
	}
	// The launcher bridge should finish and leave an idle shell, never close its
	// own pane. Wait for that independently of result recording.
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		info, err := client.PaneProcessInfo(ctx, ref.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if info.ShellIdle() {
			break
		}
		if time.Now().After(end) {
			t.Fatal("finished pane did not return to shell")
		}
	}
	// The supervisor's workspace close sweep closes it once the launcher is gone
	// and the 1s delay has passed, and leaves the Director's workspace and tab.
	cfg, err := config.Load(d.config)
	if err != nil {
		t.Fatal(err)
	}
	app, _, sweepErr := newTestApp(nil, nil)
	sweep, err := app.workspaceCloseSweep(cfg, d.store, d.state)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(at.Dir(d.state), paneClosed)
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		sweep.run()
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the sweep did not close the finished workspace; stderr %s", sweepErr)
		}
	}
	workspaces, err = client.WorkspaceList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range workspaces {
		if w.ID == ref.WorkspaceID {
			t.Fatalf("finished Artisan workspace still listed: %+v", workspaces)
		}
	}
	if _, err := client.RecordedTab(ctx, parent.ID, parent.InitialTab.ID(), parent.InitialTab.PaneID()); err != nil {
		t.Fatalf("the Director's tab did not survive the sweep: %v", err)
	}
	if after := d.attempts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("closing finished workspace changed records: %+v -> %+v", before, after)
	}
	if msgs := directorMessages(t, d.store); slices.ContainsFunc(msgs, func(id string) bool { return strings.Contains(id, store.MessageHerdrLeftOpen) }) {
		t.Fatalf("the sweep reported a left-open workspace: %v", msgs)
	}
	t.Logf("sweep closed finished Artisan workspace %s; Director workspace %s and tab %s remain; attempt unchanged: %+v", ref.WorkspaceID, parent.ID, parent.InitialTab.ID(), before[0])
	// Evidence for whether Herdr reuses a closed workspace's id.
	probe, err := client.WorkspaceCreate(ctx, "lab-id-probe", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("herdr", "workspace", "close", probe.ID, "--session", session).CombinedOutput(); err != nil {
			t.Logf("close probe Herdr workspace %s: %v: %s", probe.ID, err, out)
		}
	})
	t.Logf("herdr id after close: closed %s, next created %s, reused=%v", ref.WorkspaceID, probe.ID, probe.ID == ref.WorkspaceID)
}
