package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
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
	b = []byte(strings.Replace(string(b), "[local]\n", "[local]\nherdr_session = \""+session+"\"\n", 1))
	if err := os.WriteFile(d.config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("herdr", "workspace", "create", "--label", "lab-director", "--session", session).CombinedOutput(); err != nil {
		t.Fatalf("create lab Director presentation: %s, %v", out, err)
	}
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
	b, err = os.ReadFile(filepath.Join(at.Dir(d.state), launch.PaneClaim))
	if err != nil {
		t.Fatal(err)
	}
	var ref adapter.TabRef
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	client, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workspaces, err := client.WorkspaceList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspaceFound := false
	for _, w := range workspaces {
		if w.ID == ref.WorkspaceID && w.Label == "artisan app/t-1/plan/1" {
			workspaceFound = true
		}
	}
	if !workspaceFound {
		t.Fatalf("separate Artisan workspace missing: %+v", workspaces)
	}
	// The PATH shim routes this through the guarded lab helper, just like the
	// client's API calls. Read the actual terminal, not merely launcher.log.
	paneText, err := exec.Command("herdr", "pane", "read", ref.PaneID, "--session", session).CombinedOutput()
	if err != nil || !strings.Contains(string(paneText), "fake artisan:") {
		t.Fatalf("no readable live pane output: %s, %v", paneText, err)
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
	if len(tabs) != 1 || tabs[0].PaneCount != 1 {
		t.Fatal("refusing to close workspace with unexpected tabs or panes")
	}
	if _, err := client.RecordedTab(ctx, ref.WorkspaceID, ref.TabID, ref.PaneID); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("herdr", "workspace", "close", ref.WorkspaceID, "--session", session).CombinedOutput(); err != nil {
		t.Fatalf("close owned finished workspace: %s, %v", out, err)
	}
	if after := d.attempts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("closing finished workspace changed records: %+v -> %+v", before, after)
	}
	t.Logf("finished Artisan workspace closed; attempt outcome and spend unchanged: %+v", before[0])
}
