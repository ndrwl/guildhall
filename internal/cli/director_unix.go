//go:build unix

package cli

import (
	"os"
	"syscall"
)

// sessionSignals are the signals a Director session's supervisor handles rather than dying of.
var sessionSignals = []os.Signal{os.Interrupt, syscall.SIGHUP, syscall.SIGTERM}

// terminate passes a hangup or termination s on to the runtime's unit u as a termination and reports whether it
// did; an interrupt has already reached the runtime from the terminal.
func terminate(u runtimeUnit, s os.Signal) bool {
	if s == os.Interrupt {
		return false
	}
	_ = u.Terminate()
	return true
}

// endRuntime asks every process in the runtime's unit u to end with a termination, as a closing terminal would.
func endRuntime(u runtimeUnit) { _ = u.Terminate() }
