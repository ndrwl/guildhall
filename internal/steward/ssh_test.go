package steward_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
)

type sshHarness struct{ f steward.StubFleet }

func (h sshHarness) Runner() steward.Runner { return h.f.Runner() }
func (h sshHarness) Answer(_ *testing.T, alias string, output []byte, failure string) {
	h.f.Answer(alias, output, failure)
}
func (h sshHarness) Ran(_ *testing.T, alias string) [][]string { return h.f.Ran(alias) }
func (h sshHarness) Refuse(_ *testing.T, alias string)         { h.f.Refuse(alias) }
func (h sshHarness) Hang(_ *testing.T, alias string)           { h.f.Hang(alias) }

// TestSSHRunnerContract runs the shared contract against ssh, played by the test binary in a separate process.
func TestSSHRunnerContract(t *testing.T) {
	stewardtest.RunRunnerContract(t, func(t *testing.T) stewardtest.RunnerHarness {
		return sshHarness{steward.NewStubFleet(t)}
	})
}

// TestSSHTimeoutBoundsEachRun covers the ssh Runner's own limit, which applies even when the caller sets none.
func TestSSHTimeoutBoundsEachRun(t *testing.T) {
	f := steward.NewStubFleet(t)
	f.Hang("alpha")
	r := f.Runner()
	r.Timeout = 200 * time.Millisecond // timing:allow intentional timeout of a tool that never answers; no startup-speed assertion.
	start := time.Now()
	_, err := r.Run(context.Background(), "alpha", steward.ReloadCommand(steward.DefaultGuildhall, "/home/guild/directors/tools"))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "ssh: ") {
		t.Errorf("Run error = %v, want an ssh error wrapping context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Run returned after %v", elapsed)
	}
}

// TestCheckCommand pins the allow-list: exactly config reload and director mode --json, each ending in the copy
// directory, with the reserved director reload refused until guildhall/d98 builds it.
func TestCheckCommand(t *testing.T) {
	for _, argv := range [][]string{
		steward.ReloadCommand(steward.DefaultGuildhall, "/home/guild/directors/tools"),
		steward.ReloadCommand("/home/guild/bin/guildhall", "/home/guild/directors/tools"),
		steward.ReloadCommand("C:/Users/guild/bin/guildhall.exe", "C:/Users/guild/directors/games"),
		steward.ModeCommand(steward.DefaultGuildhall, "/home/guild/directors/tools"),
	} {
		if err := steward.CheckCommand(argv); err != nil {
			t.Errorf("CheckCommand %q: %v", argv, err)
		}
	}
	err := steward.CheckCommand([]string{steward.DefaultGuildhall, "director", "reload", "--copy", "/home/guild/directors/tools"})
	if !errors.Is(err, steward.ErrNotAllowed) || !strings.Contains(err.Error(), "guildhall/d98") {
		t.Errorf("reserved director reload: %v, want a refusal naming guildhall/d98", err)
	}
}
