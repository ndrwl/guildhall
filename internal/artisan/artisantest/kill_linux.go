package artisantest

import (
	"errors"

	"github.com/ndrwl/guildhall/internal/artisan"
	"golang.org/x/sys/unix"
)

func KillVerified(id artisan.Identity) error {
	fd, err := unix.PidfdOpen(id.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if lv, err := artisan.Probe(id); lv != artisan.Alive {
		return err
	}
	err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
