package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFinishTimesAreTheFirstFinishReceipts(t *testing.T) {
	opts := testOptions()
	opts.Now = steppingClock()
	s, err := Open(filepath.Join(t.TempDir(), "guildhall.store"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	mustCreate(t, s, "m-3", sampleTask("lib", "t-1"))
	run := func(id string, f func() (Receipt, error)) Receipt {
		t.Helper()
		r, err := f()
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return r
	}
	run("m-4", func() (Receipt, error) { return s.StartAttempt("m-4", "app", "t-1", "build") })
	built := run("m-5", func() (Receipt, error) { return s.FinishAttempt("m-5", "app", "t-1", "build", 1, AttemptSucceeded) })
	// A replay under another mutation id and a later intake rejection keep the first finish.
	run("m-6", func() (Receipt, error) { return s.FinishAttempt("m-6", "app", "t-1", "build", 1, AttemptSucceeded) })
	run("m-7", func() (Receipt, error) { return s.RejectAttempt("m-7", "app", "t-1", "build", 1, "no head") })
	run("m-8", func() (Receipt, error) { return s.StartAttempt("m-8", "app", "t-1", "build") })
	run("m-9", func() (Receipt, error) { return s.StartAttempt("m-9", "app", "t-2", "plan") })
	planned := run("m-10", func() (Receipt, error) { return s.FinishAttempt("m-10", "app", "t-2", "plan", 1, AttemptStopped) })
	run("m-11", func() (Receipt, error) { return s.StartAttempt("m-11", "lib", "t-1", "build") })
	run("m-12", func() (Receipt, error) { return s.FinishAttempt("m-12", "lib", "t-1", "build", 1, AttemptFailed) })

	got, err := s.FinishTimes("app")
	if err != nil {
		t.Fatal(err)
	}
	want := map[AttemptName]time.Time{
		{TaskID: "t-1", Step: "build", Number: 1}: built.CommittedAt,
		{TaskID: "t-2", Step: "plan", Number: 1}:  planned.CommittedAt,
	}
	if len(got) != len(want) {
		t.Fatalf("FinishTimes = %v, want %v", got, want)
	}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("FinishTimes[%+v] = %v, want %v", k, got[k], v)
		}
	}
	if none, err := s.FinishTimes("other"); err != nil || len(none) != 0 {
		t.Errorf("FinishTimes of an unknown repository = %v, %v", none, err)
	}
}

func TestFinishTimesOfLegacyReceipts(t *testing.T) {
	ro := testOptions()
	ro.ReadOnly = true
	s, err := Open(fixture, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	attempts, err := s.Attempts("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.FinishTimes("app")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		at, ok := got[AttemptName{TaskID: a.TaskID, Step: a.Step, Number: a.Number}]
		if (a.Outcome != "") != ok || ok && at.IsZero() {
			t.Errorf("legacy attempt %+v finish time = %v, %v", a, at, ok)
		}
	}
}
