package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// dieAtStartEnv makes the test binary a launcher that runs the attempt in
// its value and is killed as it starts the worker (mainIfDyingLauncher).
const dieAtStartEnv = "GUILDHALL_LAUNCH_TEST_DIE_AT_START"

// mainIfDyingLauncher runs launch.Run under launch.DieAtWorkerStart when
// dieAtStartEnv names an attempt; reaching its exit means Run returned.
func mainIfDyingLauncher() {
	v := os.Getenv(dieAtStartEnv)
	if v == "" {
		return
	}
	var a launch.Attempt
	if err := json.Unmarshal([]byte(v), &a); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	launch.DieAtWorkerStart()
	_, err := launch.Run(context.Background(), launch.Config{Attempt: a, Command: adapter.Command{Argv: []string{os.Args[0]}}})
	fmt.Fprintf(os.Stderr, "Run returned: %v\n", err)
	os.Exit(3)
}

// killedAtWorkerStart runs attempt a in a launcher process killed between
// taking ownership and recording its worker, and returns the record it left.
func killedAtWorkerStart(t *testing.T, a launch.Attempt) store.Ownership {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	v, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), dieAtStartEnv+"="+string(v))
	out, err := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); err == nil || code == 2 || code == 3 {
		t.Fatalf("launcher exited %d, %v, not killed: %s", code, err, out)
	}
	o := ownership(t, a)
	if o.State != store.OwnerStarting || o.Endpoint == "" || o.Containment.Worker != (store.ProcessIdentity{}) {
		t.Fatalf("ownership %+v; want starting with its endpoint and no worker", o)
	}
	if o.Containment.Name != "" {
		// A run take-over leaves the dead launcher's emptied unit; a sweep removes it.
		t.Cleanup(func() {
			_, _ = artisan.Sweep(artisan.Containment{Kind: o.Containment.Kind, Name: o.Containment.Name, Reason: o.Containment.Reason}, artisan.Identity{})
		})
	}
	return o
}

// namedUnits reports whether this platform's units are found by name (cgroup,
// job) rather than by their worker (a process group).
func namedUnits(t *testing.T) bool {
	t.Helper()
	u, err := artisan.NewUnit(fmt.Sprintf("kind-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	return u.Containment().Name != ""
}

// TestRecoveryAfterALauncherKilledBeforeItsWorkerIsRecorded pins F20: a
// named unit (cgroup, job) is recorded with ownership, so a launcher killed
// before its worker is recorded is proven exited by its empty unit and
// resume settles it, or run takes it over. A process group is found only by
// its worker, so on group kinds the window stays: resume and run refuse it
// (docs/artisan.md#limits).
func TestRecoveryAfterALauncherKilledBeforeItsWorkerIsRecorded(t *testing.T) {
	for _, recovery := range []string{"resume", "run"} {
		t.Run(recovery, func(t *testing.T) {
			a := newAttempt(t)
			dead := killedAtWorkerStart(t, a)
			c, err := launch.Status(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if !namedUnits(t) {
				if dead.Containment != (store.Containment{}) {
					t.Fatalf("ownership %+v; a group is recorded only with its worker", dead)
				}
				t.Logf("group containment: the window stays (%+v)", c)
				rep, err := resume(t, a)
				if !errors.Is(err, store.ErrOwnerUnknown) || rep.Owner != launch.OwnerUnverified {
					t.Fatalf("Resume = %+v, %v; want unverified and ErrOwnerUnknown", rep, err)
				}
				cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 100 * time.Millisecond})
				done, cancel := start(a, cmd)
				defer cancel()
				if r := wait(t, done); !errors.Is(r.err, store.ErrOwnerUnknown) {
					t.Fatalf("Run = %+v, %v; want ErrOwnerUnknown", r.r, r.err)
				}
				return
			}
			if dead.Containment.Name == "" {
				t.Fatalf("ownership %+v; want the named unit recorded with ownership", dead)
			}
			if c.State != reconcile.Exited || c.Launcher != "gone" {
				t.Fatalf("classification %+v; want exited with the launcher gone", c)
			}
			switch recovery {
			case "resume":
				rep, err := resume(t, a)
				if err != nil || rep.Owner != launch.OwnerExited || rep.AttemptOutcome != store.AttemptFailed {
					t.Fatalf("Resume = %+v, %v; want owner exited and failed recorded", rep, err)
				}
				if rep.Outcome == nil || rep.Outcome.State != artisan.Failed || rep.Outcome.Detail != "launcher exited before its worker was recorded" {
					t.Fatalf("outcome %+v; want failed: launcher exited before its worker was recorded", rep.Outcome)
				}
				if o := ownership(t, a); o.State != store.OwnerReleased {
					t.Fatalf("ownership %+v not released", o)
				}
			case "run":
				cmd, _ := worker(t, a, artisantest.Worker{Lifetime: 100 * time.Millisecond})
				done, cancel := start(a, cmd)
				defer cancel()
				r := wait(t, done)
				if r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
					t.Fatalf("take-over Run = %+v, %v", r.r, r.err)
				}
				if o := ownership(t, a); o.LauncherIdentity == dead.LauncherIdentity || o.State != store.OwnerReleased {
					t.Fatalf("ownership %+v; want the take-over's, released", o)
				}
			}
			if got := storedOutcome(t, a); got != store.AttemptFailed {
				t.Errorf("store outcome %q, want failed", got)
			}
		})
	}
}
