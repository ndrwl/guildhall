package store

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/schema-v1.store")

const fixture = "testdata/schema-v1.store"

// writeFixture fills a store with one record of every kind.
func writeFixture(t *testing.T, path string) {
	t.Helper()
	s := openTest(t, path)
	mustCreate(t, s, "fixture-1", sampleTask("app", "t-1"))
	steps := []func() (Receipt, error){
		func() (Receipt, error) { return s.StartAttempt("fixture-2", "app", "t-1", "build") },
		func() (Receipt, error) {
			return s.FinishAttempt("fixture-3", "app", "t-1", "build", 1, AttemptSucceeded)
		},
		func() (Receipt, error) {
			return s.PutDocument("fixture-4", DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan", Path: "tasks/t-1/plan.md", SHA256: strings.Repeat("d", 64)})
		},
		func() (Receipt, error) {
			return s.BindPane("fixture-5", PaneBinding{Repository: "app", TaskID: "t-1", Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p3"})
		},
		func() (Receipt, error) {
			return s.OpenDecision("fixture-6", Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Kind: DecisionQuestion, Question: "Accept the plan?"})
		},
		func() (Receipt, error) { return s.AnswerDecision("fixture-7", "app", "t-1", "accept-plan", "Yes") },
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFinishAttemptLegacyReceipt(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	first, err := s.Receipt("fixture-3")
	if err != nil || first.Record != "" {
		t.Fatalf("legacy receipt = %+v, %v; want no record label", first, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []AttemptOutcome{AttemptSucceeded, AttemptFailed, AttemptStopped, AttemptSucceeded} {
		got, err := s.FinishAttempt("legacy-second", "app", "t-1", "build", 1, outcome)
		if outcome == AttemptSucceeded {
			if err != nil || got != first {
				t.Fatalf("equal finish = %+v, %v; want %+v", got, err, first)
			}
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("unequal finish: %v; want ErrInvalid", err)
		}
	}
	if _, err := s.Receipt("legacy-second"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second receipt: %v; want ErrNotFound", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("second finishes changed the persisted store")
	}
	s = openTest(t, path)
	r, err := s.StartAttempt("legacy-second", "app", "t-1", "build")
	if err != nil || r.Sequence != 8 || r.Attempt != 2 {
		t.Fatalf("next mutation = %+v, %v; want sequence 8, attempt 2", r, err)
	}
}

// TestSchemaV1Fixture opens a store file committed to the repository, so every
// CI platform reads a file it did not create.
func TestSchemaV1Fixture(t *testing.T) {
	if *update {
		_ = os.Remove(fixture)
		writeFixture(t, fixture)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	ro := testOptions()
	ro.ReadOnly = true
	s, err := Open(path, ro)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if rep.SchemaVersion != 1 || rep.Receipts != 7 || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	want := RepositoryCounts{Repository: "app", Tasks: 1, Attempts: 1, Documents: 1, PaneBindings: 1, Decisions: 1}
	if len(rep.Repositories) != 1 || rep.Repositories[0] != want {
		t.Fatalf("repositories = %+v, want %+v", rep.Repositories, want)
	}
	decisions, err := s.Decisions("app", "t-1")
	if err != nil || len(decisions) != 1 || decisions[0].Answer != "Yes" {
		t.Fatalf("decisions = %+v, %v", decisions, err)
	}
	_ = s.Close()

	// The answer's request shape is unchanged by typed decisions, so the recorded answer still replays.
	s = openTest(t, path)
	if r, err := s.AnswerDecision("fixture-7", "app", "t-1", "accept-plan", "Yes"); err != nil || r.Sequence != 7 {
		t.Fatalf("answer replay from fixture = %+v, %v", r, err)
	}
	_ = s.Close()

	// The copied file accepts new writes and replays its recorded mutations.
	s = openTest(t, path)
	r, err := s.StartAttempt("fixture-2", "app", "t-1", "build")
	if err != nil || r.Attempt != 1 || r.Sequence != 2 {
		t.Fatalf("replay from fixture = %+v, %v", r, err)
	}
	mustCreate(t, s, "after-fixture", sampleTask("app", "t-2"))
	// A schema 1 file written before messages existed accepts them.
	m := toDirector("q-fixture")
	if _, err := s.SendMessage(m); err != nil {
		t.Fatalf("message on fixture copy: %v", err)
	}
	// A schema 1 file written before workflow approvals existed accepts them.
	if _, err := s.ApproveWorkflow(sampleApproval('a')); err != nil {
		t.Fatalf("workflow approval on fixture copy: %v", err)
	}
}
