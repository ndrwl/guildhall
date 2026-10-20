package adapter

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const nativeStopSupported = true

var queryProcessImage = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

type windowsWorker struct{ handle syscall.Handle }

func processCreationTime(h syscall.Handle) (uint64, error) {
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

func nativeCreationTime() (uint64, error) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, err
	}
	return processCreationTime(h)
}

func openNativeWorker(pid uint32, created uint64, exe string) (nativeWorker, error) {
	// PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE.
	// No privilege adjustment, process enumeration, shell PID, or name-based kill.
	h, err := syscall.OpenProcess(0x0001|0x1000|0x00100000, false, pid)
	if err != nil {
		return nil, err
	}
	verified := false
	defer func() {
		if !verified {
			_ = syscall.CloseHandle(h)
		}
	}()
	actual, err := processCreationTime(h)
	if err != nil {
		return nil, err
	}
	if created == 0 || actual != created {
		return nil, errors.New("worker process creation identity mismatch")
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	ok, _, callErr := queryProcessImage.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return nil, fmt.Errorf("query worker image: %w", callErr)
	}
	want, err := filepath.Abs(exe)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Clean(syscall.UTF16ToString(buf[:size])), filepath.Clean(want)) {
		return nil, errors.New("worker executable identity mismatch")
	}
	state, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || state != syscall.WAIT_TIMEOUT {
		return nil, errors.New("worker exited before ownership was established")
	}
	verified = true
	return &windowsWorker{handle: h}, nil
}

func (w *windowsWorker) stop(ctx context.Context) error {
	state, err := syscall.WaitForSingleObject(w.handle, 0)
	if err != nil {
		return err
	}
	if state == syscall.WAIT_OBJECT_0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syscall.TerminateProcess(w.handle, 1); err != nil {
		// A concurrent natural exit is success only if the retained handle
		// itself proves termination.
		if state, waitErr := syscall.WaitForSingleObject(w.handle, 0); waitErr != nil || state != syscall.WAIT_OBJECT_0 {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := syscall.WaitForSingleObject(w.handle, 50)
		if err != nil {
			return err
		}
		switch state {
		case syscall.WAIT_OBJECT_0:
			return nil
		case syscall.WAIT_TIMEOUT:
		default:
			return fmt.Errorf("unexpected worker wait state %d", state)
		}
	}
}

func (w *windowsWorker) close() error { return syscall.CloseHandle(w.handle) }
