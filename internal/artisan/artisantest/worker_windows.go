package artisantest

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	setsidAvailable    = false
	breakawayAvailable = true
)

func applySetsid(*exec.Cmd) {}

func applyBreakaway(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
}
