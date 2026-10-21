//go:build !windows

package artisantest

import "testing"

// runPlatformStopCases adds no cases outside Windows: the sharing violations
// exercised by the Windows cases are specific to that platform.
func runPlatformStopCases(*testing.T, Harness) {}
