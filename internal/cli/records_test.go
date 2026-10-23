package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/messaging/messagingtest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestStoreRecordsContract runs the shared interp.Records suite against the production adapter,
// which opens the store file for each operation (docs/interpreter.md#seams).
func TestStoreRecordsContract(t *testing.T) {
	interptest.RunRecordsContract(t, newStoreRecordsHarness)
}

// TestStoreRecordsHarnessNamesWriters pins the per-operation adapter's concurrency at the two
// processes production has, since bbolt's lock wait is an unordered poll (docs/store.md#one-writer).
func TestStoreRecordsHarnessNamesWriters(t *testing.T) {
	if h := newStoreRecordsHarness(t); h.Writers != 2 || h.Operations == 0 {
		t.Errorf("Writers, Operations = %d, %d; want 2 writers and a named loop", h.Writers, h.Operations)
	}
}

// newStoreRecordsHarness is storeRecords over a new temporary store; its setup writes open the store
// per call as the adapter does, so neither holds the store's lock between operations. It runs the
// concurrent case at two writers, the contention production has, with a shorter loop.
func newStoreRecordsHarness(t *testing.T) interptest.RecordsHarness {
	path := filepath.Join(t.TempDir(), "director.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	clock := interptest.NewClock()
	write := func(fn func(*store.Store) (store.Receipt, error)) (store.Receipt, error) {
		return withStore(path, false, fn)
	}
	return interptest.RecordsHarness{
		Writers: 2, Operations: 20,
		Records: clockedStoreRecords{storeRecords{path: path}, clock}, Clock: clock,
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
		PauseDirector: func() error {
			_, err := write(func(s *store.Store) (store.Receipt, error) { return store.Receipt{}, interptest.PauseStoreDirector(s) })
			return err
		},
	}
}

// clockedStoreRecords is storeRecords whose starts, the receipts the daily cap counts, are stamped by the
// harness's clock rather than wall time.
type clockedStoreRecords struct {
	storeRecords
	clock *interptest.Clock
}

func (p clockedStoreRecords) StartAttempt(m, repo, id, step string) (store.Receipt, error) {
	return withRecords(p.storeRecords, false, p.clock.Now, func(s *store.Store) (store.Receipt, error) { return s.StartAttempt(m, repo, id, step) })
}

func (p clockedStoreRecords) StartCappedAttempt(m, repo, id, step string, caps store.AttemptCaps) (store.Receipt, error) {
	return withRecords(p.storeRecords, false, p.clock.Now, func(s *store.Store) (store.Receipt, error) {
		return s.StartCappedAttempt(m, repo, id, step, caps)
	})
}

// TestStoreRecordsOutboxContract runs the shared Mailbox suite through the production adapter's Send
// (docs/interpreter.md#seams).
func TestStoreRecordsOutboxContract(t *testing.T) {
	messagingtest.RunMailboxContract(t, newStoreOutboxHarness)
}

// storeOutboxHarness sends through storeRecords and reads back through a StoreMailbox opened per call,
// so no operation holds the store's lock between calls and Restart has nothing to drop.
type storeOutboxHarness struct {
	path string
	n    int
}

func newStoreOutboxHarness(t *testing.T) messagingtest.Harness {
	path := filepath.Join(t.TempDir(), "director.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return &storeOutboxHarness{path: path}
}

func (h *storeOutboxHarness) Mailbox() messaging.Mailbox {
	return storeOutbox{storeRecords{path: h.path}}
}

func (h *storeOutboxHarness) mutation() string {
	h.n++
	return fmt.Sprintf("harness-%d", h.n)
}

func (h *storeOutboxHarness) write(t *testing.T, fn func(*store.Store) (store.Receipt, error)) store.Receipt {
	t.Helper()
	r, err := withStore(h.path, false, fn)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (h *storeOutboxHarness) CreateTask(t *testing.T, repository, task string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	rec := store.Task{Repository: repository, ID: task, Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
	m := h.mutation()
	h.write(t, func(s *store.Store) (store.Receipt, error) { return s.CreateTask(m, rec) })
}

func (h *storeOutboxHarness) StartAttempt(t *testing.T, repository, task, step string) int {
	t.Helper()
	m := h.mutation()
	return h.write(t, func(s *store.Store) (store.Receipt, error) { return s.StartAttempt(m, repository, task, step) }).Attempt
}

func (h *storeOutboxHarness) FinishAttempt(t *testing.T, repository, task, step string, number int) {
	t.Helper()
	m := h.mutation()
	h.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt(m, repository, task, step, number, store.AttemptSucceeded)
	})
}

func (h *storeOutboxHarness) Restart(*testing.T) {}

// storeOutbox is the Mailbox the suite drives: Send is storeRecords' own, and List, Read and Ack,
// which the engine never calls, open the store per call as storeRecords does.
type storeOutbox struct{ storeRecords }

func (b storeOutbox) List(to store.Recipient, after uint64, limit int) (messaging.Page, error) {
	return withStore(b.path, true, func(s *store.Store) (messaging.Page, error) {
		return (&messaging.StoreMailbox{Store: s}).List(to, after, limit)
	})
}

func (b storeOutbox) Read(id string) (messaging.Stored, error) {
	return withStore(b.path, true, func(s *store.Store) (messaging.Stored, error) {
		return (&messaging.StoreMailbox{Store: s}).Read(id)
	})
}

func (b storeOutbox) Ack(to store.Recipient, id string) (store.Receipt, error) {
	return withStore(b.path, false, func(s *store.Store) (store.Receipt, error) {
		return (&messaging.StoreMailbox{Store: s}).Ack(to, id)
	})
}
