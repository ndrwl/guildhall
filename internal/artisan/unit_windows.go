package artisan

import (
	"errors"
	"fmt"
	"os"
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
	mu           sync.Mutex
	name         string
	job          windows.Handle
	cmd          *exec.Cmd
	worker       Identity
	workerHandle windows.Handle
	closed       bool
	done         chan struct{}
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
	u.cmd = cmd
	go func() {
		_ = cmd.Wait()
		close(u.done)
	}()
	fail := func(what string, err error) (Identity, error) {
		return Identity{}, errors.Join(fmt.Errorf("%w: %s: %v", ErrContainment, what, err), u.closeLocked())
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SUSPEND_RESUME|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail("OpenProcess", err)
	}
	u.workerHandle = h
	if err := windows.AssignProcessToJobObject(u.job, h); err != nil {
		return fail("AssignProcessToJobObject", err)
	}
	var in int32
	if r, _, e := procIsProcessInJob.Call(uintptr(h), uintptr(u.job), uintptr(unsafe.Pointer(&in))); r == 0 || in == 0 {
		return fail("IsProcessInJob", e)
	}
	id, err := identityFromHandle(cmd.Process.Pid, h)
	if err != nil {
		return fail("Lookup", err)
	}
	if r, _, _ := procNtResumeProcess.Call(uintptr(h)); r != 0 {
		return fail("NtResumeProcess", fmt.Errorf("NTSTATUS %#x", r))
	}
	u.worker = id
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
		id, err := u.member(int(pid))
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

func (u *jobUnit) member(pid int) (Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return Identity{}, errNoProcess
	}
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var in int32
	if r, _, e := procIsProcessInJob.Call(uintptr(h), uintptr(u.job), uintptr(unsafe.Pointer(&in))); r == 0 {
		return Identity{}, e
	}
	if in == 0 {
		return Identity{}, errNoProcess
	}
	return identityFromHandle(pid, h)
}

// Interrupt implements Unit.Interrupt. The retained worker handle prevents
// PID reuse while the unit can signal its console process group.
func (u *jobUnit) Interrupt() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return err
	}
	if _, err := identityFromHandle(u.worker.PID, u.workerHandle); errors.Is(err, errNoProcess) {
		return nil
	} else if err != nil {
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
	return u.closeLocked()
}

func (u *jobUnit) closeLocked() error {
	if u.cmd != nil {
		if err := windows.TerminateJobObject(u.job, 1); err != nil {
			return err
		}
		if u.worker.PID == 0 {
			if err := u.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
		}
		<-u.done
	}
	if u.workerHandle != 0 {
		if err := windows.CloseHandle(u.workerHandle); err != nil {
			return err
		}
		u.workerHandle = 0
	}
	if err := windows.CloseHandle(u.job); err != nil {
		return err
	}
	u.closed = true
	return nil
}

func (u *jobUnit) Containment() Containment { return Containment{Kind: KindJob, Name: u.name} }
