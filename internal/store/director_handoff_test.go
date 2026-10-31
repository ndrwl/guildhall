package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestRetiredHandoffNotesStillDecode: a store holding a handoff note from before notes replaced them opens, and
// doctor decodes the note and reports nothing (docs/decisions/director-reload-and-notes.md).
func TestRetiredHandoffNotesStillDecode(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	_ = s.Close()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	note, err := json.Marshal(HandoffNote{Session: 1, Sequence: 645, Path: "director/handoff-645.md", SHA256: strings.Repeat("0", 64), RecordedAt: testTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket([]byte("director")).CreateBucketIfNotExists([]byte("handoffs"))
		if err != nil {
			return err
		}
		return b.Put(seqKey(645), note)
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s = openTest(t, path)
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v; want a clean store", rep, err)
	}
	if live, err := s.LiveNotes(); err != nil || len(live) != 0 {
		t.Fatalf("live notes = %+v, %v; want none", live, err)
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
	applied, err := s.ApplyDirectorReload(now.Sequence, 3, 4, ReloadRefusal{})
	if err != nil || applied.State != ReloadApplied || applied.OldSession != 3 || applied.NewSession != 4 || applied.AppliedAt.IsZero() || applied.Sequence != now.Sequence || !applied.Now {
		t.Fatalf("applied = %+v, %v", applied, err)
	}
	if again, err := s.ApplyDirectorReload(now.Sequence, 3, 4, ReloadRefusal{}); err != nil || again != applied {
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
	if v, err := s.DirectorView(); err != nil || v.Reload == nil || *v.Reload != next {
		t.Fatalf("DirectorView reload %+v, %v; want the new request", v.Reload, err)
	}
}

func TestApplyDirectorReloadRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.ApplyDirectorReload(1, 0, 1, ReloadRefusal{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("apply of no reload = %v, want ErrNotFound", err)
	}
	r, err := s.RequestDirectorReload(false)
	if err != nil {
		t.Fatal(err)
	}
	receipts := countReceipts(t, s)
	for name, c := range map[string][2]int{"no new session": {0, 0}, "old not below new": {2, 2}} {
		if _, err := s.ApplyDirectorReload(r.Sequence, c[0], c[1], ReloadRefusal{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	for name, refused := range map[string]ReloadRefusal{
		"no reason":      {SHA256: binaryDigest},
		"bad sha256":     {SHA256: "abc", Reason: "exit status 1"},
		"blank reason":   {Reason: "  "},
		"reason too big": {Reason: strings.Repeat("x", MaxRefusedReasonBytes+1)},
	} {
		if _, err := s.ApplyDirectorReload(r.Sequence, 0, 1, refused); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if countReceipts(t, s) != receipts {
		t.Fatal("a refused apply recorded something")
	}
	if _, err := s.ApplyDirectorReload(r.Sequence, 0, 1, ReloadRefusal{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyDirectorReload(r.Sequence, 1, 2, ReloadRefusal{}); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("second apply with other sessions = %v, want ErrMutationConflict", err)
	}
}
