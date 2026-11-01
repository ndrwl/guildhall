package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// binaryDigest is a well-formed binary sha256.
const binaryDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestApplyDirectorReloadRecordsTheBinaries: the applied reload copies each session's release, binary sha256 and config digest from
// its record and carries a refusal; replaying it with the same payload returns the same record, while another
// refusal conflicts. A session with no sha256, as an older supervisor records, leaves its side's sha256 empty.
func TestApplyDirectorReloadRecordsTheBinaries(t *testing.T) {
	s, _ := newStore(t)
	old, cur := strings.Repeat("a", 64), strings.Repeat("b", 64)
	first := sampleSession(1)
	first.BinarySHA256 = old
	mustStartSession(t, s, first)
	r, err := s.RequestDirectorReload(true)
	if err != nil {
		t.Fatal(err)
	}
	second := sampleSession(2)
	second.Release, second.BinarySHA256, second.ConfigDigest = "v0.5.0", cur, sampleDigest('d')
	mustStartSession(t, s, second)
	refused := ReloadRefusal{SHA256: strings.Repeat("c", 64), Reason: "director start --check: exit status 1"}
	applied, err := s.ApplyDirectorReload(r.Sequence, 1, 2, refused)
	if err != nil {
		t.Fatal(err)
	}
	if applied.OldRelease != "v0.4.0" || applied.OldSHA256 != old || applied.NewRelease != "v0.5.0" || applied.NewSHA256 != cur ||
		applied.OldConfigDigest != sampleDigest('c') || applied.NewConfigDigest != sampleDigest('d') ||
		applied.RefusedSHA256 != refused.SHA256 || applied.RefusedReason != refused.Reason || applied.State != ReloadApplied {
		t.Fatalf("applied = %+v", applied)
	}
	receipts := countReceipts(t, s)
	if again, err := s.ApplyDirectorReload(r.Sequence, 1, 2, refused); err != nil || again != applied || countReceipts(t, s) != receipts {
		t.Fatalf("replay = %+v, %v; want the applied reload, recording nothing", again, err)
	}
	if _, err := s.ApplyDirectorReload(r.Sequence, 1, 2, ReloadRefusal{}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("replay without the refusal = %v, want ErrMutationConflict", err)
	}

	r, err = s.RequestDirectorReload(false)
	if err != nil {
		t.Fatal(err)
	}
	mustStartSession(t, s, sampleSession(3))
	got, err := s.ApplyDirectorReload(r.Sequence, 2, 3, ReloadRefusal{})
	if err != nil || got.OldSHA256 != cur || got.NewSHA256 != "" || got.NewRelease != "v0.4.0" || got.RefusedSHA256 != "" || got.RefusedReason != "" {
		t.Fatalf("applied onto an unhashed session = %+v, %v", got, err)
	}
}

// TestStartDirectorSessionRefusesABadBinarySHA256: a session's binary sha256 is empty or 64 lowercase hex characters.
func TestStartDirectorSessionRefusesABadBinarySHA256(t *testing.T) {
	s, _ := newStore(t)
	d := sampleSession(1)
	d.BinarySHA256 = strings.Repeat("A", 64)
	if _, err := s.StartDirectorSession(d); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session with an uppercase binary sha256 = %v, want ErrInvalid", err)
	}
}

// TestDirectorBinaryFieldsDecodeAsOlderRecords: the binary fields are omitted when empty, and records carrying them
// decode into types without them, as an older binary reads them.
func TestDirectorBinaryFieldsDecodeAsOlderRecords(t *testing.T) {
	plain, err := json.Marshal(DirectorReload{Sequence: 3, State: ReloadApplied})
	if err != nil || strings.Contains(string(plain), "sha256") || strings.Contains(string(plain), "release") {
		t.Fatalf("an empty reload's binary fields = %s, %v; want them omitted", plain, err)
	}
	full, err := json.Marshal(DirectorReload{Sequence: 3, State: ReloadApplied, OldSession: 1, NewSession: 2, OldSHA256: binaryDigest,
		NewSHA256: binaryDigest, OldRelease: "dev", NewRelease: "dev", RefusedSHA256: binaryDigest, RefusedReason: "exit status 1"})
	if err != nil {
		t.Fatal(err)
	}
	var reload struct {
		Sequence   uint64      `json:"sequence"`
		State      ReloadState `json:"state"`
		OldSession int         `json:"old_session"`
		NewSession int         `json:"new_session"`
	}
	if err := decode([]byte("k"), full, &reload); err != nil || reload.Sequence != 3 || reload.OldSession != 1 || reload.NewSession != 2 {
		t.Fatalf("the reload as an older binary reads it = %+v, %v", reload, err)
	}
	d := sampleSession(1)
	d.BinarySHA256 = binaryDigest
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Number  int    `json:"number"`
		Release string `json:"release"`
	}
	if err := decode([]byte("k"), data, &session); err != nil || session.Number != 1 || session.Release != d.Release {
		t.Fatalf("the session as an older binary reads it = %+v, %v", session, err)
	}
}
