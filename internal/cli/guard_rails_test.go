package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// lastSequence is the store's newest receipt sequence, so a test can see that a refusal wrote nothing.
func lastSequence(t *testing.T, path string) uint64 {
	t.Helper()
	v, err := withStore(path, true, func(s *store.Store) (store.DirectorView, error) { return s.DirectorView() })
	if err != nil {
		t.Fatal(err)
	}
	return v.AsOf.Sequence
}

// TestUnlistedRepositoryRefusedByEveryCommand: every command that acts on a repository refuses one the config
// does not list, naming the repositories guard rail, and writes nothing.
func TestUnlistedRepositoryRefusedByEveryCommand(t *testing.T) {
	d := newRunDirector(t)
	// A task of an unlisted repository, recorded behind the config's back, so only the rail refuses it.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		task, err := s.Task("app", "t-1")
		if err != nil {
			return store.Receipt{}, err
		}
		task.Repository, task.ID = "other", "t-9"
		return s.CreateTask("other-task", task)
	}); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--store", d.store, "--config", d.config, "--state", d.state, "--repo", "other"}
	note := `{"schema":"guildhall.message/1","id":"n-other","recipient":"director","repository":"other","task":"t-9","type":"note","text":"Hello."}`
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"task new", append([]string{"task", "new", "--title", "Elsewhere"}, append(flags[:4:4], "--repo", "other", "t-10")...), ""},
		{"task start", append([]string{"task", "start"}, append(flags, "t-9")...), ""},
		{"step done", append([]string{"step", "done"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1", "--result", "result.json")...), ""},
		{"step finish", append([]string{"step", "finish"}, append(flags, "--task", "t-9", "--step", "grill", "--attempt", "1", "--summary", "Abandoned.", "--failed")...), ""},
		{"step retry", append([]string{"step", "retry"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1")...), ""},
		{"step park", append([]string{"step", "park"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1", "--reason", "Hold.")...), ""},
		{"step unpark", append([]string{"step", "unpark"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1")...), ""},
		{"messages send", []string{"messages", "send", "--store", d.store, "--config", d.config, "director", "-"}, note},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := lastSequence(t, d.store)
			app, stdout, stderr := newTestApp(nil, nil)
			app.Stdin = strings.NewReader(tc.stdin)
			if code := app.Run(tc.args); code != 1 || !strings.Contains(stderr.String(), `guard rail repositories: the config does not list repository "other"`) {
				t.Fatalf("exit %d, stdout %s, stderr %s; want exit 1 naming the repositories guard rail", code, stdout, stderr)
			}
			if after := lastSequence(t, d.store); after != before {
				t.Errorf("a refusal wrote the store: sequence %d, then %d", before, after)
			}
		})
	}
}

// TestRunningCapQueuesTheFifthLaunch: with the default caps, task start queues a launch while four attempts run,
// exiting 0 with its place and sending no message; a p0 queued later goes first, a task whose own attempt just
// finished queues behind both, the start-queue watcher starts the front once a slot frees, and nothing while the
// session is paused.
func TestRunningCapQueuesTheFifthLaunch(t *testing.T) {
	d := newRunDirector(t)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		task, err := s.Task("app", "t-1")
		if err != nil {
			return store.Receipt{}, err
		}
		task.ID = "t-2"
		if _, err := s.CreateTask("t-2", task); err != nil {
			return store.Receipt{}, err
		}
		for i := range config.DefaultMaxRunningAttempts {
			if _, err := s.StartAttempt(fmt.Sprintf("occupy-%d", i), "app", "t-2", fmt.Sprintf("step-%d", i)); err != nil {
				return store.Receipt{}, err
			}
		}
		return store.Receipt{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	// t-1 waits at the default priority; t-3, raised to p0 and queued after it, goes ahead of it.
	newTask(t, d.store, d.config, "app", "Another", "t-3")
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskPriority("priority-t-3", "app", "t-3", store.PriorityUrgent)
	}); err != nil {
		t.Fatal(err)
	}
	start := append([]string{"task", "start"}, d.flags("t-1")...)
	for range 2 {
		code, out, stderr := d.run(t, start...)
		const want = `{"wait":"queued","step":"plan","attempt":1,"position":1}` + "\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("fifth launch: exit %d, %q, stderr %s; want exit 0 and %s", code, out, stderr, want)
		}
		if next := readFileString(t, filepath.Join(d.state, "app", "t-1", nextFile)); next != want {
			t.Fatalf("next.json = %q, want %q", next, want)
		}
	}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-3")...)...); code != 0 || out != `{"wait":"queued","step":"plan","attempt":1,"position":1}`+"\n" {
		t.Fatalf("a p0 start queued after a p3: exit %d, %q, stderr %s; want it first", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("a queued launch recorded %+v", got)
	}
	if got := budgetRefused(t, d.store); len(got) != 0 {
		t.Fatalf("budget-refused messages = %+v, want none for a queued start", got)
	}
	// A retry naming the queued attempt is refused, naming the queue entry.
	if code, _, stderr := d.run(t, append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "Again.")...)...); code != 1 || !strings.Contains(stderr, "attempt 1 of step plan is queued at place 2") {
		t.Fatalf("a conflicting retry: exit %d, stderr %s; want exit 1 naming the queued attempt", code, stderr)
	}

	status, err := readDirectorStatus(context.Background(), d.store, d.state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var queue []string
	for _, e := range status.Session.StartQueue {
		queue = append(queue, fmt.Sprintf("%d:%s/%s:%d:%s:%s", e.Position, e.Task, e.Step, e.Attempt, e.Priority, e.Request))
	}
	if want := []string{"1:t-3/plan:1:p0:start", "2:t-1/plan:1:p3:start"}; !slices.Equal(queue, want) {
		t.Errorf("status start_queue = %v, want %v", queue, want)
	}
	for _, item := range status.NeedsMe {
		if item.TaskID == "t-1" || item.TaskID == "t-3" {
			t.Errorf("a queued start is in needs_me: %+v", item)
		}
	}

	// t-2's attempt finishes and frees a slot, but its own next start queues behind the two waiting, as does
	// t-1's, which is not the front.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.FinishAttempt("finish-occupied", "app", "t-2", "step-0", 1, store.AttemptSucceeded); err != nil {
			return store.Receipt{}, err
		}
		return s.StartCappedAttempt("t-2-next", "app", "t-2", "step-0", store.AttemptCaps{MaxRunning: config.DefaultMaxRunningAttempts, Queue: &store.StartRequest{Kind: store.StartByEngine}})
	}); err == nil {
		t.Fatal("t-2's next start after its attempt finished was admitted, want it queued")
	} else if q, ok := errors.AsType[*store.StartQueued](err); !ok || q.Position != 3 {
		t.Fatalf("t-2's next start = %v, want it queued at place 3, behind the entries already waiting", err)
	}
	if code, out, stderr := d.run(t, start...); code != 0 || out != `{"wait":"queued","step":"plan","attempt":1,"position":2}`+"\n" {
		t.Fatalf("a start behind the front with a free slot: exit %d, %q, stderr %s; want it queued at place 2", code, out, stderr)
	}

	// The start-queue watcher's round starts the front, t-3, and stops at t-1, which stays queued. Its advance here
	// starts in the store as an advance would, so the round does not race a fake Artisan's finish.
	var advanced []string
	advance := func(r taskRef) (interp.Next, bool, error) {
		advanced = append(advanced, r.task)
		n, err := withStore(d.store, false, func(s *store.Store) (interp.Next, error) {
			q, err := s.StartQueue()
			if err != nil {
				return interp.Next{}, err
			}
			i := slices.IndexFunc(q, func(e store.QueuedStart) bool { return e.TaskID == r.task })
			_, err = s.StartCappedAttempt("round-"+r.task, r.repo, r.task, q[i].Step, store.AttemptCaps{MaxRunning: config.DefaultMaxRunningAttempts, Queue: &store.StartRequest{Kind: store.StartByTask}})
			if qs, ok := errors.AsType[*store.StartQueued](err); ok {
				return interp.Next{Wait: interp.WaitQueued, Step: qs.Entry.Step, Attempt: qs.Entry.Attempt, Position: qs.Position}, nil
			}
			return interp.Next{Wait: interp.WaitAttempt, Step: q[i].Step, Attempt: 1}, err
		})
		return n, true, err
	}
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	startQueueRound(d.store, advance, logf)
	if want := []string{"t-3", "t-1"}; !slices.Equal(advanced, want) || len(logged) != 0 {
		t.Fatalf("the round advanced %v and logged %v; want %v and nothing", advanced, logged, want)
	}

	// Now the front, t-1 starts with a plain task start once a slot is free.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt("finish-t-3", "app", "t-3", "plan", 1, store.AttemptSucceeded)
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := d.run(t, start...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("the front's start once a slot is free: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)

	// While the session is paused the watcher starts nothing, though a slot is free.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.FinishAttempt("finish-occupied-1", "app", "t-2", "step-1", 1, store.AttemptSucceeded); err != nil {
			return store.Receipt{}, err
		}
		return store.Receipt{}, interptest.PauseStoreDirector(s)
	}); err != nil {
		t.Fatal(err)
	}
	advanced = nil
	startQueueRound(d.store, advance, logf)
	if len(advanced) != 0 {
		t.Fatalf("a paused session's round advanced %v", advanced)
	}
}

// TestStartQueueRoundDropsAStartNoLongerDue: an entry whose advance neither starts nor queues it leaves the queue,
// logged, so it does not hold the front.
func TestStartQueueRoundDropsAStartNoLongerDue(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Another", "t-3")
	caps := store.AttemptCaps{MaxRunning: 1, Queue: &store.StartRequest{Kind: store.StartByTask}}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartCappedAttempt("t-1-plan", "app", "t-1", "other", caps); err != nil {
			return store.Receipt{}, err
		}
		return s.StartCappedAttempt("t-3-plan", "app", "t-3", "plan", caps)
	}); !errors.Is(err, store.ErrStartQueued) {
		t.Fatalf("t-3's start = %v, want it queued", err)
	}
	var logged []string
	startQueueRound(d.store, func(taskRef) (interp.Next, bool, error) {
		return interp.Next{Wait: interp.WaitDependencies, Step: "plan"}, true, nil
	}, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	q, err := withStore(d.store, true, func(s *store.Store) ([]store.QueuedStart, error) { return s.StartQueue() })
	if err != nil || len(q) != 0 {
		t.Fatalf("StartQueue = %+v, %v; want the entry dropped", q, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "app/t-3: attempt 1 of step plan left the start queue: the advance waits dependencies") {
		t.Errorf("logged %q", logged)
	}

	// An advance that now queues another step of the task does not keep the old entry at the front.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartCappedAttempt("t-3-plan", "app", "t-3", "plan", caps)
	}); !errors.Is(err, store.ErrStartQueued) {
		t.Fatalf("t-3's start again = %v, want it queued", err)
	}
	logged = nil
	startQueueRound(d.store, func(taskRef) (interp.Next, bool, error) {
		return interp.Next{Wait: interp.WaitQueued, Step: "build", Attempt: 1, Position: 2}, true, nil
	}, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	if q, err := withStore(d.store, true, func(s *store.Store) ([]store.QueuedStart, error) { return s.StartQueue() }); err != nil || len(q) != 0 {
		t.Fatalf("StartQueue = %+v, %v; want the plan entry dropped", q, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "the advance waits queued for step build") {
		t.Errorf("logged %q", logged)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt("t-1-other", "app", "t-1", "other", 1, store.AttemptSucceeded)
	}); err != nil {
		t.Fatal(err)
	}
}

// budgetRefused lists the budget-refused messages to the Director in the store at path.
func budgetRefused(t *testing.T, path string) []store.Message {
	t.Helper()
	refused, err := withStore(path, true, func(s *store.Store) ([]store.Message, error) {
		inbox, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		if err != nil {
			return nil, err
		}
		var out []store.Message
		for _, m := range inbox {
			if m.Type == store.MessageBudgetRefused {
				out = append(out, m)
			}
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return refused
}
