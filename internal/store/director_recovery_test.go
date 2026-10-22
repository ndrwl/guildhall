package store

import (
	"errors"
	"testing"
)

func TestRequestDirectorStopRecoversStoppedRuntime(t *testing.T) {
	for _, now := range []bool{false, true} {
		s, _ := newStore(t)
		mustStartSession(t, s, sampleSession(1))
		if _, err := s.RecordDirectorRuntime(1, sampleRuntime()); err != nil {
			t.Fatal(err)
		}
		mustMove(t, s, 1, DirectorStopped)
		before, _ := s.DirectorSession(1)
		r, err := s.RequestDirectorStop(1, now, before.StateSequence)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := s.DirectorSession(1)
		expected := before
		expected.Sweeping, expected.StateSequence = true, r.Sequence
		if after != expected {
			t.Fatalf("recovery changed end metadata: %+v; want %+v", after, expected)
		}
		if replay, err := s.RequestDirectorStop(1, now, before.StateSequence); err != nil || replay != r {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		if _, err := s.RequestDirectorStop(1, now, after.StateSequence); !errors.Is(err, ErrInvalid) {
			t.Fatalf("duplicate request = %v", err)
		}
		if _, err := s.SetDirectorState(1, DirectorRunning, after.StateSequence); !errors.Is(err, ErrInvalid) {
			t.Fatalf("revived stopped session: %v", err)
		}
		if _, err := s.FinishDirectorStop(1, after.StateSequence); err != nil {
			t.Fatal(err)
		}
		finished, _ := s.DirectorSession(1)
		if finished.Sweeping || finished.State != DirectorStopped || finished.EndedAt != before.EndedAt {
			t.Fatalf("finish = %+v", finished)
		}
	}
}
