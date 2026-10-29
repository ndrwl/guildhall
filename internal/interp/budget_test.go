package interp_test

import (
	"errors"
	"fmt"
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
		if strings.Contains(id, store.MessageBudgetRefused) {
			out[id] = text
		}
	}
	return out
}

// TestRunningCapQueuesALaunchUntilOneFinishes: an advance whose next attempt would pass the running cap records
// and launches no attempt, waits queued and sends no message; repeating it keeps its place, and once a running
// attempt finishes the next advance launches it and empties the queue.
func TestRunningCapQueuesALaunchUntilOneFinishes(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})))
			e.eng.Caps = store.AttemptCaps{MaxRunning: 4, MaxPerDay: 40}
			for _, step := range []string{"plan", "build", "review", "fix"} {
				occupy(t, e, step)
			}
			for range 2 {
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitQueued, Step: "plan", Attempt: 1, Position: 1})
			}
			if got := e.attempts(t); len(got) != 0 {
				t.Fatalf("a queued launch recorded attempts %+v", got)
			}
			if got := e.script.Launched(); len(got) != 0 {
				t.Fatalf("a queued launch launched %d attempts", len(got))
			}
			if msgs := budgetMessages(t, e); len(msgs) != 0 {
				t.Fatalf("budget-refused messages = %v, want none for a queued start", msgs)
			}
			q, err := e.h.Records.StartQueue()
			if err != nil || len(q) != 1 || !q[0].Is("app", "t7", "plan") || q[0].Attempt != 1 || q[0].Kind != store.StartByTask {
				t.Fatalf("StartQueue = %+v, %v; want plan's first attempt, queued by task start", q, err)
			}

			if _, err := e.h.Records.FinishAttempt("finish:build", "app", "t8", "build", 1, store.AttemptFailed); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			if q, err := e.h.Records.StartQueue(); err != nil || len(q) != 0 {
				t.Fatalf("StartQueue after the start = %+v, %v; want it empty", q, err)
			}
		})
	}
}

// TestDirectorStageStartsAtTheRunningCap: a Director stage takes no running slot, so it starts while the cap is full
// and does not hold an Artisan start out of it.
func TestDirectorStageStartsAtTheRunningCap(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"))
			e.eng.Caps = store.AttemptCaps{MaxRunning: 1}
			occupy(t, e, "plan")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			if got := e.attempts(t); len(got) != 1 || !got[0].Director {
				t.Fatalf("attempts = %+v, want one Director attempt", got)
			}
			if _, err := e.h.Records.FinishAttempt("finish:plan", "app", "t8", "plan", 1, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			if _, err := e.h.Records.StartCappedAttempt("occupy:build", "app", "t8", "build", e.eng.Caps); err != nil {
				t.Fatalf("an Artisan start with only the grill running = %v, want it admitted", err)
			}
		})
	}
}

// TestDailyCapRefusesUntilTheNextDay: the daily cap counts the day's starts by the records' clock, and the refusal
// message is sent once per day per task.
func TestDailyCapRefusesUntilTheNextDay(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			e := newEnv(t, b, plan)
			now := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)
			e.h.Clock.Set(now)
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

			e.h.Clock.Set(now.Add(time.Hour))
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
		})
	}
}

// TestNoDailyCapStartsPastTheDefault: a daily cap of 0 is no cap, so a launch past the default's count of starts
// in one day is not refused and sends no budget-refused message.
func TestNoDailyCapStartsPastTheDefault(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})))
			e.h.Clock.Set(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
			e.eng.Caps = store.AttemptCaps{MaxRunning: 100, MaxPerDay: 0}
			// 41 starts of app/t8's plan today, the last still running.
			occupy(t, e, "plan")
			for n := range config.DefaultMaxAttemptsPerDay {
				if _, err := e.h.Records.FinishAttempt(fmt.Sprintf("finish:plan:%d", n), "app", "t8", "plan", n+1, store.AttemptFailed); err != nil {
					t.Fatal(err)
				}
				if _, err := e.h.Records.StartAttempt(fmt.Sprintf("occupy:plan:%d", n), "app", "t8", "plan"); err != nil {
					t.Fatal(err)
				}
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			if got := budgetMessages(t, e); len(got) != 0 {
				t.Fatalf("budget-refused messages = %v, want none", got)
			}
		})
	}
}

// TestQueuedRetryStartsOnAPlainAdvance: a retry queued at the running cap sends no reason; a retry of another
// attempt is refused naming the queued one; once a slot frees a plain advance, as the start-queue watcher makes,
// launches it with its first bound reason, which is sent once, at launch.
func TestQueuedRetryStartsOnAPlainAdvance(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Fail("still no plan"))
			plan2.Attempt = 2
			plan3 := planExpectation(t, interptest.Fail("no plan again"))
			plan3.Attempt = 3
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")), plan2, plan3)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.advance(t, retried("plan", 1, "try again"))
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
			e.eng.Caps = store.AttemptCaps{MaxRunning: 1}
			occupy(t, e, "plan")
			queued := interp.Next{Wait: interp.WaitQueued, Step: "plan", Attempt: 3, Position: 1}
			want(t, e.advance(t, retried("plan", 2, "first reason")), queued)
			want(t, e.advance(t, retried("plan", 2, "second reason")), queued)
			if err := advanceErr(t, e, retried("plan", 3, "the queued attempt")); !errors.Is(err, interp.ErrEvent) || !strings.Contains(err.Error(), "attempt 3 of step plan is queued at place 1") {
				t.Fatalf("a retry of another attempt while one is queued = %v, want it refused naming the queued attempt", err)
			}
			if got := e.messages(t)["app.t7.attempt-retried.plan-2"]; got != "" {
				t.Errorf("a queued retry sent the reason %q", got)
			}
			if msgs := budgetMessages(t, e); len(msgs) != 0 {
				t.Fatalf("budget-refused messages = %v, want none", msgs)
			}
			// Still queued: a plain advance keeps the retry's place rather than waiting for the Director.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), queued)
			if _, err := e.h.Records.FinishAttempt("finish:plan", "app", "t8", "plan", 1, store.AttemptFailed); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 3})
			wantRetrySection(t, retryPrompt(t, e, "plan", 3), "first reason")
			if got := e.messages(t)["app.t7.attempt-retried.plan-2"]; got != "first reason" {
				t.Errorf("reason message = %q, want the first reason, sent at launch", got)
			}
			count := len(e.inbox(t))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if len(e.inbox(t)) != count {
				t.Error("a replayed advance sent the reason again")
			}
		})
	}
}

// TestRetryPastTheDailyCapSendsNoReason: a retry refused at the daily cap sends no reason, its budget-refused
// message names step retry, and a later retry launches.
func TestRetryPastTheDailyCapSendsNoReason(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.eng.Caps = store.AttemptCaps{MaxPerDay: 2}
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
	e.eng.Caps.MaxPerDay = 3
	want(t, e.advance(t, retried("plan", 1, "second reason")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
}
