package artisan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func kill(id Identity) error {
	fd, err := unix.PidfdOpen(id.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if lv, err := Probe(id); lv != Alive {
		return err
	}
	err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func sweep(c Containment, worker Identity) ([]Identity, error) {
	switch c.Kind {
	case KindCgroup:
		return sweepCgroup(c.Name)
	case KindSubreaper:
		return sweepMembers(c, worker)
	}
	return nil, fmt.Errorf("containment %q on Linux: %w", c.Kind, errors.ErrUnsupported)
}

// sweepCgroup kills a recorded cgroup through cgroup.kill, or removes it
// once it is found empty.
func sweepCgroup(dir string) ([]Identity, error) {
	members, err := containedCgroup(dir)
	if err != nil || len(members) == 0 {
		if err == nil {
			if rerr := unix.Rmdir(dir); rerr != nil && !errors.Is(rerr, unix.ENOENT) && !errors.Is(rerr, unix.EBUSY) {
				err = fmt.Errorf("remove cgroup %s: %w", dir, rerr)
			}
		}
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil {
		return members, fmt.Errorf("kill cgroup %s: %w", dir, err)
	}
	return members, nil
}
