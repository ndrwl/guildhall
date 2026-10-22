package artisan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
	"golang.org/x/sys/unix"
)

func TestFailedExecRestoresForeground(t *testing.T) {
	if os.Getenv("ARTISAN_FOREGROUND_TEST") == "1" {
		want := unix.Getpgrp()
		if got, err := unix.IoctlGetInt(0, unix.TIOCGPGRP); err != nil || got != want {
			t.Fatalf("initial foreground = %d, %v; want %d", got, err, want)
		}
		unit, err := newUnit(t.Name())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unit.Close() }()
		cmd := exec.Command(filepath.Join(t.TempDir(), "missing-worker"))
		cmd.Stdin = os.Stdin
		if _, err := unit.Start(cmd); err == nil {
			t.Fatal("Start of missing executable succeeded")
		}
		if !cmd.SysProcAttr.Foreground {
			t.Fatal("foreground handover was not requested")
		}
		if got, err := unix.IoctlGetInt(0, unix.TIOCGPGRP); err != nil || got != want {
			t.Fatalf("foreground after failed exec = %d, %v; want %d", got, err, want)
		}
		return
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFailedExecRestoresForeground$")
	cmd.Env = append(os.Environ(), "ARTISAN_FOREGROUND_TEST=1")
	cmd.Stdin = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("controlling-terminal helper: %v\n%s", err, output)
	}
}
