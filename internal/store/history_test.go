package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReceiptsNameTheirRecord pins the record key each task mutation writes into its receipt.
func TestReceiptsNameTheirRecord(t *testing.T) {
	s, _ := newStore(t)
	steps := []struct {
		want string
		run  func() (Receipt, error)
	}{
		{"task", func() (Receipt, error) { return s.CreateTask("m-1", sampleTask("app", "t-1")) }},
		{"attempt/build/1", func() (Receipt, error) { return s.StartAttempt("m-2", "app", "t-1", "build") }},
		{"attempt/build/1", func() (Receipt, error) {
			return s.FinishAttempt("m-3", "app", "t-1", "build", 1, AttemptFailed)
		}},
		{"document/plan", func() (Receipt, error) {
			return s.PutDocument("m-4", DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan", Path: "t-1/plan.md", SHA256: strings.Repeat("d", 64)})
		}},
		{"pane", func() (Receipt, error) {
			return s.BindPane("m-5", PaneBinding{Repository: "app", TaskID: "t-1", Session: "lab", Workspace: "w", Tab: "t", Pane: "p"})
		}},
		{"pane", func() (Receipt, error) { return s.UnbindPane("m-6", "app", "t-1") }},
		{"decision/q-1", func() (Receipt, error) { return s.OpenDecision("m-7", sampleDecision("q-1")) }},
		{"decision/q-1", func() (Receipt, error) { return s.AnswerDecision("m-8", "app", "t-1", "q-1", "approve") }},
		{"message/q-msg", func() (Receipt, error) { return s.SendMessage(toDirector("q-msg")) }},
		{"message/q-msg", func() (Receipt, error) { return s.AckMessage(toDirector("q-msg").To(), "q-msg") }},
		{"task", func() (Receipt, error) { return s.SetTaskState("m-9", "app", "t-1", TaskDone) }},
		{"", func() (Receipt, error) { return s.ApproveWorkflow(sampleApproval('a')) }},
	}
	for _, step := range steps {
		r, err := step.run()
		if err != nil {
			t.Fatal(err)
		}
		if r.Record != step.want {
			t.Fatalf("%s receipt record = %q, want %q", r.Operation, r.Record, step.want)
		}
		if again, err := s.Receipt(r.MutationID); err != nil || again.Record != step.want {
			t.Fatalf("stored %s receipt = %+v, %v", r.MutationID, again, err)
		}
	}
}

// steppingClock returns a clock that advances one minute on every call, starting at testTime.
func steppingClock() func() time.Time {
	next := testTime
	return func() time.Time {
		now := next
		next = next.Add(time.Minute)
		return now
	}
}

func TestTaskHistoryReadsOneTaskConsistently(t *testing.T) {
	opts := testOptions()
	opts.Now = steppingClock()
	s, err := Open(filepath.Join(t.TempDir(), "guildhall.store"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	if _, err := s.StartAttempt("m-3", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("m-4", "app", "t-2", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("m-5", sampleDecision("q-1")); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "m-6", sampleTask("lib", "t-1"))

	h, err := s.TaskHistory("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if h.AsOf.Sequence != 6 || !h.AsOf.CommittedAt.Equal(testTime.Add(5*time.Minute)) {
		t.Fatalf("as of = %+v, want sequence 6 at the sixth commit", h.AsOf)
	}
	if h.Task.ID != "t-1" || len(h.Attempts) != 1 || len(h.Decisions) != 1 || len(h.Documents) != 0 {
		t.Fatalf("records = %+v", h)
	}
	var got []string
	for _, r := range h.Receipts {
		got = append(got, r.MutationID+"="+r.Record)
	}
	// Other tasks of the same repository and the same task id in another repository are excluded.
	if want := "m-1=task m-3=attempt/build/1 m-5=decision/q-1"; strings.Join(got, " ") != want {
		t.Fatalf("receipts = %v, want %s", got, want)
	}
	if h.Documents == nil || h.Receipts == nil || h.Attempts == nil || h.Decisions == nil {
		t.Fatalf("history lists must be empty, not nil: %+v", h)
	}

	if _, err := s.TaskHistory("app", "t-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task: err = %v, want ErrNotFound", err)
	}
	if _, err := s.TaskHistory("nope", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repository: err = %v, want ErrNotFound", err)
	}
}

func TestTaskHistoryIsReadOnly(t *testing.T) {
	s, path := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ro := testOptions()
	ro.ReadOnly = true
	r, err := Open(path, ro)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.TaskHistory("app", "t-1"); err != nil {
		t.Fatalf("history on a read-only store: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("reading history changed the store file")
	}
	if _, err := r.TaskHistory("app", "t-1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed store: err = %v, want ErrClosed", err)
	}
}

// TestTaskHistoryOfLegacyReceipts reads the committed schema 1 fixture, written before receipts named
// their record: every receipt is still listed, with an empty record.
func TestTaskHistoryOfLegacyReceipts(t *testing.T) {
	ro := testOptions()
	ro.ReadOnly = true
	s, err := Open(fixture, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h, err := s.TaskHistory("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if h.AsOf.Sequence != 7 || len(h.Receipts) != 7 {
		t.Fatalf("legacy history = %+v", h)
	}
	for i, r := range h.Receipts {
		if r.Sequence != uint64(i+1) || r.Record != "" {
			t.Fatalf("legacy receipt %d = %+v", i, r)
		}
	}
}
