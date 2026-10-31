//go:build !unix

package steward

import "os/exec"

// configureCancellation leaves cancelling cmd, an scp or ssh run, to CommandContext, which kills the direct
// process; the caller's WaitDelay also bounds pipes a descendant inherited.
func configureCancellation(cmd *exec.Cmd) {}
