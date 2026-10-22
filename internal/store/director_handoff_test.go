package store

import (
	"errors"
	"strings"
	"testing"
)

const noteDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestHandoffNoteIsRecordedAndReadBack(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.LatestHandoffNote(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest note of an empty store = %v, want ErrNotFound", err)
	}
	mustStartSession(t, s, sampleSession(1))
	var wrote []string
	write := func(p string) error { wrote = append(wrote, p); return nil }
	n, r, err := s.RecordHandoffNote(1, noteDigest, write)
	if err != nil {
		t.Fatal(err)
	}
	if want := HandoffPath(r.Sequence); n.Session != 1 || n.Sequence != r.Sequence || n.Path != want || n.SHA256 != noteDigest || n.RecordedAt.IsZero() {
		t.Fatalf("note = %+v, want session 1 at %s", n, want)
	}
	if want := "director/handoff-" + itoa(r.Sequence) + ".md"; n.Path != want || len(wrote) != 1 || wrote[0] != want {
		t.Fatalf("path %q, wrote %q; want %q written once", n.Path, wrote, want)
	}
	if r.MutationID != "handoff:1:"+noteDigest+":0" || r.Operation != "record-handoff-note" || r.Record != "director/handoff/"+itoa(r.Sequence) {
		t.Fatalf("receipt = %+v", r)
	}
	again, ar, err := s.RecordHandoffNote(1, noteDigest, write)
	if err != nil || again != n || ar != r || len(wrote) != 1 {
		t.Fatalf("replay = %+v, %+v, %v, wrote %d; want the original without writing", again, ar, err, len(wrote))
	}
	mustStartSession(t, s, sampleSession(2))
	other := strings.Repeat("e", 64)
	second, _, err := s.RecordHandoffNote(2, other, write)
	if err != nil || second.Sequence <= n.Sequence {
		t.Fatalf("second note = %+v, %v", second, err)
	}
	_ = s.Close()
	s = openTest(t, path)
	if got, err := s.LatestHandoffNote(); err != nil || got != second {
		t.Fatalf("latest after reopen = %+v, %v; want %+v", got, err, second)
	}
	if v, err := s.DirectorView(); err != nil || v.Handoff == nil || *v.Handoff != second || v.Reload != nil {
		t.Fatalf("DirectorView handoff %+v, reload %+v, %v; want the second note and no reload", v.Handoff, v.Reload, err)
	}
}

// TestHandoffNoteRecordedAgainAfterAnotherIsTheNewest: notes A, B, then A again in one session record A anew,
// with a new sequence and path, as the newest note.
func TestHandoffNoteRecordedAgainAfterAnotherIsTheNewest(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	var wrote []string
	write := func(p string) error { wrote = append(wrote, p); return nil }
	a, _, err := s.RecordHandoffNote(1, noteDigest, write)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.RecordHandoffNote(1, strings.Repeat("e", 64), write)
	if err != nil {
		t.Fatal(err)
	}
	again, r, err := s.RecordHandoffNote(1, noteDigest, write)
	if err != nil || again.Sequence <= b.Sequence || again.Path == a.Path || again.SHA256 != noteDigest || len(wrote) != 3 {
		t.Fatalf("A again = %+v, %v, wrote %q; want A recorded anew after B", again, err, wrote)
	}
	if r.MutationID != "handoff:1:"+noteDigest+":"+itoa(b.Sequence) {
		t.Fatalf("mutation id = %q", r.MutationID)
	}
	if got, err := s.LatestHandoffNote(); err != nil || got != again {
		t.Fatalf("latest = %+v, %v; want A recorded again", got, err)
	}
}

func TestRecordHandoffNoteRefusals(t *testing.T) {
	s, _ := newStore(t)
	never := func(string) error { t.Fatal("write called for a refused note"); return nil }
	if _, _, err := s.RecordHandoffNote(1, noteDigest, never); !errors.Is(err, ErrNotFound) {
		t.Fatalf("note without a session = %v, want ErrNotFound", err)
	}
	mustStartSession(t, s, sampleSession(1))
	mustStartSession(t, s, sampleSession(2))
	receipts := countReceipts(t, s)
	for name, c := range map[string]struct {
		n      int
		digest string
	}{
		"superseded session": {1, noteDigest},
		"zero session":       {0, noteDigest},
		"short digest":       {2, "abc"},
		"uppercase digest":   {2, strings.ToUpper(noteDigest)},
	} {
		if _, _, err := s.RecordHandoffNote(c.n, c.digest, never); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	failed := errors.New("disk full")
	if _, _, err := s.RecordHandoffNote(2, noteDigest, func(string) error { return failed }); !errors.Is(err, failed) {
		t.Errorf("failed write: err = %v, want it returned", err)
	}
	mustMove(t, s, 2, DirectorStopped)
	receipts++
	if _, _, err := s.RecordHandoffNote(2, noteDigest, never); !errors.Is(err, ErrInvalid) {
		t.Errorf("stopped session: err = %v, want ErrInvalid", err)
	}
	if got := countReceipts(t, s); got != receipts {
		t.Errorf("receipts = %d, want %d: a refused note recorded something", got, receipts)
	}
	if _, err := s.LatestHandoffNote(); !errors.Is(err, ErrNotFound) {
		t.Errorf("latest note after refusals = %v, want ErrNotFound", err)
	}
}

func TestDirectorReloadRequestsCollapseUntilApplied(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.LatestDirectorReload(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest reload of an empty store = %v, want ErrNotFound", err)
	}
	first, err := s.RequestDirectorReload(false)
	if err != nil || first.State != ReloadRequested || first.Now || first.Sequence == 0 || first.RequestedAt.IsZero() {
		t.Fatalf("first request = %+v, %v", first, err)
	}
	receipts := countReceipts(t, s)
	if again, err := s.RequestDirectorReload(false); err != nil || again != first || countReceipts(t, s) != receipts {
		t.Fatalf("repeated plain request = %+v, %v; want the first, recording nothing", again, err)
	}
	now, err := s.RequestDirectorReload(true)
	if err != nil || !now.Now || now.Sequence != first.Sequence || now.State != ReloadRequested {
		t.Fatalf("--now over a plain request = %+v, %v; want it upgraded in place", now, err)
	}
	receipts = countReceipts(t, s)
	for _, n := range []bool{true, false} {
		if again, err := s.RequestDirectorReload(n); err != nil || again != now || countReceipts(t, s) != receipts {
			t.Fatalf("request (now %v) after --now = %+v, %v; want the pending one unchanged", n, again, err)
		}
	}
	_ = s.Close()
	s = openTest(t, path)
	if got, err := s.LatestDirectorReload(); err != nil || got != now {
		t.Fatalf("latest after reopen = %+v, %v; want %+v", got, err, now)
	}
	applied, err := s.ApplyDirectorReload(now.Sequence, 3, 4)
	if err != nil || applied.State != ReloadApplied || applied.OldSession != 3 || applied.NewSession != 4 || applied.AppliedAt.IsZero() || applied.Sequence != now.Sequence || !applied.Now {
		t.Fatalf("applied = %+v, %v", applied, err)
	}
	if again, err := s.ApplyDirectorReload(now.Sequence, 3, 4); err != nil || again != applied {
		t.Fatalf("replayed apply = %+v, %v; want the applied reload", again, err)
	}
	if got, err := s.LatestDirectorReload(); err != nil || got != applied {
		t.Fatalf("latest after apply = %+v, %v; want the applied reload", got, err)
	}
	next, err := s.RequestDirectorReload(false)
	if err != nil || next.Sequence <= applied.Sequence || next.State != ReloadRequested || next.Now {
		t.Fatalf("request after apply = %+v, %v; want a new request", next, err)
	}
	if got, err := s.LatestDirectorReload(); err != nil || got != next {
		t.Fatalf("latest after a new request = %+v, %v; want it", got, err)
	}
	if v, err := s.DirectorView(); err != nil || v.Reload == nil || *v.Reload != next || v.Handoff != nil {
		t.Fatalf("DirectorView reload %+v, handoff %+v, %v; want the new request and no note", v.Reload, v.Handoff, err)
	}
}

func TestApplyDirectorReloadRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.ApplyDirectorReload(1, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("apply of no reload = %v, want ErrNotFound", err)
	}
	r, err := s.RequestDirectorReload(false)
	if err != nil {
		t.Fatal(err)
	}
	receipts := countReceipts(t, s)
	for name, c := range map[string][2]int{"no new session": {0, 0}, "old not below new": {2, 2}} {
		if _, err := s.ApplyDirectorReload(r.Sequence, c[0], c[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if countReceipts(t, s) != receipts {
		t.Fatal("a refused apply recorded something")
	}
	if _, err := s.ApplyDirectorReload(r.Sequence, 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyDirectorReload(r.Sequence, 1, 2); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("second apply with other sessions = %v, want ErrMutationConflict", err)
	}
}
