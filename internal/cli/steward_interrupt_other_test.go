//go:build !unix

package cli

// runStewardHangStub has no hanging stub off unix, where the interrupt tests do not run.
func runStewardHangStub() {}
