package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"

	"github.com/ndrwl/guildhall/internal/digest"
)

// bucketTaskReleases is each repository's bucket of task releases, keyed by task id: the release a task recorded
// at task new and keeps (docs/store.md#task-releases). It is its own append-only bucket, not a field of Task, so a
// release that predates it and rewrites a task record cannot drop it; it is additive, so it needs no schema bump.
var bucketTaskReleases = []byte("task_releases")

// TaskRelease is the release a task records when it is created on a copy that pins one: the pin's version and the
// sha256 of that release's binary. It is never changed (docs/releases.md#tasks-keep-their-release).
type TaskRelease struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	// RecordedAt is set by the store: the commit time of the task's create receipt, in UTC.
	RecordedAt time.Time `json:"recorded_at"`
}

// createTaskRequest is the create-task request of a task created with its release, so a rerun under the same
// mutation id with another release conflicts rather than replaying.
type createTaskRequest struct {
	Task    Task        `json:"task"`
	Release TaskRelease `json:"release"`
}

// CreateTaskWithRelease records t as CreateTask does and, in the same transaction, its release rel, whose
// Repository and TaskID must be t's or empty. An invalid version or a sha256 that is not 64 lowercase hex
// characters is ErrInvalid, as is a store-set RecordedAt.
func (s *Store) CreateTaskWithRelease(mutationID string, t Task, rel TaskRelease) (Receipt, error) {
	if err := validateTask(t); err != nil {
		return Receipt{}, err
	}
	if rel.Repository == "" && rel.TaskID == "" {
		rel.Repository, rel.TaskID = t.Repository, t.ID
	}
	switch {
	case rel.Repository != t.Repository || rel.TaskID != t.ID:
		return Receipt{}, invalid("task release names %s/%s, not the task %s/%s", rel.Repository, rel.TaskID, t.Repository, t.ID)
	case !validReleaseVersion(rel.Version):
		return Receipt{}, invalid("task release version %q is not a release version", rel.Version)
	case !rel.RecordedAt.IsZero():
		return Receipt{}, invalid("a task release's time is set by the store")
	}
	if _, err := digest.ParseHex(rel.SHA256); err != nil {
		return Receipt{}, invalid("task release sha256 %q must be 64 lowercase hex characters", rel.SHA256)
	}
	if err := checkTitle(t); err != nil {
		return Receipt{}, err
	}
	return s.createTaskWith(mutationID, t, &createTaskRequest{Task: t, Release: rel}, func(tx *bolt.Tx) error {
		b, err := createRepoBucket(tx, t.Repository, bucketTaskReleases)
		if err != nil {
			return err
		}
		if b.Get([]byte(t.ID)) != nil {
			return fmt.Errorf("%w: task release of %s/%s", ErrExists, t.Repository, t.ID)
		}
		rel.RecordedAt = s.now().UTC()
		return putJSON(b, []byte(t.ID), rel)
	})
}

// TaskRelease is the release task repository/taskID recorded, or ErrNotFound when it recorded none: a task created
// before its copy recorded releases, or on a copy with no pin.
func (s *Store) TaskRelease(repository, taskID string) (TaskRelease, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return TaskRelease{}, err
	}
	var r TaskRelease
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketTaskReleases), []byte(taskID), &r)
	})
	if err != nil {
		return TaskRelease{}, fmt.Errorf("store: task release of %s/%s: %w", repository, taskID, err)
	}
	return r, nil
}

// TaskReleases is every recorded task release, by repository and then task id.
func (s *Store) TaskReleases() ([]TaskRelease, error) {
	out := []TaskRelease{}
	err := s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRepos).ForEachBucket(func(repo []byte) error {
			return scan(repoBucket(tx, string(repo), bucketTaskReleases), nil, &out)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: task releases: %w", err)
	}
	return out, nil
}

// ReadTaskRelease reads the release task repository/taskID recorded in the store file at path, for the PATH
// launcher, which runs a task on it: read-only, waiting up to wait for the store's lock, and without the schema
// check Open makes, since the launcher is one fixed program while the store's schema moves on with its releases;
// the task_releases bucket keeps its layout under every schema. A task with no record is ErrNotFound; a path with
// no file is fs.ErrNotExist, and a file that is not a store ErrNotStore.
func ReadTaskRelease(path, repository, taskID string, wait time.Duration) (TaskRelease, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return TaskRelease{}, err
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return TaskRelease{}, fmt.Errorf("store: %w", err)
	case info.Size() == 0:
		return TaskRelease{}, fmt.Errorf("store: %s: %w", path, ErrNotStore)
	}
	if wait <= 0 {
		wait = DefaultLockTimeout
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: wait, ReadOnly: true, OpenFile: func(name string, flag int, mode os.FileMode) (*os.File, error) {
		return os.OpenFile(name, flag&^os.O_CREATE, mode)
	}})
	switch {
	case errors.Is(err, berrors.ErrTimeout):
		return TaskRelease{}, fmt.Errorf("store: %s: %w", path, ErrLocked)
	case errors.Is(err, fs.ErrNotExist):
		return TaskRelease{}, fmt.Errorf("store: %w", err)
	case err != nil:
		return TaskRelease{}, fmt.Errorf("store: %s: %w: %w", path, ErrNotStore, err)
	}
	defer func() { _ = db.Close() }()
	var r TaskRelease
	err = db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketReceipts, bucketRepos} {
			if tx.Bucket(name) == nil {
				return ErrNotStore
			}
		}
		if meta := tx.Bucket(bucketMeta); string(meta.Get(keyFormat)) != string(formatName) {
			return ErrNotStore
		}
		return getJSON(repoBucket(tx, repository, bucketTaskReleases), []byte(taskID), &r)
	})
	if err != nil {
		return TaskRelease{}, fmt.Errorf("store: %s: task release of %s/%s: %w", path, repository, taskID, err)
	}
	return r, nil
}
