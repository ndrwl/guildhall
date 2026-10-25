package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

// recordUnit records on session 1 in the store at path a runtime unit this test holds, whose worker has exited
// unless live, and returns its record.
func recordUnit(t *testing.T, path string, live bool) store.Containment {
	t.Helper()
	u, err := artisan.NewSessionUnit(fmt.Sprintf("test-supersede-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	w := artisantest.Worker{Lifetime: time.Millisecond}
	if live {
		w = artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true, Lifetime: 10 * time.Minute}
	}
	worker, err := u.Start(artisantest.Command(t.TempDir(), w))
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		select {
		case <-u.Exited():
		case <-time.After(30 * time.Second):
			t.Fatal("the unit's worker did not exit")
		}
	}
	c := u.Containment()
	rec := store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(worker)}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.RecordDirectorRuntime(1, rec)
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

// endSession records session 1 in the store at path stopped as superseded.
func endSession(t *testing.T, path string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		d, err := s.LatestDirectorSession()
		if err != nil {
			return store.Receipt{}, err
		}
		return s.EndDirectorSession(1, store.EndSuperseded, d.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
}

// stubSweep replaces the removal sweep with fn for the test.
func stubSweep(t *testing.T, fn func(artisan.Containment, artisan.Identity) ([]artisan.Identity, error)) {
	t.Helper()
	saved := sweepRuntimeUnit
	sweepRuntimeUnit = fn
	t.Cleanup(func() { sweepRuntimeUnit = saved })
}

// TestDirectorStartRemovesASupersededUnit: over a session whose recorded runtime unit is empty, stopped (path A)
// or with its supervisor gone (path B), the start check supersedes it and removes the unit, and a repeated check
// finds it removed with no removal error. The removal is observed on a cgroup unit only.
func TestDirectorStartRemovesASupersededUnit(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		t.Run(map[bool]string{true: "stopped", false: "supervisor gone"}[stopped], func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
			path := filepath.Join(dir, "director.store")
			recordSession(t, path, true, store.DirectorRunning)
			c := recordUnit(t, path, false)
			if stopped {
				endSession(t, path)
			}
			for n := 1; n <= 2; n++ {
				sp, err := withStore(path, true, checkSupersede)
				if err != nil || sp.Number != 1 || sp.Removal != nil {
					t.Fatalf("check %d = %+v, %v; want session 1 superseded with its unit removed", n, sp, err)
				}
			}
			if c.Kind != artisan.KindCgroup {
				t.Skipf("removal observed on a cgroup unit only; this unit is %s (%s)", c.Kind, c.Reason)
			}
			if _, err := os.Stat(c.Name); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the superseded unit's cgroup %s is left: %v", c.Name, err)
			}
		})
	}
}

// TestDirectorStartWarnsOfAFailedRemoval: a removal sweep that fails is returned beside the superseded session,
// naming the unit and the error, and is never a refusal; the wrapper and the supervisor in its tab each print the
// warning, and the new session is still recorded and printed.
func TestDirectorStartWarnsOfAFailedRemoval(t *testing.T) {
	stubSweep(t, func(artisan.Containment, artisan.Identity) ([]artisan.Identity, error) {
		return nil, errors.New("stub sweep refused")
	})
	dir := tabCopy(t, "/exit")
	path := filepath.Join(dir, "director.store")
	recordSession(t, path, true, store.DirectorRunning)
	c := recordUnit(t, path, false)
	sp, err := withStore(path, true, checkSupersede)
	if err != nil || sp.Number != 1 || sp.Removal == nil || !strings.Contains(sp.Removal.Error(), unitName(c)) ||
		!strings.Contains(sp.Removal.Error(), "stub sweep refused") {
		t.Fatalf("check = %+v, %v; want session 1 superseded with a removal error naming %s and the sweep's error", sp, err, unitName(c))
	}
	warning := fmt.Sprintf("director start: warning: session 1's runtime unit %s was not removed: ", unitName(c))
	host := newDirectorTabs()
	host.onRun = func(tab adapter.TabRef, line string) {
		code, out, stderr := runDirectorCmd(t, supervisorArgv(t, dir, tab)[2:]...)
		if code != 0 {
			t.Errorf("supervisor in the tab: exit %d\n%s\n%s", code, out, stderr)
		}
		if strings.Count(stderr, warning) != 1 {
			t.Errorf("supervisor's stderr %q; want one warning %q", stderr, warning)
		}
	}
	code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
	host.runs.Wait()
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, stderr)
	}
	if strings.Count(stderr, warning) != 1 || !strings.Contains(stderr, "stub sweep refused") {
		t.Errorf("wrapper's stderr %q; want one warning %q with the sweep's error", stderr, warning)
	}
	if d := lastSession(t, out); d.Number != 2 {
		t.Errorf("printed %+v; want session 2", d)
	}
	if got := sessions(t, dir); len(got) != 2 || got[0].EndReason != store.EndSuperseded {
		t.Errorf("sessions = %+v; want session 1 superseded and session 2", got)
	}
}

// TestRemoveRuntimeReportsALeftoverCgroup: a cgroup unit whose directory is still present after a sweep that
// returned nil, as one Rmdir refused with EBUSY is, is a removal error naming it; a missing directory is removed.
func TestRemoveRuntimeReportsALeftoverCgroup(t *testing.T) {
	var swept []artisan.Containment
	stubSweep(t, func(c artisan.Containment, _ artisan.Identity) ([]artisan.Identity, error) {
		swept = append(swept, c)
		return nil, nil
	})
	left := t.TempDir()
	d := store.DirectorSession{Number: 3, Runtime: store.Containment{Kind: artisan.KindCgroup, Name: left, Worker: store.ProcessIdentity{PID: 1}}}
	if err := removeRuntime(d); err == nil || !strings.Contains(err.Error(), left) || !strings.Contains(err.Error(), "still present after removal") {
		t.Errorf("removeRuntime over a leftover cgroup = %v; want it named still present after removal", err)
	}
	d.Runtime.Name = filepath.Join(left, "removed")
	if err := removeRuntime(d); err != nil {
		t.Errorf("removeRuntime over a removed cgroup = %v; want nil", err)
	}
	if len(swept) != 2 || swept[0].Kind != artisan.KindCgroup || swept[0].Name != left {
		t.Errorf("swept %+v; want each unit swept once as recorded", swept)
	}
	if err := removeRuntime(store.DirectorSession{Number: 3}); err != nil || len(swept) != 2 {
		t.Errorf("removeRuntime with no runtime = %v after %d sweeps; want nil and no sweep", err, len(swept))
	}
}

// TestDirectorStartSweepsNothingItDoesNotSupersede: no removal sweep runs over survivors in the unit, a live
// supervisor or a sweeping stop, each still refused, nor for a superseded session with no runtime recorded; a
// superseded session's emptied unit is swept once, showing the recording sweep sees the calls.
func TestDirectorStartSweepsNothingItDoesNotSupersede(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, path string) store.Containment
		refuse error
		sweeps int
	}{
		{"emptied", func(t *testing.T, path string) store.Containment {
			recordSession(t, path, true, store.DirectorRunning)
			return recordUnit(t, path, false)
		}, nil, 1},
		{"survivors", func(t *testing.T, path string) store.Containment {
			recordSession(t, path, true, store.DirectorRunning)
			return recordUnit(t, path, true)
		}, errDirectorLive, 0},
		{"live supervisor", func(t *testing.T, path string) store.Containment {
			recordSession(t, path, false, store.DirectorRunning)
			return recordUnit(t, path, false)
		}, errDirectorLive, 0},
		{"sweeping", func(t *testing.T, path string) store.Containment {
			recordSession(t, path, true, store.DirectorRunning)
			c := recordUnit(t, path, false)
			if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
				d, err := s.LatestDirectorSession()
				if err != nil {
					return store.Receipt{}, err
				}
				return s.RequestDirectorStop(1, false, d.StateSequence)
			}); err != nil {
				t.Fatal(err)
			}
			return c
		}, errStopSweeping, 0},
		{"no runtime", func(t *testing.T, path string) store.Containment {
			recordSession(t, path, true, store.DirectorRunning)
			return store.Containment{}
		}, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			stubSweep(t, func(c artisan.Containment, w artisan.Identity) ([]artisan.Identity, error) {
				calls++
				return artisan.Sweep(c, w)
			})
			dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
			path := filepath.Join(dir, "director.store")
			c := tc.setup(t, path)
			sp, err := withStore(path, true, checkSupersede)
			switch {
			case tc.refuse != nil && !errors.Is(err, tc.refuse):
				t.Errorf("check = %+v, %v; want %v", sp, err, tc.refuse)
			case tc.refuse == nil && (err != nil || sp.Number != 1 || sp.Removal != nil):
				t.Errorf("check = %+v, %v; want session 1 superseded", sp, err)
			}
			if calls != tc.sweeps {
				t.Errorf("the removal sweep ran %d times, want %d", calls, tc.sweeps)
			}
			if tc.sweeps == 0 && c.Kind == artisan.KindCgroup {
				if _, err := os.Stat(c.Name); err != nil {
					t.Errorf("the unit's cgroup %s: %v; want it left", c.Name, err)
				}
			}
		})
	}
}
