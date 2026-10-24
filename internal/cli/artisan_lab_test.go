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
	if _, err := f.WriteString("\nguildhall-fake: delay=2s\n"); err != nil {
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
	cmd := exec.Command(binary, "task", "start", "--copy", dir, "--repo", "app", "t-1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workflow start: %v: %s", err, out)
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
	owned, err := client.RecordedTab(ctx, ref.WorkspaceID, ref.TabID, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CloseOwnedTab(ctx, owned); err != nil {
		t.Fatal(err)
	}
	if after := d.attempts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("closing finished tab changed records: %+v -> %+v", before, after)
	}
	t.Logf("finished Artisan tab closed; attempt outcome and spend unchanged: %+v", before[0])
}
