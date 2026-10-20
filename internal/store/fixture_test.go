package store

import (
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
			return s.OpenDecision("fixture-6", "app", "t-1", "accept-plan", "Accept the plan?")
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

	// The copied file accepts new writes and replays its recorded mutations.
	s = openTest(t, path)
	r, err := s.StartAttempt("fixture-2", "app", "t-1", "build")
	if err != nil || r.Attempt != 1 || r.Sequence != 2 {
		t.Fatalf("replay from fixture = %+v, %v", r, err)
	}
	mustCreate(t, s, "after-fixture", sampleTask("app", "t-2"))
}
