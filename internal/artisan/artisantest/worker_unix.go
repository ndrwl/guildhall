//go:build unix

package artisantest

import (
	"os/exec"
	"syscall"
)

const (
	setsidAvailable    = true
	breakawayAvailable = false
)

func applySetsid(c *exec.Cmd)  { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func applyBreakaway(*exec.Cmd) {}
