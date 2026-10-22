//go:build !unix

package cli

import "os"

// sessionSignals are the signals a Director session's supervisor handles rather than dying of.
var sessionSignals = []os.Signal{os.Interrupt}

// terminate passes nothing on: the console's interrupt has already reached the runtime.
func terminate(*os.Process, os.Signal) bool { return false }
