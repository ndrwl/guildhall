package launch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Cancellation of the ownership wait must not be hidden by a ready record
// read after the caller stopped waiting.
func TestOwnedWaitHonoursCancellationBeforeReady(t *testing.T) {
	for _, when := range []string{"before read", "during read"} {
		t.Run(when, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "store")
				s, err := store.Open(path, store.Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = s.Close() }()
				digest := "sha256:" + strings.Repeat("a", 64)
				_, err = s.CreateTask("task", store.Task{Repository: "app", ID: "t", Title: "task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest})
				if err != nil {
					t.Fatal(err)
				}
				self, err := artisan.Self()
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.StartOwnedAttempt("own", store.Ownership{Repository: "app", TaskID: "t", Step: "build", LauncherIdentity: store.ProcessIdentity(self), Endpoint: "ready"})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c := &child{at: Attempt{Store: path, Key: artisan.AttemptKey{Repository: "app", TaskID: "t", Step: "build", Attempt: 1}}, identity: self, exited: make(chan struct{})}
				done := make(chan error, 1)
				if when == "before read" {
					cancel()
				}
				go func() { done <- (&Process{}).owned(ctx, c) }()
				if when == "during read" {
					// The writer holds the store while owned blocks on its read lock.
					synctest.Wait()
					cancel()
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("owned after cancellation = %v, want cancellation", err)
				}
			})
		})
	}
}
