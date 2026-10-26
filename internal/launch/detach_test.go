package launch

import (
	"crypto/rand"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// detacherEnv makes the test binary play a session runtime that launches an attempt: it starts, detached as Launch
// starts a launcher, an artisantest worker recording into the directory it names, then waits to be ended.
const detacherEnv = "GUILDHALL_LAUNCH_TEST_DETACHER"

func mainIfDetacher() {
	inv := os.Getenv(detacherEnv)
	if inv == "" {
		return
	}
	cmd, err := startDetached(func() *exec.Cmd { return artisantest.Command(inv, artisantest.Worker{Lifetime: time.Minute}) })
	if err != nil {
		os.Exit(2)
	}
	_ = cmd.Process.Release()
	time.Sleep(time.Minute) // timing:allow fixture lifetime; parent verifies detach then kills the recorded identity.
	os.Exit(0)
}

// TestDetachedLauncherLeavesTheSessionUnit runs a session runtime in its unit, with the environment the supervisor
// gives it, and has it start a process detached as Launch starts a launcher: the process is not one of the unit's
// members, and survives the unit's kill and close, so a Director's session end leaves its attempts' launchers to
// their own stop (docs/director.md#start).
func TestDetachedLauncherLeavesTheSessionUnit(t *testing.T) {
	u, err := artisan.NewSessionUnit("detach-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	inv := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtime := exec.Command(exe, "-test.run=^$")
	runtime.Env = append(append(os.Environ(), detacherEnv+"="+inv), artisan.SessionEnv(u)...)
	if _, err := u.Start(runtime); err != nil {
		t.Fatal(err)
	}
	var launcher artisan.Identity
	for end := time.Now().Add(2 * time.Minute); launcher.PID == 0; time.Sleep(10 * time.Millisecond) {
		if entries, err := artisantest.Inventory(inv); err == nil && len(entries) == 1 {
			launcher = entries[0].Identity
		}
		if time.Now().After(end) {
			t.Fatal("the detached process never started")
		}
	}
	t.Cleanup(func() { _ = artisan.Kill(launcher) })
	members, err := u.Members()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(members, launcher) {
		t.Errorf("the detached process %d is a member of the session unit %+v", launcher.PID, u.Containment())
	}
	if err := u.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.Exited():
	case <-time.After(2 * time.Minute):
		t.Fatal("the runtime did not exit after Kill")
	}
	if err := u.Close(); err != nil {
		t.Errorf("Close of the session unit: %v", err)
	}
	if lv, err := artisan.Probe(launcher); lv != artisan.Alive {
		t.Errorf("the detached process is %v (%v) after the session unit ended", lv, err)
	}
}
