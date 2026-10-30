package store

import (
	"errors"
	"strings"
	"testing"
)

func policySession(n int) DirectorSession {
	d := sampleSession(n)
	d.Maintenance = MaintenancePolicy{NoteAuditEverySessions: 3, HallmarkAfterMerges: 4}
	return d
}

func sendMerged(t *testing.T, s *Store, repo, task, id string) {
	t.Helper()
	if _, err := s.SendMessage(Message{ID: id, Recipient: RecipientDirector, Repository: repo, TaskID: task, Type: MessageMerged, Text: "Merged."}); err != nil {
		t.Fatal(err)
	}
}

func readMaintenance(t *testing.T, s *Store) MaintenanceView {
	t.Helper()
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	return v.Maintenance
}

// TestMaintenanceRecordAndList: runs round-trip in order, replaying the newest run returns it, and the sequence
// keys the mutation id.
func TestMaintenanceRecordAndList(t *testing.T) {
	s, path := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustStartSession(t, s, policySession(1))
	notes, r, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 1, Summary: "removed 4 (held by d47, D94, config); proposed 2: d58, skill change"})
	if err != nil {
		t.Fatal(err)
	}
	if notes.Sequence != 1 || notes.Receipt != r.Sequence || notes.Time.IsZero() || r.MutationID != "director-maintenance:1" ||
		r.Record != "director/maintenance/1" || r.Operation != "record-maintenance" {
		t.Fatalf("notes run = %+v, receipt %+v", notes, r)
	}
	if again, ar, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 1, Summary: notes.Summary}); err != nil || ar != r || again != notes {
		t.Fatalf("replayed run = %+v, %+v, %v; want the first", again, ar, err)
	}
	hall, hr, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceHallmark, Repository: "app", Task: "t-1", Session: 1, Summary: "hallmark launched"})
	if err != nil || hall.Sequence != 2 || hr.MutationID != "director-maintenance:2" {
		t.Fatalf("hallmark run = %+v, %+v, %v", hall, hr, err)
	}
	// The same note audit again, after another run, is a new run.
	if again, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 1, Summary: notes.Summary}); err != nil || again.Sequence != 3 {
		t.Fatalf("second note audit = %+v, %v; want run 3", again, err)
	}
	_ = s.Close()
	s = openTest(t, path)
	runs, err := s.MaintenanceRuns()
	if err != nil || len(runs) != 3 || runs[0] != notes || runs[1] != hall || runs[2].Sequence != 3 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

func TestMaintenanceRefusals(t *testing.T) {
	s, _ := newStore(t)
	if runs, err := s.MaintenanceRuns(); err != nil || len(runs) != 0 || runs == nil {
		t.Fatalf("no runs = %#v, %v; want an empty list", runs, err)
	}
	if _, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 1, Summary: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record without a session = %v, want ErrNotFound", err)
	}
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	negative := policySession(1)
	negative.Maintenance.HallmarkAfterMerges = -1
	if _, err := s.StartDirectorSession(negative); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session with a negative cadence = %v, want ErrInvalid", err)
	}
	mustStartSession(t, s, policySession(1))
	mustStartSession(t, s, policySession(2))
	receipts := countReceipts(t, s)
	for name, m := range map[string]MaintenanceRun{
		"older session":     {Kind: MaintenanceNotes, Session: 1, Summary: "x"},
		"zero session":      {Kind: MaintenanceNotes, Summary: "x"},
		"bad kind":          {Kind: "audit", Session: 2, Summary: "x"},
		"notes with a repo": {Kind: MaintenanceNotes, Repository: "app", Session: 2, Summary: "x"},
		"hallmark no task":  {Kind: MaintenanceHallmark, Repository: "app", Session: 2, Summary: "x"},
		"hallmark bad id":   {Kind: MaintenanceHallmark, Repository: "app", Task: "t 1", Session: 2, Summary: "x"},
		"blank summary":     {Kind: MaintenanceNotes, Session: 2, Summary: "  "},
		"newline":           {Kind: MaintenanceNotes, Session: 2, Summary: "a\nb"},
		"over 1024":         {Kind: MaintenanceNotes, Session: 2, Summary: strings.Repeat("x", MaxMaintenanceSummaryBytes+1)},
		"store-set fields":  {Kind: MaintenanceNotes, Session: 2, Summary: "x", Receipt: 9},
	} {
		if _, _, err := s.RecordMaintenance(m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceHallmark, Repository: "app", Task: "t-9", Session: 2, Summary: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 3, Summary: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 2, Summary: strings.Repeat("x", MaxMaintenanceSummaryBytes)}); err != nil {
		t.Errorf("1024 bytes: %v", err)
	}
	receipts++
	mustMove(t, s, 2, DirectorStopped)
	receipts++
	if _, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 2, Summary: "y"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("stopped session: err = %v, want ErrInvalid", err)
	}
	if got := countReceipts(t, s); got != receipts {
		t.Errorf("receipts = %d, want %d: a refused run recorded something", got, receipts)
	}
}

// TestMaintenanceViewCountsFromTheBaseline: sessions and merges before the first session with a maintenance policy
// count for nothing; later merges count per repository until a hallmark of that repository is recorded.
func TestMaintenanceViewCountsFromTheBaseline(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-2", sampleTask("web", "t-1"))
	if v := readMaintenance(t, s); v.Baseline != 0 || len(v.Repositories) != 0 || v.LastNotes != nil {
		t.Fatalf("no session = %+v; want no baseline", v)
	}
	mustStartSession(t, s, sampleSession(1))
	for i := range 6 {
		sendMerged(t, s, "app", "t-1", "old-"+itoa(uint64(i)))
	}
	mustStartSession(t, s, sampleSession(2))
	if v := readMaintenance(t, s); v.Baseline != 0 {
		t.Fatalf("sessions without a policy = %+v; want no baseline", v)
	}
	r := mustStartSession(t, s, policySession(3))
	mustStartSession(t, s, policySession(4))
	v := readMaintenance(t, s)
	if v.Baseline != 3 || v.BaselineReceipt != r.Sequence || len(v.Repositories) != 2 || v.Repositories[0].Merged != 0 || v.Repositories[1].Merged != 0 {
		t.Fatalf("upgraded = %+v; want baseline 3 and nothing merged since", v)
	}
	sendMerged(t, s, "app", "t-1", "new-1")
	sendMerged(t, s, "app", "t-1", "new-2")
	sendMerged(t, s, "web", "t-1", "new-3")
	if _, err := s.SendMessage(Message{ID: "other", Recipient: RecipientDirector, Repository: "app", TaskID: "t-1", Type: MessagePublished, Text: "Published."}); err != nil {
		t.Fatal(err)
	}
	v = readMaintenance(t, s)
	if v.Repositories[0].Repository != "app" || v.Repositories[0].Merged != 2 || v.Repositories[1].Repository != "web" || v.Repositories[1].Merged != 1 {
		t.Fatalf("after merges = %+v", v.Repositories)
	}
	hall, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceHallmark, Repository: "app", Task: "t-1", Session: 4, Summary: "launched"})
	if err != nil {
		t.Fatal(err)
	}
	sendMerged(t, s, "app", "t-1", "new-4")
	notes, _, err := s.RecordMaintenance(MaintenanceRun{Kind: MaintenanceNotes, Session: 4, Summary: "removed 1"})
	if err != nil {
		t.Fatal(err)
	}
	v = readMaintenance(t, s)
	if app := v.Repositories[0]; app.LastHallmark == nil || *app.LastHallmark != hall || app.Merged != 1 {
		t.Fatalf("app after its hallmark = %+v; want the run and one merge since", app)
	}
	if web := v.Repositories[1]; web.LastHallmark != nil || web.Merged != 1 {
		t.Fatalf("web = %+v; want its own count untouched", web)
	}
	if v.LastNotes == nil || *v.LastNotes != notes {
		t.Fatalf("last notes = %+v, want %+v", v.LastNotes, notes)
	}
}
