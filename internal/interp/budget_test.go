package interp_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// occupy records another task, app/t8, and starts attempt n of its step under no caps.
func occupy(t *testing.T, e *env, step string) {
	t.Helper()
	if _, err := e.h.Records.Task("app", "t8"); errors.Is(err, store.ErrNotFound) {
		task, err := e.h.Records.Task("app", "t7")
		if err != nil {
			t.Fatal(err)
		}
		task.ID = "t8"
		if _, err := e.h.CreateTask("task:app:t8", task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.h.Records.StartAttempt("occupy:"+step, "app", "t8", step); err != nil {
		t.Fatal(err)
	}
}

// budgetMessages are the task's budget-refused messages by id.
func budgetMessages(t *testing.T, e *env) map[string]string {
	t.Helper()
	out := map[string]string{}
	for id, text := range e.messages(t) {
		if strings.Contains(id, interp.BudgetRefused) {
			out[id] = text
		}
	}
	return out
}

// TestRunningCapRefusesALaunchUntilOneFinishes: an advance whose next attempt would pass the running cap records
// and launches nothing, fails naming the rail and tells the Director once; once a running attempt finishes the
// next advance launches it.
func TestRunningCapRefusesALaunchUntilOneFinishes(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})))
			e.eng.Caps = store.AttemptCaps{MaxRunning: 4, MaxPerDay: 40}
			for _, step := range []string{"plan", "build", "review", "fix"} {
				occupy(t, e, step)
			}
			for range 2 {
				err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
				if !errors.Is(err, store.ErrAttemptCap) || !strings.Contains(err.Error(), "guard rail "+config.MaxRunningAttemptsKey) {
					t.Fatalf("fifth launch = %v, want a refusal naming guard rail %s", err, config.MaxRunningAttemptsKey)
				}
			}
			if got := e.attempts(t); len(got) != 0 {
				t.Fatalf("a refused launch recorded attempts %+v", got)
			}
			if got := e.script.Launched(); len(got) != 0 {
				t.Fatalf("a refused launch launched %d attempts", len(got))
			}
			msgs := budgetMessages(t, e)
			if len(msgs) != 1 {
				t.Fatalf("budget-refused messages = %v, want one for two refusals", msgs)
			}
			for id, text := range msgs {
				if !strings.HasPrefix(id, "app.t7."+interp.BudgetRefused+".") || !strings.Contains(text, config.MaxRunningAttemptsKey) || !strings.Contains(text, "plan") || !strings.Contains(text, "guildhall task start") {
					t.Errorf("message %s = %q, want the task's, naming the rail, the step and task start", id, text)
				}
			}

			if _, err := e.h.Records.FinishAttempt("finish:build", "app", "t8", "build", 1, store.AttemptFailed); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
		})
	}
}

// TestDailyCapRefusesUntilTheNextDay: the daily cap counts the day's starts by the records' clock, and the refusal
// message is sent once per day per task.
func TestDailyCapRefusesUntilTheNextDay(t *testing.T) {
	plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
	e := newEnv(t, backends[0], plan)
	now := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)
	e.h.Records.(*interptest.Records).Now = func() time.Time { return now }
	e.eng.Caps = store.AttemptCaps{MaxRunning: 4, MaxPerDay: 2}
	occupy(t, e, "plan")
	if _, err := e.h.Records.FinishAttempt("finish:plan", "app", "t8", "plan", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	occupy(t, e, "build")
	if _, err := e.h.Records.FinishAttempt("finish:build", "app", "t8", "build", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, store.ErrAttemptCap) || !strings.Contains(err.Error(), "guard rail "+config.MaxAttemptsPerDayKey) {
			t.Fatalf("third start of the day = %v, want a refusal naming guard rail %s", err, config.MaxAttemptsPerDayKey)
		}
	}
	if got := budgetMessages(t, e); len(got) != 1 || got["app.t7.budget-refused.2026-10-22"] == "" {
		t.Fatalf("budget-refused messages = %v, want one for 2026-10-22", got)
	}

	now = now.Add(time.Hour)
	occupy(t, e, "review")
	occupy(t, e, "fix")
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, store.ErrAttemptCap) {
		t.Fatalf("third start of the next day = %v, want a refusal", err)
	}
	if got := budgetMessages(t, e); len(got) != 2 || got["app.t7.budget-refused.2026-10-23"] == "" {
		t.Fatalf("budget-refused messages = %v, want a second one for 2026-10-23", got)
	}
	e.eng.Caps.MaxPerDay = 3
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
}

// TestRetryPastACapSendsNoReason: a retry refused at a cap sends no reason, its budget-refused message names
// step retry, and a later retry launches.
func TestRetryPastACapSendsNoReason(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.eng.Caps = store.AttemptCaps{MaxRunning: 1}
	occupy(t, e, "plan")
	if err := advanceErr(t, e, retried("plan", 1, "first reason")); !errors.Is(err, store.ErrAttemptCap) {
		t.Fatalf("retry at the cap = %v", err)
	}
	if got := e.messages(t)["app.t7.attempt-retried.plan-1"]; got != "" {
		t.Errorf("a refused retry sent the reason %q", got)
	}
	msgs := budgetMessages(t, e)
	if len(msgs) != 1 {
		t.Fatalf("budget-refused messages = %v, want one", msgs)
	}
	for id, text := range msgs {
		if !strings.Contains(text, "guildhall step retry for step plan attempt 1") || strings.Contains(text, "task start") {
			t.Errorf("message %s = %q, want it to name step retry for step plan attempt 1", id, text)
		}
	}
	if _, err := e.h.Records.FinishAttempt("finish:plan", "app", "t8", "plan", 1, store.AttemptFailed); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, retried("plan", 1, "second reason")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
}
