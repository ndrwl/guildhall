package launch

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// WorkerEnv is workerEnv.
var WorkerEnv = workerEnv

// DieAtWorkerStart makes Run's units kill this process, with no cleanup,
// when asked to start the worker: the launcher is gone after taking
// ownership and before its worker is recorded.
func DieAtWorkerStart() {
	newUnit = func(name string) (artisan.Unit, error) {
		u, err := artisan.NewUnit(name)
		if err != nil {
			return nil, err
		}
		return dyingUnit{u}, nil
	}
}

type dyingUnit struct{ artisan.Unit }

func (dyingUnit) Start(*exec.Cmd) (artisan.Identity, error) {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Kill()
	}
	for {
		time.Sleep(time.Hour) // timing:allow unreachable fallback after self-kill, not synchronization.
	}
}

// HideUnstartedContainment models process-group platforms on every host: no
// containment is recoverable until a worker has started. The underlying unit
// is real, so its normal startup and cleanup contracts are preserved.
func HideUnstartedContainment() {
	newUnit = func(name string) (artisan.Unit, error) {
		u, err := artisan.NewUnit(name)
		return &unnamedBeforeStart{Unit: u}, err
	}
}

type unnamedBeforeStart struct {
	artisan.Unit
	started bool
}

func (u *unnamedBeforeStart) Containment() artisan.Containment {
	if !u.started {
		return artisan.Containment{}
	}
	return u.Unit.Containment()
}

func (u *unnamedBeforeStart) Start(cmd *exec.Cmd) (artisan.Identity, error) {
	u.started = true
	return u.Unit.Start(cmd)
}

// MainIfDetacher plays the detaching session runtime of TestDetachedLauncherLeavesTheSessionUnit.
var MainIfDetacher = mainIfDetacher

// AfterLauncherStart makes Process.Launch call f with each hidden launcher's pid after starting it and before
// reading its identity, until t ends.
func AfterLauncherStart(t testing.TB, f func(pid int)) {
	afterLauncherStart = f
	t.Cleanup(func() { afterLauncherStart = nil })
}
