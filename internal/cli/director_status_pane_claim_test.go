package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestDirectorStatusShowsAPaneClaimWithNoOwner is the d216 shape: the decision was answered and the build attempt
// recorded, but its pane launcher never owned it, so it is absent, holds a bound pane claim and has no
// launched.json, while next.json still waits on the decision. Once the claim is older than the start budget,
// needs_me lists a resume item naming the command that recovers it; a fresh claim, a launch still starting, lists
// nothing.
func TestDirectorStatusShowsAPaneClaimWithNoOwner(t *testing.T) {
	for _, c := range []struct {
		name     string
		launcher bool
		age      time.Duration
		command  string
	}{
		{"launcher gone", true, 5 * time.Minute, "guildhall artisan resume --repo app --task t1 --step build --attempt 1"},
		{"launcher unrecorded", false, 5 * time.Minute, "guildhall artisan resume --repo app --task t1 --step build --attempt 1 --abandon-pane-claim"},
		{"launch still starting", true, time.Second, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path, state := filepath.Join(dir, "director.store"), filepath.Join(dir, "state")
			s, err := store.Open(path, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			def, err := workflow.Default()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateTask("i-1", store.Task{Repository: "app", ID: "t1", Title: "Approved", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartAttempt("i-2", "app", "t1", "build"); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			o := ownedAttempt{state: state}
			var launcher *artisan.Identity
			if c.launcher {
				launcher = goneLauncher(t)
			}
			attemptDir := heldClaim(t, o, launcher)
			now := time.Now()
			claimed := now.Add(-c.age)
			if err := os.Chtimes(filepath.Join(attemptDir, launch.PaneClaimFile), claimed, claimed); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(state, "app", "t1", "next.json"), `{"wait":"decision","step":"plan","decision":"accept-1"}`+"\n")
			st, err := readDirectorStatus(context.Background(), path, state, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Running) != 1 || st.Running[0].State != reconcile.Absent || !strings.HasPrefix(st.Running[0].Reason, "pane claim held, no owner") {
				t.Fatalf("running = %+v", st.Running)
			}
			if c.command == "" {
				if len(st.NeedsMe) != 0 {
					t.Fatalf("needs_me = %+v, want nothing while the launch may still be starting", st.NeedsMe)
				}
				return
			}
			if len(st.NeedsMe) != 1 || st.NeedsMe[0].Kind != "resume" || st.NeedsMe[0].Command != c.command ||
				!strings.HasPrefix(st.NeedsMe[0].Reason, "absent: pane claim held, no owner") {
				t.Fatalf("needs_me = %+v, want one resume item running %q", st.NeedsMe, c.command)
			}
		})
	}
}

// TestPaneStartBudgetCoversSetupAndOwnership: a bound claim younger than the default setup timeout plus the copy's
// lock wait and the ownership slack is a launch that may still be starting.
func TestPaneStartBudgetCoversSetupAndOwnership(t *testing.T) {
	if got, want := launch.PaneStartBudget(config.DefaultLockWait), 30*time.Second+config.DefaultLockWait+10*time.Second; got != want {
		t.Fatalf("PaneStartBudget = %s, want %s", got, want)
	}
}
