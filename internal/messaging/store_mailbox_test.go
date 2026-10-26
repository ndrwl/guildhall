package messaging_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/messaging/messagingtest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// storeHarness runs the contract against a real store file, closed and reopened on Restart.
type storeHarness struct {
	path string
	s    *store.Store
	n    int
}

func newStoreHarness(t *testing.T) *storeHarness {
	h := &storeHarness{path: filepath.Join(t.TempDir(), "guildhall.store")}
	h.open(t)
	t.Cleanup(func() { _ = h.s.Close() })
	return h
}

func (h *storeHarness) open(t *testing.T) {
	t.Helper()
	s, err := store.Open(h.path, store.Options{LockTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
}

// mutation returns a fresh mutation id for a harness record.
func (h *storeHarness) mutation() string {
	h.n++
	return fmt.Sprintf("harness-%d", h.n)
}

func (h *storeHarness) Mailbox() messaging.Mailbox { return &messaging.StoreMailbox{Store: h.s} }

func (h *storeHarness) CreateTask(t *testing.T, repository, task string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	rec := store.Task{Repository: repository, ID: task, Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
	if _, err := h.s.CreateTask(h.mutation(), rec); err != nil {
		t.Fatal(err)
	}
}

func (h *storeHarness) StartAttempt(t *testing.T, repository, task, step string) int {
	t.Helper()
	r, err := h.s.StartAttempt(h.mutation(), repository, task, step)
	if err != nil {
		t.Fatal(err)
	}
	return r.Attempt
}

func (h *storeHarness) FinishAttempt(t *testing.T, repository, task, step string, number int) {
	t.Helper()
	if _, err := h.s.FinishAttempt(h.mutation(), repository, task, step, number, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
}

func (h *storeHarness) Restart(t *testing.T) {
	t.Helper()
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(t)
}

func TestStoreMailboxContract(t *testing.T) {
	messagingtest.RunMailboxContract(t, func(t *testing.T) messagingtest.Harness { return newStoreHarness(t) })
}
