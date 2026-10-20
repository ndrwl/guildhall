package artisan

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a live process.
const stillActive = 259

// Lookup returns the identity of the live process pid: its creation time.
// OpenProcess failing with ERROR_INVALID_PARAMETER means no such process;
// access denied is an error, so Probe reports it unverified, never gone.
func Lookup(pid int) (Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return Identity{}, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return Identity{}, err
	}
	if code != stillActive {
		return Identity{}, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, Token: strconv.FormatInt(c.Nanoseconds(), 10)}, nil
}
