package store

import (
	"errors"
	"testing"
)

func TestRejectionReleasesOwnershipAtomically(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	testHookBeforeCommit = func() error { return errors.New("interrupted commit") }
	_, err := s.RejectAttempt("reject", "app", "t-1", "build", 1, "invalid head")
	testHookBeforeCommit = nil
	if err == nil {
		t.Fatal("expected rollback")
	}
	if a, _ := s.Attempts("app", "t-1"); a[0].Outcome != "" || a[0].IntakeRejection != "" {
		t.Fatalf("partial rejection: %+v", a)
	}
	if got := mustOwnership(t, s, 1); got.State == OwnerReleased {
		t.Fatal("ownership released before rejection committed")
	}
	if _, err := s.RejectAttempt("reject", "app", "t-1", "build", 1, "invalid head"); err != nil {
		t.Fatal(err)
	}
	if got := mustOwnership(t, s, 1); got.State != OwnerReleased {
		t.Fatalf("ownership=%+v", got)
	}
}

func TestRejectionPreservesLauncherHistory(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "task", sampleTask("app", "t-1"))
	if _, err := s.StartAttempt("start", "app", "t-1", "review"); err != nil {
		t.Fatal(err)
	}
	finish, err := s.FinishAttempt("launcher", "app", "t-1", "review", 1, AttemptSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := s.RejectAttempt("intake", "app", "t-1", "review", 1, "no output for port outcome")
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.TaskHistory("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Receipts) != 4 || history.Receipts[2] != finish || history.Receipts[3] != rejected {
		t.Fatalf("history=%+v", history)
	}
	if got, err := s.Receipt("launcher"); err != nil || got != finish {
		t.Fatalf("launcher receipt rewritten: %+v, %v", got, err)
	}
	// Even a later launcher replay cannot undo the intake rejection.
	if _, err := s.FinishAttempt("launcher", "app", "t-1", "review", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	a, err := s.Attempts("app", "t-1")
	if err != nil || a[0].Outcome != AttemptFailed || a[0].IntakeRejection == "" {
		t.Fatalf("attempts=%+v, %v", a, err)
	}
}
