package messaging_test

import (
	"errors"
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
	s, err := store.Open(h.path, store.Options{LockTimeout: time.Second})
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

func TestStoreMailboxNotifiesOnlyAfterCommit(t *testing.T) {
	h := newStoreHarness(t)
	h.CreateTask(t, "app", "t-1")
	h.StartAttempt(t, "app", "t-1", "build")
	var notified []store.Message
	b := &messaging.StoreMailbox{Store: h.s, Notify: func(m store.Message, r store.Receipt) {
		// The message is durable before any notification.
		if got, err := h.s.Message(m.ID); err != nil || got.Sequence != r.Sequence {
			t.Errorf("message during notification = %+v, %v", got, err)
		}
		notified = append(notified, m)
	}}

	e := messagingtest.Envelope("q-1", store.RecipientDirector)
	r, err := b.Send(messagingtest.Director, e)
	if err != nil || len(notified) != 1 || notified[0].ID != "q-1" || notified[0].Sequence != r.Sequence {
		t.Fatalf("Send = %+v, %v; notified %+v", r, err, notified)
	}
	// A retry after a lost result returns the original receipt and notifies again, so an adapter that missed the first notice can catch up.
	if again, err := b.Send(messagingtest.Director, e); err != nil || again != r || len(notified) != 2 {
		t.Fatalf("retry = %+v, %v, notified %d", again, err, len(notified))
	}
	// A refused send stores and notifies nothing.
	bad := messagingtest.Envelope("q-2", store.RecipientDirector)
	bad.Task = "t-9"
	if _, err := b.Send(messagingtest.Director, bad); !errors.Is(err, store.ErrNotFound) || len(notified) != 2 {
		t.Fatalf("send to missing task: err = %v, notified %d", err, len(notified))
	}
}
