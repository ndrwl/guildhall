package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestTaskRelease: a task created with its release records it in the same transaction, a rerun replays it, a rerun
// with another release conflicts, and invalid releases create no task; a task created without one has none.
func TestTaskRelease(t *testing.T) {
	s, path := newStore(t)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	rel := TaskRelease{Version: "v1.0.0", SHA256: a}
	r, err := s.CreateTaskWithRelease("task:r:t1", sampleTask("r", "t1"), rel)
	if err != nil || r.Record != "task" {
		t.Fatalf("create with release: %+v, %v", r, err)
	}
	got, err := s.TaskRelease("r", "t1")
	if err != nil || got.Version != "v1.0.0" || got.SHA256 != a || got.Repository != "r" || got.TaskID != "t1" || !got.RecordedAt.Equal(testTime) {
		t.Fatalf("task release %+v, %v", got, err)
	}
	if again, err := s.CreateTaskWithRelease("task:r:t1", sampleTask("r", "t1"), rel); err != nil || again.Sequence != r.Sequence {
		t.Fatalf("rerun: %+v, %v, want the first receipt", again, err)
	}
	if _, err := s.CreateTaskWithRelease("task:r:t1", sampleTask("r", "t1"), TaskRelease{Version: "v1.1.0", SHA256: b}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("rerun with another release: %v, want ErrMutationConflict", err)
	}
	for name, bad := range map[string]TaskRelease{
		"dev":            {Version: "dev", SHA256: a},
		"a path":         {Version: "v1/../x", SHA256: a},
		"upper-case sha": {Version: "v1.0.0", SHA256: strings.Repeat("A", 64)},
		"another task":   {Repository: "r", TaskID: "other", Version: "v1.0.0", SHA256: a},
		"store-set time": {Version: "v1.0.0", SHA256: a, RecordedAt: time.Now()},
	} {
		if _, err := s.CreateTaskWithRelease("task:r:bad", sampleTask("r", "bad"), bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Task("r", "bad"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused release created the task: %v", err)
	}
	mustCreate(t, s, "task:r:t2", sampleTask("r", "t2"))
	if _, err := s.TaskRelease("r", "t2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task created without a release: %v, want ErrNotFound", err)
	}
	all, err := s.TaskReleases()
	if err != nil || len(all) != 1 || all[0] != got {
		t.Fatalf("task releases %+v, %v", all, err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || len(rep.Repositories) != 1 || rep.Repositories[0].TaskReleases != 1 {
		t.Fatalf("doctor %+v, %v", rep, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadTaskRelease(path, "r", "t1", time.Second); err != nil || r != got {
		t.Fatalf("ReadTaskRelease: %+v, %v", r, err)
	}
	if _, err := ReadTaskRelease(path, "r", "t2", time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadTaskRelease of an unrecorded task: %v, want ErrNotFound", err)
	}
	if _, err := ReadTaskRelease(filepath.Join(t.TempDir(), "none"), "r", "t1", time.Second); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadTaskRelease of no file: %v, want fs.ErrNotExist", err)
	}
	junk := filepath.Join(t.TempDir(), "junk")
	if err := os.WriteFile(junk, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTaskRelease(junk, "r", "t1", time.Second); !errors.Is(err, ErrNotStore) {
		t.Fatalf("ReadTaskRelease of a file that is not a store: %v, want ErrNotStore", err)
	}
}

// TestTaskReleaseSurvivesTaskRewrites: a task's release lives outside its task record, so rewriting the record, as
// cancelling does, or as a release that predates task releases does with a record that names none, keeps it; and
// the launcher's read ignores a schema newer than this build's.
func TestTaskReleaseSurvivesTaskRewrites(t *testing.T) {
	s, path := newStore(t)
	rel := TaskRelease{Version: "v1.0.0", SHA256: strings.Repeat("a", 64)}
	if _, err := s.CreateTaskWithRelease("task:r:t1", sampleTask("r", "t1"), rel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTask("cancel:r:t1", "r", "t1", "replaced"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// An older release rewrites the task record whole, from a Task it read, knowing nothing of releases, and
	// moves the store's schema on as a newer one would.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		tasks := repoBucket(tx, "r", bucketTasks)
		var old map[string]any
		if err := json.Unmarshal(tasks.Get([]byte("t1")), &old); err != nil {
			return err
		}
		old["title"] = "Rewritten by an older release"
		v, err := json.Marshal(old)
		if err != nil {
			return err
		}
		if err := tasks.Put([]byte("t1"), v); err != nil {
			return err
		}
		return tx.Bucket(bucketMeta).Put(keySchema, []byte("999"))
	})
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadTaskRelease(path, "r", "t1", time.Second)
	if err != nil || got.Version != rel.Version || got.SHA256 != rel.SHA256 {
		t.Fatalf("release after the rewrite: %+v, %v", got, err)
	}
}
