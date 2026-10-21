package artisan

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobTerminate is JOB_OBJECT_TERMINATE, the access TerminateJobObject needs.
const jobTerminate = 0x0008

func kill(id Identity) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(id.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		// A job closing can terminate this process between inventory and
		// OpenProcess. Windows may deny termination access during exit;
		// suppress that error only if the recorded identity is proven gone.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if lv, _ := Probe(id); lv == Gone {
				return nil
			}
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	current, err := identityFromHandle(id.PID, h)
	if errors.Is(err, errNoProcess) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != id {
		return nil
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		if _, checkErr := identityFromHandle(id.PID, h); errors.Is(checkErr, errNoProcess) {
			return nil
		}
		return err
	}
	return nil
}

// sweep terminates the named job when it still holds members.
func sweep(c Containment, _ Identity) ([]Identity, error) {
	if c.Kind != KindJob {
		return nil, fmt.Errorf("containment %q on Windows: %w", c.Kind, errors.ErrUnsupported)
	}
	p, err := windows.UTF16PtrFromString(c.Name)
	if err != nil {
		return nil, err
	}
	r, _, e := procOpenJobObject.Call(jobQuery|jobTerminate, 0, uintptr(unsafe.Pointer(p)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil
		}
		return nil, fmt.Errorf("OpenJobObject %s: %w", c.Name, e)
	}
	job := windows.Handle(r)
	defer func() { _ = windows.CloseHandle(job) }()
	members, err := jobMembers(job, c.Name)
	if err != nil || len(members) == 0 {
		return nil, err
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return members, fmt.Errorf("terminate job %s: %w", c.Name, err)
	}
	return members, nil
}
