//go:build !windows

package artisantest

import "testing"

// runPlatformStopCases runs the stop-contract cases of this platform: none
// outside Windows, where a rename never races an open.
func runPlatformStopCases(*testing.T, Harness) {}
