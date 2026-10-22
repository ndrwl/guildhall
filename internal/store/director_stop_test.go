package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestRequestDirectorStopMoves(t *testing.T) {
	cases := []struct {
		name  string
		path  []DirectorState
		first bool // a plain stop before the one tested
		now   bool
		ok    bool
	}{
		{"recovering", nil, false, false, true},
		{"running", []DirectorState{DirectorRunning}, false, false, true},
		{"paused", []DirectorState{DirectorRunning, DirectorPaused}, false, true, true},
		{"stopping takes now", []DirectorState{DirectorRunning}, true, true, true},
		{"stopping stays", []DirectorState{DirectorRunning}, true, false, false},
		{"stopped", []DirectorState{DirectorStopped}, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustStartSession(t, s, sampleSession(1))
			for _, st := range c.path {
				mustMove(t, s, 1, st)
			}
			if c.first {
				d, _ := s.DirectorSession(1)
				if _, err := s.RequestDirectorStop(1, false, d.StateSequence); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := s.DirectorSession(1)
			receipts := countReceipts(t, s)
			r, err := s.RequestDirectorStop(1, c.now, before.StateSequence)
			if c.ok != (err == nil) {
				t.Fatalf("stop: err = %v, want ok %v", err, c.ok)
			}
			after, _ := s.DirectorSession(1)
			if !c.ok {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("refused stop err = %v, want ErrInvalid", err)
				}
				if after != before || countReceipts(t, s) != receipts {
					t.Fatalf("refused stop changed the session: %+v -> %+v", before, after)
				}
				return
			}
			if after.State != DirectorStopping || after.StopNow != c.now || after.StateSequence != r.Sequence || after.EndedAt != before.EndedAt {
				t.Fatalf("session after stop = %+v", after)
			}
			mode := map[bool]string{false: "turn", true: "now"}[c.now]
			if want := "director-stop:1:" + mode + ":" + itoa(before.StateSequence); r.MutationID != want || r.Operation != "request-director-stop" || r.Record != "director/session/1" {
				t.Fatalf("receipt = %+v, want mutation id %s", r, want)
			}
			if again, err := s.RequestDirectorStop(1, c.now, before.StateSequence); err != nil || again != r {
				t.Fatalf("replay = %+v, %v; want the original receipt", again, err)
			}
		})
	}
}

func TestRequestDirectorStopRefusesStaleAndSuperseded(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	r := mustMove(t, s, 1, DirectorRunning)
	mustMove(t, s, 1, DirectorPaused)
	if _, err := s.RequestDirectorStop(1, false, r.Sequence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale stop = %v, want ErrInvalid", err)
	}
	first, _ := s.DirectorSession(1)
	mustStartSession(t, s, sampleSession(2))
	if _, err := s.RequestDirectorStop(1, true, first.StateSequence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stop of a superseded session = %v, want ErrInvalid", err)
	}
	if _, err := s.RequestDirectorStop(3, true, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop of a missing session = %v, want ErrNotFound", err)
	}
}

func TestEndDirectorSessionRecordsTheReason(t *testing.T) {
	for _, path := range [][]DirectorState{nil, {DirectorRunning}, {DirectorRunning, DirectorPaused}, {DirectorStopping}} {
		s, reopen := newStore(t)
		mustStartSession(t, s, sampleSession(1))
		for _, st := range path {
			mustMove(t, s, 1, st)
		}
		before, _ := s.DirectorSession(1)
		r, err := s.EndDirectorSession(1, "superseded", before.StateSequence)
		if err != nil {
			t.Fatalf("after %v: %v", path, err)
		}
		if want := "director-end:1:" + itoa(before.StateSequence); r.MutationID != want || r.Operation != "end-director-session" || r.Record != "director/session/1" {
			t.Fatalf("receipt = %+v, want mutation id %s", r, want)
		}
		if again, err := s.EndDirectorSession(1, "superseded", before.StateSequence); err != nil || again != r {
			t.Fatalf("replay = %+v, %v; want the original receipt", again, err)
		}
		_ = s.Close()
		s = openTest(t, reopen)
		d, _ := s.DirectorSession(1)
		if d.State != DirectorStopped || d.EndReason != "superseded" || d.EndedAt != testTime || d.StateSequence != r.Sequence {
			t.Fatalf("after %v: session = %+v", path, d)
		}
	}
}

func TestEndDirectorSessionRefusals(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	d, _ := s.DirectorSession(1)
	receipts := countReceipts(t, s)
	for name, reason := range map[string]string{"blank": " ", "two lines": "a\nb", "long": strings.Repeat("x", MaxEndReasonBytes+1)} {
		if _, err := s.EndDirectorSession(1, reason, d.StateSequence); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s reason: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.EndDirectorSession(1, "superseded", d.StateSequence+7); !errors.Is(err, ErrInvalid) {
		t.Errorf("stale end: err = %v, want ErrInvalid", err)
	}
	if after, _ := s.DirectorSession(1); after != d || countReceipts(t, s) != receipts {
		t.Fatalf("refused ends changed the session: %+v -> %+v", d, after)
	}
	mustMove(t, s, 1, DirectorStopped)
	stopped, _ := s.DirectorSession(1)
	if _, err := s.EndDirectorSession(1, "superseded", stopped.StateSequence); !errors.Is(err, ErrInvalid) {
		t.Errorf("end of a stopped session: err = %v, want ErrInvalid", err)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
