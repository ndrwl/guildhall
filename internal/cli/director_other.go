//go:build !unix

package cli

import "os"

// sessionSignals are the signals a Director session's supervisor handles rather than dying of.
var sessionSignals = []os.Signal{os.Interrupt}

// terminate passes nothing on: the console's interrupt has already reached the runtime.
func terminate(runtimeUnit, os.Signal) bool { return false }

// endRuntime ends every process in the runtime's unit u: Windows has no termination signal to ask them with.
func endRuntime(u runtimeUnit) { _ = u.Kill() }
