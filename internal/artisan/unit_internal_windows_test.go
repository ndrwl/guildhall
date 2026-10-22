package artisan

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
	"golang.org/x/sys/windows"
)

func TestUnitExitWorker(t *testing.T) {
	if os.Getenv("ARTISAN_UNIT_EXIT_WORKER") == "1" {
		os.Exit(0)
	}
}

func TestInterruptAfterWorkerExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unit, err := newUnit("interrupt-exited-" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	u := unit.(*jobUnit)
	t.Cleanup(func() { _ = u.Close() })
	cmd := exec.Command(exe, "-test.run=^TestUnitExitWorker$")
	cmd.Env = append(os.Environ(), "ARTISAN_UNIT_EXIT_WORKER=1")
	if _, err := u.Start(cmd); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.Exited():
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("worker did not exit")
	}
	if state, err := windows.WaitForSingleObject(u.workerHandle, 0); state != windows.WAIT_OBJECT_0 || err != nil {
		t.Fatalf("retained worker handle = %v, %v; want signaled", state, err)
	}
	if err := u.Interrupt(); err != nil {
		t.Fatalf("Interrupt after exit: %v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if u.workerHandle != 0 {
		t.Fatal("Close retained the worker handle")
	}
	if err := u.Interrupt(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Interrupt after Close = %v; want ErrClosed", err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

func TestMemberRejectsProcessOutsideJob(t *testing.T) {
	unit, err := newUnit("membership-" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	u := unit.(*jobUnit)
	t.Cleanup(func() { _ = u.Close() })
	if id, err := jobMember(u.job, os.Getpid()); !errors.Is(err, errNoProcess) {
		t.Fatalf("outside process returned as member: %v, %v", id, err)
	}
}
