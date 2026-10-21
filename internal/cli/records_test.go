package cli

import (
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestStoreRecordsContract runs the shared interp.Records suite against the production adapter,
// which opens the store file for each operation (docs/interpreter.md#seams).
func TestStoreRecordsContract(t *testing.T) {
	interptest.RunRecordsContract(t, newStoreRecordsHarness)
}

// newStoreRecordsHarness is storeRecords over a new temporary store; its setup writes open the store
// per call as the adapter does, so neither holds the store's lock between operations.
func newStoreRecordsHarness(t *testing.T) interptest.RecordsHarness {
	path := filepath.Join(t.TempDir(), "director.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	write := func(fn func(*store.Store) (store.Receipt, error)) (store.Receipt, error) {
		return withStore(path, false, fn)
	}
	return interptest.RecordsHarness{
		Records: storeRecords(path),
		CreateTask: func(m string, task store.Task) (store.Receipt, error) {
			return write(func(s *store.Store) (store.Receipt, error) { return s.CreateTask(m, task) })
		},
		AnswerDecision: func(m, repo, id, decision, answer string) (store.Receipt, error) {
			return write(func(s *store.Store) (store.Receipt, error) { return s.AnswerDecision(m, repo, id, decision, answer) })
		},
		RuleFinding: func(m string, r store.Ruling) (store.Receipt, error) {
			return write(func(s *store.Store) (store.Receipt, error) { return s.RuleFinding(m, r) })
		},
		RequestFullReview: func(m, repo, id string) (store.Receipt, error) {
			return write(func(s *store.Store) (store.Receipt, error) { return s.RequestFullReview(m, repo, id) })
		},
	}
}
