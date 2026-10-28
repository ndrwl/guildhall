package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestRealClaudeIsRefused: the built binary is not a test binary, so interp.CheckAdapter lets its plan profile
// name the claude adapter with no claudetest stub installed. The claude it runs is TestMain's guard
// (testenv.InstallClaudeGuard), not a real Claude Code, so the attempt fails with the guard's message and makes no
// model call.
func TestRealClaudeIsRefused(t *testing.T) {
	d := newBinDirector(t, filepath.Join(runCopy(t), "claude.toml"))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)
	var outcome store.AttemptOutcome
	testenv.WaitFor(t, "plan-1 to end", func() bool {
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			return false
		}
		attempts, err := s.Attempts("app", "t-1")
		_ = s.Close()
		if err != nil || len(attempts) != 1 {
			return false
		}
		outcome = attempts[0].Outcome
		return outcome != ""
	})
	log, err := os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "launcher.log"))
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper's first claude call is its version probe, which reports the exit status and not the guard's
	// standard error; a later call would log the message itself.
	guard := strings.Contains(string(log), testenv.ClaudeGuardMessage) || strings.Contains(string(log), fmt.Sprintf("exit status %d", testenv.ClaudeGuardExit))
	if outcome != store.AttemptFailed || !guard {
		t.Errorf("plan-1 %s; launcher log:\n%s\nwant it failed by the claude guard (%q, exit %d)", outcome, log, testenv.ClaudeGuardMessage, testenv.ClaudeGuardExit)
	}
}
