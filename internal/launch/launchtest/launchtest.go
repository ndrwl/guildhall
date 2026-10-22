// Package launchtest is the test cleanup for owned attempts: the one rule a test uses to prove an attempt's
// launcher stopped before the test ends.
package launchtest

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// LockWait and StopTimeout mirror the CLI's storeWait, the wait of each of a watcher advance's two runs for the
// store's lock, and defaultStopTimeout, artisan stop's wait for the stop ladder; the cli tests pin both.
const (
	LockWait    = 30 * time.Second
	StopTimeout = time.Minute
)

// Bound is how long Stop retries: the longest a launcher outlives its released attempt, two lock waits of its
// advance and the advance's retry, plus the stop ladder's wait, scaled by testenv.Ceiling.
func Bound() time.Duration { return testenv.Ceiling(2*LockWait + StopTimeout) }

// Stopper runs artisan stop for one attempt once and returns its report, with a non-nil error when it exits
// non-zero.
type Stopper func() (launch.StopReport, error)

// Report reads one artisan stop's run: the report it printed on stdout, and err, the run's error, with its
// standard error, or the report's decoding error when the run succeeded.
func Report(stdout, stderr string, err error) (launch.StopReport, error) {
	var rep launch.StopReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil && err == nil {
		return rep, fmt.Errorf("artisan stop printed no report: %w: %s", jerr, stdout)
	}
	if err != nil {
		return rep, fmt.Errorf("%w: %s", err, stderr)
	}
	return rep, nil
}

// Stop runs stop until its report proves the attempt's launcher no longer runs, within Bound, and otherwise
// returns the last stop's error. A stop proves it when it succeeds without the endpoint's answer (no ownership
// record, or a released one whose launcher is gone and unit empty), or when the ownership is released, the
// launcher gone and the attempt exited with no containment recorded, released at setup before any worker. A stop
// the endpoint answered leaves the launcher to record and advance, so it is run again until the launcher is gone.
// A stop that fails once its report shows the launcher gone is run once more, since the launcher may have exited
// while it probed, and its error is then returned without waiting out the bound, so a surviving worker is
// reported rather than waited for.
func Stop(stop Stopper) error { return stopWithin(stop, Bound(), 50*time.Millisecond) }

func stopWithin(stop Stopper, bound, poll time.Duration) error {
	seenGone := false
	for end := time.Now().Add(bound); ; time.Sleep(poll) {
		rep, err := stop()
		gone := rep.Launcher == launch.LauncherGone || rep.Classification.Launcher == artisan.Gone.String()
		switch {
		case err == nil && rep.Endpoint != launch.EndpointStopped:
			return nil
		case rep.Launcher == launch.LauncherGone && rep.Classification.State == reconcile.Exited:
			return nil
		case err != nil && gone && seenGone:
			return fmt.Errorf("%w; endpoint %q, launcher %q, classification %+v", err, rep.Endpoint, rep.Launcher, rep.Classification)
		case time.Now().After(end):
			if err == nil {
				err = fmt.Errorf("the launcher outlived its answered stop by %v", bound)
			}
			return fmt.Errorf("%w; endpoint %q, launcher %q, classification %+v", err, rep.Endpoint, rep.Launcher, rep.Classification)
		}
		seenGone = seenGone || (err != nil && gone)
	}
}

// StopAll stops each attempt with stop and fails t for each Stop cannot prove stopped.
func StopAll(t testing.TB, keys []artisan.AttemptKey, stop func(artisan.AttemptKey) (launch.StopReport, error)) {
	t.Helper()
	stopAllWithin(t, keys, stop, Bound(), 50*time.Millisecond)
}

func stopAllWithin(t testing.TB, keys []artisan.AttemptKey, stop func(artisan.AttemptKey) (launch.StopReport, error), bound, poll time.Duration) {
	t.Helper()
	for _, k := range keys {
		if err := stopWithin(func() (launch.StopReport, error) { return stop(k) }, bound, poll); err != nil {
			t.Errorf("artisan stop %s-%d: %v", k.Step, k.Attempt, err)
		}
	}
}
