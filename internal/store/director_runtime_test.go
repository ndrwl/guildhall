package store

import (
	"errors"
	"testing"
)

// sampleRuntime is a runtime unit as a Linux supervisor records it.
func sampleRuntime() Containment {
	return Containment{Kind: ContainmentCgroup, Name: "/sys/fs/cgroup/user.slice/guildhall-director-9-session-1",
		Reason: "delegated cgroup v2", Worker: ProcessIdentity{PID: 4343, Token: "boot-1:78"}}
}

// TestDirectorRuntimeIsRecordedOnTheSession: the runtime's unit is recorded on the newest session under
// director-runtime:<n>, survives a restart, leaves the state sequence alone, so a move read before it still applies,
// and replays; a different unit for the same session conflicts.
func TestDirectorRuntimeIsRecordedOnTheSession(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	before, err := s.DirectorSession(1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RecordDirectorRuntime(1, sampleRuntime())
	if err != nil {
		t.Fatalf("RecordDirectorRuntime: %v", err)
	}
	if r.MutationID != "director-runtime:1" || r.Operation != "record-director-runtime" || r.Record != "director/session/1" {
		t.Errorf("receipt = %+v", r)
	}
	_ = s.Close()

	s = openTest(t, path)
	got, err := s.DirectorSession(1)
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Runtime = sampleRuntime()
	if got != want {
		t.Fatalf("session = %+v, want %+v", got, want)
	}
	if again, err := s.RecordDirectorRuntime(1, sampleRuntime()); err != nil || again != r {
		t.Errorf("replay = %+v, %v; want the original receipt", again, err)
	}
	other := sampleRuntime()
	other.Worker.PID++
	if _, err := s.RecordDirectorRuntime(1, other); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("a different unit = %v, want ErrMutationConflict", err)
	}
	if _, err := s.SetDirectorState(1, DirectorRunning, before.StateSequence); err != nil {
		t.Errorf("a move read before the record = %v, want it applied", err)
	}
}

// TestDirectorRuntimeRecordRefusals: a missing, superseded or stopped session, an unrecoverable unit and one without
// its worker are refused, recording nothing; a new session given a runtime is refused.
func TestDirectorRuntimeRecordRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.RecordDirectorRuntime(1, sampleRuntime()); !errors.Is(err, ErrNotFound) {
		t.Errorf("no session = %v, want ErrNotFound", err)
	}
	given := sampleSession(1)
	given.Runtime = sampleRuntime()
	if _, err := s.StartDirectorSession(given); !errors.Is(err, ErrInvalid) {
		t.Errorf("a new session given a runtime = %v, want ErrInvalid", err)
	}
	mustStartSession(t, s, sampleSession(1))
	for name, c := range map[string]Containment{
		"no kind":       {Worker: sampleRuntime().Worker},
		"unknown kind":  {Kind: "chroot", Name: "x", Worker: sampleRuntime().Worker},
		"unnamed job":   {Kind: ContainmentJob, Worker: sampleRuntime().Worker},
		"no worker":     {Kind: ContainmentCgroup, Name: "/sys/fs/cgroup/x"},
		"group no pid":  {Kind: ContainmentSubreaper},
		"named a group": {Kind: ContainmentGroup, Name: "x", Worker: sampleRuntime().Worker},
	} {
		if _, err := s.RecordDirectorRuntime(1, c); err == nil {
			t.Errorf("%s: recorded, want refused", name)
		}
	}
	mustStartSession(t, s, sampleSession(2))
	if _, err := s.RecordDirectorRuntime(1, sampleRuntime()); !errors.Is(err, ErrInvalid) {
		t.Errorf("a superseded session = %v, want ErrInvalid", err)
	}
	mustMove(t, s, 2, DirectorStopped)
	if _, err := s.RecordDirectorRuntime(2, sampleRuntime()); !errors.Is(err, ErrInvalid) {
		t.Errorf("a stopped session = %v, want ErrInvalid", err)
	}
	for _, n := range []int{1, 2} {
		if d, err := s.DirectorSession(n); err != nil || d.Runtime != (Containment{}) {
			t.Errorf("session %d = %+v, %v; want no runtime recorded", n, d, err)
		}
	}
}
