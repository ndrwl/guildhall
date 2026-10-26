package interptest

import (
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// runRejectionContract exercises rejection through the consumer Records boundary.
func runRejectionContract(t *testing.T, newHarness func(*testing.T) RecordsHarness) {
	t.Helper()
	for _, initial := range []store.AttemptOutcome{"", store.AttemptSucceeded, store.AttemptFailed, store.AttemptStopped} {
		t.Run("reject/"+string(initial), func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.Records.StartAttempt("start", "app", "t7", "review"); err != nil {
				t.Fatal(err)
			}
			if initial != "" {
				if _, err := h.Records.FinishAttempt("launcher", "app", "t7", "review", 1, initial); err != nil {
					t.Fatal(err)
				}
			}
			r, err := h.Records.RejectAttempt("reject", "app", "t7", "review", 1, "missing outcome")
			if initial == store.AttemptFailed || initial == store.AttemptStopped {
				if !errors.Is(err, store.ErrInvalid) {
					t.Fatalf("reject completed failure=%v", err)
				}
				return
			}
			if err != nil || r.Operation != "reject-attempt" || r.Record != "attempt/review/1" {
				t.Fatalf("receipt=%+v, %v", r, err)
			}
			a, err := h.Records.Attempts("app", "t7")
			if err != nil || len(a) != 1 || a[0].Outcome != store.AttemptFailed || a[0].IntakeRejection != "missing outcome" {
				t.Fatalf("attempts=%+v, %v", a, err)
			}
			if again, err := h.Records.RejectAttempt("reject", "app", "t7", "review", 1, "missing outcome"); err != nil || again != r {
				t.Fatalf("replay=%+v, %v", again, err)
			}
			if _, err := h.Records.RejectAttempt("reject", "app", "t7", "review", 1, "changed"); !errors.Is(err, store.ErrMutationConflict) {
				t.Fatalf("changed request=%v", err)
			}
			if _, err := h.Records.RejectAttempt("other", "app", "t7", "review", 1, "changed"); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("second rejection=%v", err)
			}
			if next, err := h.Records.StartAttempt("retry", "app", "t7", "review"); err != nil || next.Attempt != 2 {
				t.Fatalf("retry=%+v, %v", next, err)
			}
			if again, err := h.Records.RejectAttempt("reject", "app", "t7", "review", 1, "missing outcome"); err != nil || again != r {
				t.Fatalf("replay after retry=%+v, %v", again, err)
			}
			if _, err := h.Records.RejectAttempt("missing", "app", "t7", "review", 99, "reason"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("missing=%v", err)
			}
			if _, err := h.Records.RejectAttempt("blank", "app", "t7", "review", 2, " "); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("blank=%v", err)
			}
		})
	}
}
