package interp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
)

func TestSendBackRejectsInvalidRequestsWithoutRecording(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _ := newValidateEnv(t, b)
			e.toValidate(t)
			for _, ev := range []interp.Event{
				{Step: "build", Attempt: 1},
				{Step: "missing", Attempt: 1, Reason: "again"},
				{Step: "build", Attempt: 2, Reason: "again"},
				{Step: "deliver", Attempt: 1, Reason: "again"},
			} {
				ev.Kind, ev.Repository, ev.Task = interp.StepSentBack, "app", "t7"
				if _, err := e.eng.Advance(context.Background(), ev); !errors.Is(err, interp.ErrEvent) {
					t.Errorf("%+v error = %v", ev, err)
				}
			}
			for name := range e.documents(t) {
				if len(name) >= 10 && name[:10] == "send-back-" {
					t.Fatalf("refused request recorded %s", name)
				}
			}
		})
	}
}

func TestSendBackHonoursHoldAndRejectsChangedReplay(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _ := newValidateEnv(t, b)
			e.toValidate(t)
			feedback := "Resolve the conflict however appropriate."
			e.script.Expect(buildExp(t, 2, head2, "sha256:"+sum([]byte(feedback))))
			e.eng.Hold = func() (bool, error) { return true, nil }
			ev := interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitPaused, Step: "build"})
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitPaused, Step: "build"})
			changed := ev
			changed.Repository, changed.Task, changed.Reason = "app", "t7", "different feedback"
			if _, err := e.eng.Advance(context.Background(), changed); !errors.Is(err, interp.ErrEvent) {
				t.Fatalf("changed replay = %v", err)
			}
			e.eng.Hold = nil
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			// A new request cannot steal a workspace from an unfinished attempt.
			active := interp.Event{Kind: interp.StepSentBack, Repository: "app", Task: "t7", Step: "plan", Attempt: 1, Reason: "change plan"}
			if _, err := e.eng.Advance(context.Background(), active); !errors.Is(err, interp.ErrEvent) {
				t.Fatalf("active task = %v", err)
			}
			attempts, err := e.h.Records.Attempts("app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 4 || attempts[1].Step != "build" || attempts[1].Number != 2 || attempts[1].Outcome != "" {
				t.Fatalf("attempts = %+v", attempts)
			}
		})
	}
}
