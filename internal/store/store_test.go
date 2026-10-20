package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var testTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testOptions() Options {
	return Options{LockTimeout: 100 * time.Millisecond, Now: func() time.Time { return testTime }}
}

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, testOptions())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	return openTest(t, path), path
}

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func sampleTask(repo, id string) Task {
	return Task{
		Repository:   repo,
		ID:           id,
		Title:        "Add the store",
		Workflow:     workflow.Revision{Name: "default", Digest: digest('a')},
		ConfigDigest: digest('b'),
	}
}

func mustCreate(t *testing.T, s *Store, mutationID string, task Task) Receipt {
	t.Helper()
	r, err := s.CreateTask(mutationID, task)
	if err != nil {
		t.Fatalf("CreateTask(%s): %v", mutationID, err)
	}
	return r
}

func TestOpenCreatesAndReopensStore(t *testing.T) {
	s, path := newStore(t)
	r := mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if r.MutationID != "m-1" || r.Operation != "create-task" || r.Repository != "app" || r.TaskID != "t-1" || r.Sequence != 1 || !r.CommittedAt.Equal(testTime) {
		t.Fatalf("receipt = %+v", r)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = openTest(t, path)
	got, err := s.Task("app", "t-1")
	if err != nil {
		t.Fatalf("Task after reopen: %v", err)
	}
	want := sampleTask("app", "t-1")
	want.State = TaskOpen
	if got.Title != want.Title || got.Workflow != want.Workflow || got.ConfigDigest != want.ConfigDigest || got.State != TaskOpen {
		t.Fatalf("task after reopen = %+v, want %+v", got, want)
	}
	stored, err := s.Receipt("m-1")
	if err != nil || stored != r {
		t.Fatalf("Receipt after reopen = %+v, %v; want %+v", stored, err, r)
	}
}

func TestReplayReturnsOriginalReceiptWithoutChangingState(t *testing.T) {
	s, _ := newStore(t)
	first := mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.SetTaskState("m-2", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}

	// Replaying the create after a later change returns the original receipt and does not reset the task.
	again, err := s.CreateTask("m-1", sampleTask("app", "t-1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again != first {
		t.Fatalf("replay receipt = %+v, want original %+v", again, first)
	}
	task, _ := s.Task("app", "t-1")
	if task.State != TaskDone {
		t.Fatalf("replay changed state to %q", task.State)
	}
	if n := countReceipts(t, s); n != 2 {
		t.Fatalf("receipts = %d, want 2", n)
	}
}

func TestReplayIsIdempotentAcrossRestart(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.StartAttempt("m-1", "app", "t-1", "build"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attempt on missing task: %v", err)
	}
	mustCreate(t, s, "m-0", sampleTask("app", "t-1"))
	first, err := s.StartAttempt("m-1", "app", "t-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s = openTest(t, path)
	again, err := s.StartAttempt("m-1", "app", "t-1", "build")
	if err != nil || again != first || again.Attempt != 1 {
		t.Fatalf("replay after restart = %+v, %v; want %+v", again, err, first)
	}
	attempts, _ := s.Attempts("app", "t-1")
	if len(attempts) != 1 {
		t.Fatalf("attempts = %+v, want one", attempts)
	}
}

func TestConflictingMutationIDReuseIsRefusedWithoutChange(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))

	other := sampleTask("app", "t-2")
	if _, err := s.CreateTask("m-1", other); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("different request, same id: err = %v, want ErrMutationConflict", err)
	}
	if _, err := s.Task("app", "t-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("conflicting replay created a task: %v", err)
	}
	if _, err := s.SetTaskState("m-1", "app", "t-1", TaskDone); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("different operation, same id: err = %v, want ErrMutationConflict", err)
	}
	task, _ := s.Task("app", "t-1")
	if task.State != TaskOpen {
		t.Fatalf("conflicting replay changed state to %q", task.State)
	}
	if n := countReceipts(t, s); n != 1 {
		t.Fatalf("receipts = %d, want 1", n)
	}
}

func TestFailedMutationLeavesNoReceipt(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.SetTaskState("m-1", "app", "missing", TaskDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Receipt("m-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed mutation recorded a receipt: %v", err)
	}
	// The id is still free for a request that succeeds.
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
}

func TestMutationIsAtomic(t *testing.T) {
	s, path := newStore(t)
	injected := errors.New("injected failure after record write")
	testHookBeforeCommit = func() error { return injected }
	t.Cleanup(func() { testHookBeforeCommit = nil })

	if _, err := s.CreateTask("m-1", sampleTask("app", "t-1")); !errors.Is(err, injected) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	testHookBeforeCommit = nil
	_ = s.Close()

	s = openTest(t, path)
	if _, err := s.Task("app", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task survived a rolled-back mutation: %v", err)
	}
	if _, err := s.Receipt("m-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("receipt survived a rolled-back mutation: %v", err)
	}
	if r := mustCreate(t, s, "m-1", sampleTask("app", "t-1")); r.Sequence != 1 {
		t.Fatalf("rolled-back mutation consumed sequence: %+v", r)
	}
}

func TestConcurrentSameMutationAppliesOnce(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-0", sampleTask("app", "t-1"))

	const n = 16
	receipts := make([]Receipt, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { receipts[i], errs[i] = s.StartAttempt("m-1", "app", "t-1", "build") })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || receipts[i] != receipts[0] {
			t.Fatalf("call %d = %+v, %v; want %+v", i, receipts[i], errs[i], receipts[0])
		}
	}
	attempts, _ := s.Attempts("app", "t-1")
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
}

func TestSecondWriterIsRefused(t *testing.T) {
	_, path := newStore(t)
	start := time.Now()
	_, err := Open(path, testOptions())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: err = %v, want ErrLocked", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("lock wait was not bounded: %v", waited)
	}
	ro := testOptions()
	ro.ReadOnly = true
	if _, err := Open(path, ro); !errors.Is(err, ErrLocked) {
		t.Fatalf("read-only Open while a writer holds the store: err = %v, want ErrLocked", err)
	}
}

func TestCloseReleasesLockAndIsIdempotent(t *testing.T) {
	s, path := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.CreateTask("m-1", sampleTask("app", "t-1")); !errors.Is(err, ErrClosed) {
		t.Fatalf("mutation after Close: err = %v, want ErrClosed", err)
	}
	if _, err := s.Task("app", "t-1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after Close: err = %v, want ErrClosed", err)
	}
	openTest(t, path)
}

func TestCloseWaitsForInFlightMutations(t *testing.T) {
	s, path := newStore(t)
	mustCreate(t, s, "m-0", sampleTask("app", "t-1"))

	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = s.PutDocument(fmt.Sprintf("m-%d", i+1), DocumentRef{
				Repository: "app", TaskID: "t-1", Name: fmt.Sprintf("doc-%d", i), Path: fmt.Sprintf("docs/%d.md", i), SHA256: strings.Repeat("c", 64),
			})
		})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	s = openTest(t, path)
	docs, err := s.Documents("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]bool{}
	for _, d := range docs {
		stored[d.Name] = true
	}
	for i, err := range errs {
		name := fmt.Sprintf("doc-%d", i)
		switch {
		case err == nil && !stored[name]:
			t.Fatalf("mutation %d reported success but is missing after reopen", i)
		case err != nil && !errors.Is(err, ErrClosed):
			t.Fatalf("mutation %d: err = %v, want nil or ErrClosed", i, err)
		case err != nil && stored[name]:
			t.Fatalf("mutation %d reported ErrClosed but was stored", i)
		}
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	s, path := newStore(t)
	_ = s.Close()
	setMeta(t, path, "schema_version", []byte(fmt.Sprint(SchemaVersion+1)))

	if _, err := Open(path, testOptions()); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Open: err = %v, want ErrNewerSchema", err)
	}
	ro := testOptions()
	ro.ReadOnly = true
	if _, err := Open(path, ro); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("read-only Open: err = %v, want ErrNewerSchema", err)
	}
	// Refusal released the lock.
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("lock still held after refusal: %v", err)
	}
	_ = db.Close()
}

func TestMissingRequiredBucketsAreRefused(t *testing.T) {
	for _, bucket := range []string{"meta", "receipts", "repositories"} {
		for _, readOnly := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/readOnly=%t", bucket, readOnly), func(t *testing.T) {
				s, path := newStore(t)
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				db, err := bolt.Open(path, 0o600, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte(bucket)) }); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				opts := testOptions()
				opts.ReadOnly = readOnly
				opened, err := Open(path, opts)
				if opened != nil {
					_ = opened.Close()
				}
				if !errors.Is(err, ErrNotStore) {
					t.Fatalf("Open: %v, want ErrNotStore", err)
				}
				db, err = bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if err := db.View(func(tx *bolt.Tx) error {
					if tx.Bucket([]byte(bucket)) != nil {
						t.Error("Open recreated missing bucket")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestForeignFilesAreRefused(t *testing.T) {
	dir := t.TempDir()

	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte(strings.Repeat("not a database\n", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(text, testOptions()); !errors.Is(err, ErrNotStore) {
		t.Fatalf("text file: err = %v, want ErrNotStore", err)
	}

	other := filepath.Join(dir, "other.db")
	db, err := bolt.Open(other, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucket([]byte("unrelated")); return err }); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(other, testOptions()); !errors.Is(err, ErrNotStore) {
		t.Fatalf("foreign bbolt file: err = %v, want ErrNotStore", err)
	}
}

func TestReadOnlyNeverCreatesOrWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.store")
	ro := testOptions()
	ro.ReadOnly = true
	if _, err := Open(path, ro); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read-only Open of missing file: err = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read-only Open created the file: %v", err)
	}

	s := openTest(t, path)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	_ = s.Close()

	r, err := Open(path, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Task("app", "t-1"); err != nil {
		t.Fatalf("read-only read: %v", err)
	}
	if _, err := r.CreateTask("m-2", sampleTask("app", "t-2")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only mutation: err = %v, want ErrReadOnly", err)
	}
}

func countReceipts(t *testing.T, s *Store) int {
	t.Helper()
	rep, err := s.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	return rep.Receipts
}

func setMeta(t *testing.T, path, key string, value []byte) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Put([]byte(key), value) }); err != nil {
		t.Fatal(err)
	}
}
