package artisan

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

// Lookup returns the identity of the live process pid: its creation time.
// OpenProcess failing with ERROR_INVALID_PARAMETER means no such process;
// access denied is an error, so Probe reports it unverified, never gone.
func Lookup(pid int) (Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return Identity{}, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return identityFromHandle(pid, h)
}

// LookupChild returns the identity of pid, live or exited, for a child the caller started and has not yet reaped
// (its os.Process still holds a handle, so the process object and its pid survive its exit). Only such a pid is
// safe; never use it for an arbitrary pid, use Lookup.
func LookupChild(pid int) (Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return Identity{}, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return creationIdentity(pid, h)
}

func identityFromHandle(pid int, h windows.Handle) (Identity, error) {
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return Identity{}, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return Identity{}, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	case uint32(windows.WAIT_TIMEOUT):
	default:
		return Identity{}, fmt.Errorf("pid %d: unexpected wait state %#x", pid, state)
	}
	return creationIdentity(pid, h)
}

// creationIdentity returns the identity of the process h, live or exited: its creation time.
func creationIdentity(pid int, h windows.Handle) (Identity, error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, Token: strconv.FormatInt(c.Nanoseconds(), 10)}, nil
}
