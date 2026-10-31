package cli

import (
	"encoding/json"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/store"
)

// writeLegacyDecision writes open decision d straight into the store at path, bypassing the store's guards, as a
// record an earlier release left on a task it had already closed: an open follow-ups decision on a done hallmark, or
// an audit on a done task. The store now refuses to make that state (docs/decisions/closed-tasks-have-no-open-decisions.md),
// but such records predate the rule and are not migrated. The store must be closed.
func writeLegacyDecision(t *testing.T, path string, d store.Decision) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		repo, err := tx.Bucket([]byte("repositories")).CreateBucketIfNotExists([]byte(d.Repository))
		if err != nil {
			return err
		}
		b, err := repo.CreateBucketIfNotExists([]byte("decisions"))
		if err != nil {
			return err
		}
		return b.Put([]byte(d.TaskID+"/"+d.ID), data)
	}); err != nil {
		t.Fatal(err)
	}
}
