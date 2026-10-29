package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNotesAddEditRemoveKeepVersions(t *testing.T) {
	s, path := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	a, ar, err := s.AddNote(1, NotePlan, "", "rebase d20 after d18 merges")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "n1" || a.Kind != NotePlan || a.Session != 1 || a.Version != ar.Sequence || a.ChangedAt.IsZero() || a.Removed {
		t.Fatalf("added = %+v, receipt %+v", a, ar)
	}
	if ar.Record != "director/note/n1" || ar.Operation != "add-note" || !strings.HasPrefix(ar.MutationID, "note-add:1:") {
		t.Fatalf("add receipt = %+v", ar)
	}
	b, _, err := s.AddNote(1, NotePromised, "app/t-1", "tell the Guildmaster when t-1 merges")
	if err != nil || b.ID != "n2" || b.Task != "app/t-1" {
		t.Fatalf("second = %+v, %v; want n2 naming app/t-1", b, err)
	}
	e, er, err := s.EditNote(1, "n1", "rebase d20 and d22 after d18 merges")
	if err != nil || e.Text != "rebase d20 and d22 after d18 merges" || e.Version != er.Sequence || er.MutationID != "note-edit:n1:"+hexSum(e.Text)+":"+itoa(a.Version) {
		t.Fatalf("edit = %+v, %+v, %v", e, er, err)
	}
	if again, r, err := s.EditNote(1, "n1", e.Text); err != nil || r != er || again != e {
		t.Fatalf("replayed edit = %+v, %+v, %v; want the edit's receipt", again, r, err)
	}
	rm, rr, err := s.RemoveNote(1, "n2", "told them")
	if err != nil || !rm.Removed || rm.RemovedReason != "told them" || rr.MutationID != "note-remove:n2" {
		t.Fatalf("remove = %+v, %+v, %v", rm, rr, err)
	}
	if again, r, err := s.RemoveNote(1, "n2", "told them"); err != nil || r != rr || again != rm {
		t.Fatalf("replayed remove = %+v, %+v, %v", again, r, err)
	}
	_ = s.Close()
	s = openTest(t, path)
	live, err := s.LiveNotes()
	if err != nil || len(live) != 1 || live[0] != e {
		t.Fatalf("live = %+v, %v; want only the edited n1", live, err)
	}
	vs, err := s.NoteVersions("n1")
	if err != nil || len(vs) != 2 || vs[0].Action != NoteAdded || vs[0].Text != a.Text || vs[1].Action != NoteEdited || vs[1].Prior != a.Version || vs[1].Text != e.Text {
		t.Fatalf("n1 versions = %+v, %v", vs, err)
	}
	vs, err = s.NoteVersions("n2")
	if err != nil || len(vs) != 2 || vs[1].Action != NoteRemoved || vs[1].Reason != "told them" || vs[1].Text != b.Text {
		t.Fatalf("n2 versions = %+v, %v", vs, err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

// TestNoteIDsHaveTheirOwnSequence: ids count notes, not store receipts.
func TestNoteIDsHaveTheirOwnSequence(t *testing.T) {
	s, _ := newStore(t)
	for i := range 3 {
		mustCreate(t, s, "m-"+itoa(uint64(i)), sampleTask("app", "t-"+itoa(uint64(i))))
	}
	mustStartSession(t, s, sampleSession(1))
	for i, want := range []string{"n1", "n2", "n3"} {
		n, r, err := s.AddNote(1, NoteGuildmaster, "", "prefers short replies "+itoa(uint64(i)))
		if err != nil || n.ID != want || r.Sequence <= 3 {
			t.Fatalf("note %d = %+v, receipt %d, %v; want %s", i, n, r.Sequence, err, want)
		}
	}
}

func TestNoteReplayAndConflict(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	n, r, err := s.AddNote(1, NotePlan, "", "one")
	if err != nil {
		t.Fatal(err)
	}
	if again, ar, err := s.AddNote(1, NotePlan, "", "one"); err != nil || ar != r || again != n {
		t.Fatalf("replayed add = %+v, %+v, %v", again, ar, err)
	}
	if _, _, err := s.RemoveNote(1, "n1", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RemoveNote(1, "n1", "another reason"); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("remove again with another reason = %v, want ErrMutationConflict", err)
	}
	if _, _, err := s.EditNote(1, "n1", "two"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("edit of a removed note = %v, want ErrInvalid", err)
	}
	// Text set again after another edit is a new version, not a replay.
	if _, _, err := s.AddNote(1, NotePlan, "", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EditNote(1, "n2", "b"); err != nil {
		t.Fatal(err)
	}
	back, _, err := s.EditNote(1, "n2", "a")
	if err != nil || back.Text != "a" {
		t.Fatalf("edit back = %+v, %v", back, err)
	}
	if vs, err := s.NoteVersions("n2"); err != nil || len(vs) != 3 {
		t.Fatalf("n2 versions = %d, %v; want 3", len(vs), err)
	}
}

func TestNoteRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.AddNote(1, NotePlan, "", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("add without a session = %v, want ErrNotFound", err)
	}
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustStartSession(t, s, sampleSession(1))
	mustStartSession(t, s, sampleSession(2))
	if _, _, err := s.AddNote(2, NotePlan, "", "kept"); err != nil {
		t.Fatal(err)
	}
	receipts := countReceipts(t, s)
	for name, c := range map[string]struct {
		n          int
		kind, task string
		text       string
	}{
		"older session": {1, "plan", "", "x"},
		"zero session":  {0, "plan", "", "x"},
		"bad kind":      {2, "doing", "", "x"},
		"blank text":    {2, "plan", "", "  "},
		"newline":       {2, "plan", "", "a\nb"},
		"over 1024":     {2, "plan", "", strings.Repeat("x", MaxNoteBytes+1)},
		"bad task":      {2, "plan", "app", "x"},
	} {
		if _, _, err := s.AddNote(c.n, NoteKind(c.kind), c.task, c.text); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, _, err := s.AddNote(2, NotePlan, "app/t-9", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.AddNote(2, NotePlan, "", strings.Repeat("x", MaxNoteBytes)); err != nil {
		t.Errorf("1024 bytes: %v", err)
	}
	receipts++
	if _, _, err := s.EditNote(2, "n9", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("edit of an unknown note = %v, want ErrNotFound", err)
	}
	if _, _, err := s.RemoveNote(2, "n9", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove of an unknown note = %v, want ErrNotFound", err)
	}
	if _, _, err := s.EditNote(1, "n1", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("edit from an older session = %v, want ErrInvalid", err)
	}
	if _, _, err := s.RemoveNote(2, "n1", strings.Repeat("r", MaxNoteReasonBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("over-long reason = %v, want ErrInvalid", err)
	}
	mustMove(t, s, 2, DirectorStopped)
	receipts++
	if _, _, err := s.AddNote(2, NotePlan, "", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("stopped session: add = %v, want ErrInvalid", err)
	}
	if _, _, err := s.RemoveNote(2, "n1", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("stopped session: remove = %v, want ErrInvalid", err)
	}
	if got := countReceipts(t, s); got != receipts {
		t.Errorf("receipts = %d, want %d: a refused note recorded something", got, receipts)
	}
	if live, err := s.LiveNotes(); err != nil || len(live) != 2 {
		t.Errorf("live = %+v, %v; want the two added notes", live, err)
	}
}

func TestLiveNotesOrderByKindThenID(t *testing.T) {
	s, _ := newStore(t)
	mustStartSession(t, s, sampleSession(1))
	for i, k := range []NoteKind{NotePlan, NotePromised, NoteGuildmaster, NotePlan, NotePromised, NotePlan, NotePlan, NotePlan, NotePlan, NotePlan} {
		if _, _, err := s.AddNote(1, k, "", "note "+itoa(uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.LiveNotes()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range live {
		got = append(got, n.ID)
	}
	if want := "n2 n5 n3 n1 n4 n6 n7 n8 n9 n10"; strings.Join(got, " ") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}
