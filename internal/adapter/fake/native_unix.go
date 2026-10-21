//go:build unix

package fake

import (
	"math"
	"os/exec"
	"strconv"
	"time"
)

// nativeCommand is a long-running native program that knows nothing of the
// cancel marker: sleep for the lifetime.
func nativeCommand(lifetime time.Duration) *exec.Cmd {
	return exec.Command("sleep", strconv.Itoa(int(math.Ceil(lifetime.Seconds()))))
}
