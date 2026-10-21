package fake

import (
	"math"
	"os/exec"
	"strconv"
	"time"
)

// nativeCommand is a long-running native program that knows nothing of the
// cancel marker: ping the loopback address once a second for the lifetime.
func nativeCommand(lifetime time.Duration) *exec.Cmd {
	return exec.Command("ping", "-n", strconv.Itoa(int(math.Ceil(lifetime.Seconds()))+1), "127.0.0.1")
}
