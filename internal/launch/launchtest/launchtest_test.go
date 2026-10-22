package launchtest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
)

// script is a Stopper that answers its reports in turn, repeating the last.
type script struct {
	reports []launch.StopReport
	errs    []error
	calls   int
}

func (s *script) stop() (launch.StopReport, error) {
	i := min(s.calls, len(s.reports)-1)
	s.calls++
	return s.reports[i], s.errs[i]
}

var (
	errNotStopped = errors.New("exit 1: attempt is not proven stopped")
	released      = "not asked: ownership released"
	answered      = launch.StopReport{Endpoint: launch.EndpointStopped, Classification: reconcile.Classification{State: reconcile.Running}}
	advancing     = launch.StopReport{Endpoint: released, Launcher: launch.LauncherAdvancing, Classification: reconcile.Classification{State: reconcile.Finished}}
	goneEmpty     = launch.StopReport{Endpoint: released, Launcher: launch.LauncherGone, Classification: reconcile.Classification{State: reconcile.Finished}}
)

func TestStopRule(t *testing.T) {
	cases := []struct {
		name    string
		reports []launch.StopReport
		errs    []error
		calls   int
		proven  bool
	}{
		{name: "no ownership record", reports: []launch.StopReport{{Endpoint: "not asked: no ownership record"}}, errs: []error{nil}, calls: 1, proven: true},
		{name: "a launcher gone after its endpoint failed", reports: []launch.StopReport{{Endpoint: "no stop: EOF"}}, errs: []error{nil}, calls: 1, proven: true},
		{name: "an answered stop waits out the advance", reports: []launch.StopReport{answered, advancing, advancing, advancing, goneEmpty},
			errs: []error{nil, errNotStopped, errNotStopped, errNotStopped, nil}, calls: 5, proven: true},
		{name: "a launcher seen gone is stopped once more", reports: []launch.StopReport{advancing, goneEmpty}, errs: []error{errNotStopped, nil}, calls: 2, proven: true},
		{name: "released at setup with no containment", reports: []launch.StopReport{advancing,
			{Endpoint: released, Launcher: launch.LauncherGone, Classification: reconcile.Classification{State: reconcile.Exited}}},
			errs: []error{errNotStopped, errNotStopped}, calls: 2, proven: true},
		{name: "a gone launcher whose unit is not proven empty", reports: []launch.StopReport{advancing,
			{Endpoint: released, Launcher: launch.LauncherGone, Classification: reconcile.Classification{State: reconcile.Finished}}},
			errs: []error{errNotStopped, errNotStopped}, calls: 3},
		{name: "a killed launcher whose worker survives", reports: []launch.StopReport{
			{Endpoint: "no stop: connection refused", Classification: reconcile.Classification{State: reconcile.Unknown, Launcher: "gone"}}},
			errs: []error{errNotStopped}, calls: 2},
		{name: "a launcher that keeps advancing", reports: []launch.StopReport{advancing}, errs: []error{errNotStopped}},
		{name: "an unverified launcher", reports: []launch.StopReport{{Endpoint: released, Launcher: launch.LauncherUnverified}}, errs: []error{errNotStopped}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &script{reports: c.reports, errs: c.errs}
			err := stopWithin(s.stop, 300*time.Millisecond, time.Millisecond)
			switch {
			case c.proven && err != nil:
				t.Fatalf("Stop = %v, want proven", err)
			case c.proven && s.calls != c.calls:
				t.Errorf("Stop ran %d stops, want %d", s.calls, c.calls)
			case !c.proven && (err == nil || !strings.Contains(err.Error(), "not proven stopped")):
				t.Fatalf("Stop = %v, want the last stop's error", err)
			case !c.proven && c.calls != 0 && s.calls != c.calls:
				t.Errorf("Stop gave up after %d stops, want %d: once more after the launcher is seen gone", s.calls, c.calls)
			case !c.proven && c.calls == 0 && s.calls < 2:
				t.Errorf("Stop gave up after %d stops, want it retried until its bound", s.calls)
			}
		})
	}
}

// TestBoundIsTheWatchersWorstCase: the bound covers two lock waits and the stop ladder's wait.
func TestBoundIsTheWatchersWorstCase(t *testing.T) {
	t.Setenv("CI", "")
	if got, want := Bound(), 2*LockWait+StopTimeout; got != want {
		t.Errorf("Bound() = %v, want %v", got, want)
	}
}

// fakeT records the errors StopAll reports.
type fakeT struct {
	testing.TB
	errors []string
}

func (f *fakeT) Helper() {}

func (f *fakeT) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func TestStopAllFailsForEachUnproven(t *testing.T) {
	ft := &fakeT{TB: t}
	keys := []artisan.AttemptKey{{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}, {Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}}
	stopAllWithin(ft, keys, func(k artisan.AttemptKey) (launch.StopReport, error) {
		if k.Step == "plan" {
			return launch.StopReport{Endpoint: "not asked: no ownership record"}, nil
		}
		return advancing, errNotStopped
	}, 50*time.Millisecond, time.Millisecond)
	if len(ft.errors) != 1 || !strings.Contains(ft.errors[0], "build-1") {
		t.Errorf("StopAll errors = %q, want one naming build-1", ft.errors)
	}
}
