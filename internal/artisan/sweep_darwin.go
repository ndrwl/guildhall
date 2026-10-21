package artisan

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func kill(id Identity) error {
	if lv, err := Probe(id); lv != Alive {
		return err
	}
	if err := unix.Kill(id.PID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func sweep(c Containment, worker Identity) ([]Identity, error) {
	if c.Kind != KindGroup {
		return nil, fmt.Errorf("containment %q on macOS: %w", c.Kind, errors.ErrUnsupported)
	}
	return sweepMembers(c, worker)
}
