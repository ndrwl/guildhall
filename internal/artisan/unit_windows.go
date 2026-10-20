package artisan

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	ntdll               = windows.NewLazySystemDLL("ntdll.dll")
	procCreateJobObject = kernel32.NewProc("CreateJobObjectW")
	procIsProcessInJob  = kernel32.NewProc("IsProcessInJob")
	procNtResumeProcess = ntdll.NewProc("NtResumeProcess")
)

// maxMembers bounds the job's process id list.
const maxMembers = 512

type processIDList struct {
	Assigned, InList uint32
	IDs              [maxMembers]uintptr
}

// jobUnit contains the worker in a named Job Object that kills its processes
// when the last handle closes and does not allow breakaway.
type jobUnit struct {
	mu     sync.Mutex
	name   string
	job    windows.Handle
	cmd    *exec.Cmd
	worker Identity
	closed bool
	done   chan struct{}
}

// newUnit creates the job Local\guildhall-<name>; a name already in use is
// refused rather than joined.
func newUnit(name string) (Unit, error) {
	u := &jobUnit{name: `Local\guildhall-` + name, done: make(chan struct{})}
	p, err := windows.UTF16PtrFromString(u.name)
	if err != nil {
		return nil, err
	}
	// A nil security descriptor makes the handle non-inheritable.
	r, _, e := procCreateJobObject.Call(0, uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return nil, fmt.Errorf("%w: CreateJobObject %s: %v", ErrContainment, u.name, e)
	}
	u.job = windows.Handle(r)
	if errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(u.job)
		return nil, fmt.Errorf("%w: job %s already exists", ErrContainment, u.name)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// Kill-on-close only: no JOB_OBJECT_LIMIT_BREAKAWAY_OK or SILENT_BREAKAWAY_OK.
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(u.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(u.job)
		return nil, fmt.Errorf("%w: SetInformationJobObject: %v", ErrContainment, err)
	}
	return u, nil
}

// Start creates the worker suspended, assigns it to the job, verifies that
// with IsProcessInJob and only then resumes it, so no descendant can start
// outside the job.
func (u *jobUnit) Start(cmd *exec.Cmd) (Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.closed:
		return Identity{}, ErrClosed
	case u.cmd != nil:
		return Identity{}, ErrStarted
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return Identity{}, err
	}
	fail := func(what string, err error) (Identity, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Identity{}, fmt.Errorf("%w: %s: %v", ErrContainment, what, err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail("OpenProcess", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(u.job, h); err != nil {
		return fail("AssignProcessToJobObject", err)
	}
	var in int32
	if r, _, e := procIsProcessInJob.Call(uintptr(h), uintptr(u.job), uintptr(unsafe.Pointer(&in))); r == 0 || in == 0 {
		return fail("IsProcessInJob", e)
	}
	id, err := Lookup(cmd.Process.Pid)
	if err != nil {
		return fail("Lookup", err)
	}
	if r, _, _ := procNtResumeProcess.Call(uintptr(h)); r != 0 {
		return fail("NtResumeProcess", fmt.Errorf("NTSTATUS %#x", r))
	}
	u.cmd, u.worker = cmd, id
	go func() {
		_ = cmd.Wait()
		close(u.done)
	}()
	return id, nil
}

func (u *jobUnit) usable() error {
	switch {
	case u.closed:
		return ErrClosed
	case u.cmd == nil:
		return ErrNotStarted
	}
	return nil
}

func (u *jobUnit) Members() ([]Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return nil, err
	}
	var l processIDList
	if err := windows.QueryInformationJobObject(u.job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&l)), uint32(unsafe.Sizeof(l)), nil); err != nil {
		return nil, err
	}
	if l.Assigned > l.InList {
		return nil, fmt.Errorf("job %s has %d processes, more than the %d listed", u.name, l.Assigned, l.InList)
	}
	var out []Identity
	for _, pid := range l.IDs[:l.InList] {
		id, err := Lookup(int(pid))
		switch {
		case errors.Is(err, errNoProcess):
		case err != nil:
			return nil, err
		default:
			out = append(out, id)
		}
	}
	return out, nil
}

// Interrupt sends CTRL_BREAK to the worker's console process group; a worker
// with no shared console does not receive it.
func (u *jobUnit) Interrupt() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return err
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(u.worker.PID))
}

func (u *jobUnit) Terminate() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return err
	}
	return ErrUnsupportedRung
}

func (u *jobUnit) Kill() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return err
	}
	return windows.TerminateJobObject(u.job, 1)
}

func (u *jobUnit) Exited() <-chan struct{} { return u.done }

// Close terminates the job, waits for the worker and closes the last handle,
// which kills anything still contained.
func (u *jobUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	var err error
	if u.cmd != nil {
		err = windows.TerminateJobObject(u.job, 1)
		<-u.done
	}
	return errors.Join(err, windows.CloseHandle(u.job))
}
