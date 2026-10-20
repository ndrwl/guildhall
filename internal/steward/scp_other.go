//go:build !unix

package steward

import "os/exec"

// CommandContext kills the direct process; WaitDelay also bounds inherited pipes.
func configureSCPCancellation(cmd *exec.Cmd) {}
