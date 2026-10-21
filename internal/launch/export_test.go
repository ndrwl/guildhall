package launch

import (
	"os"
	"os/exec"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
)

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
		time.Sleep(time.Hour)
	}
}
