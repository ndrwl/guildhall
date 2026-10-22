package testenv

import (
	"os"
	"time"
)

// CIEnv names the environment variable CI services set on their runners;
// GitHub Actions sets it to "true".
const CIEnv = "CI"

// CISlowdown is how many times slower than a developer's machine a CI runner
// may run: hosted runners have been measured up to ten times slower than
// their usual pace, and a usual Windows run is several times slower than a
// Linux one, so a ceiling tuned on one host is multiplied by this on a runner.
const CISlowdown = 5

// CIMaxCeiling is the most a scaled ceiling grows to. go test stops a package
// after ten minutes and loses every message of the test it stops, so a wait
// scaled past a few minutes, or a test with several such waits, would turn a
// failure into that timeout; a ceiling already at or above it stays unscaled.
const CIMaxCeiling = 2 * time.Minute

// Ceiling returns d, the longest a test waits for an event before it fails,
// scaled for the host: d times CISlowdown, but no more than the larger of d
// and CIMaxCeiling, when CIEnv is set to anything but empty, and d unchanged
// otherwise. A wait that ends on the event costs nothing extra, so a slow
// runner makes a test slow rather than red, while a local run still fails
// promptly. Use it only for ceilings, never for durations the code under test
// is meant to observe.
func Ceiling(d time.Duration) time.Duration {
	if os.Getenv(CIEnv) == "" {
		return d
	}
	return min(d*CISlowdown, max(d, CIMaxCeiling))
}
