package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// capStore opens a store whose clock the test moves.
func capStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "guildhall.store"), Options{LockTimeout: 30 * time.Second, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func wantRefusal(t *testing.T, what string, err error, c Cap, count, limit int) {
	t.Helper()
	refusal, ok := errors.AsType[*CapRefusal](err)
	if !errors.Is(err, ErrAttemptCap) || !ok || refusal.Cap != c || refusal.Count != count || refusal.Limit != limit {
		t.Fatalf("%s = %v, want a %s cap refusal at %d of %d", what, err, c, count, limit)
	}
}

func TestRunningCapRefusesAndAdmitsAfterAFinish(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	caps := AttemptCaps{MaxRunning: 4, MaxPerDay: 40}
	for i := range 5 {
		repo := []string{"app", "site"}[i%2]
		mustCreate(t, s, fmt.Sprintf("task-%d", i), sampleTask(repo, fmt.Sprintf("t%d", i)))
	}
	for i := range 4 {
		repo := []string{"app", "site"}[i%2]
		if _, err := s.StartCappedAttempt(fmt.Sprintf("start-%d", i), repo, fmt.Sprintf("t%d", i), "build", caps); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	_, err := s.StartCappedAttempt("start-4", "app", "t4", "build", caps)
	wantRefusal(t, "fifth start", err, CapRunning, 4, 4)
	if a, _ := s.Attempts("app", "t4"); len(a) != 0 {
		t.Fatalf("a refused start recorded %v", a)
	}
	if _, err := s.Receipt("start-4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused start left a receipt: %v", err)
	}
	// A replay of an admitted start is never refused, whatever the caps are now.
	if r, err := s.StartCappedAttempt("start-0", "app", "t0", "build", AttemptCaps{MaxRunning: 1, MaxPerDay: 1}); err != nil || r.Attempt != 1 {
		t.Fatalf("replayed start = %+v, %v", r, err)
	}
	if _, err := s.FinishAttempt("finish-1", "site", "t1", "build", 1, AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if r, err := s.StartCappedAttempt("start-4", "app", "t4", "build", caps); err != nil || r.Attempt != 1 {
		t.Fatalf("start after a finish = %+v, %v", r, err)
	}
	u, err := s.AttemptUsage()
	if err != nil || u.Running != 4 || u.Today != 5 || !u.Day.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("AttemptUsage = %+v, %v", u, err)
	}
}

func TestDailyCapCountsTheUTCDayByTheStoreClock(t *testing.T) {
	now := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	caps := AttemptCaps{MaxRunning: 4, MaxPerDay: 3}
	mustCreate(t, s, "task", sampleTask("app", "t1"))
	start := func(n int) error {
		_, err := s.StartCappedAttempt(fmt.Sprintf("start-%d", n), "app", "t1", "build", caps)
		if err == nil {
			_, err = s.FinishAttempt(fmt.Sprintf("finish-%d", n), "app", "t1", "build", n, AttemptFailed)
		}
		return err
	}
	for n := 1; n <= 3; n++ {
		if err := start(n); err != nil {
			t.Fatalf("start %d: %v", n, err)
		}
	}
	err := start(4)
	wantRefusal(t, "fourth start of the day", err, CapDaily, 3, 3)
	if r := new(CapRefusal); !errors.As(err, &r) || !r.Day.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("refusal day = %v", r.Day)
	}
	now = time.Date(2026, 10, 23, 0, 0, 1, 0, time.UTC)
	if u, err := s.AttemptUsage(); err != nil || u.Today != 0 || u.Running != 0 {
		t.Fatalf("usage on the next day = %+v, %v", u, err)
	}
	if err := start(4); err != nil {
		t.Fatalf("first start of the next day: %v", err)
	}
	// Starts with no caps are counted too.
	if _, err := s.StartAttempt("start-5", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.AttemptUsage(); err != nil || u.Today != 2 || u.Running != 1 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
}

func TestCapRefusalKeepsStartAttemptRules(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	if _, err := s.StartCappedAttempt("start-x", "app", "t9", "build", AttemptCaps{MaxRunning: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("start of an unknown task = %v", err)
	}
	mustCreate(t, s, "task", sampleTask("app", "t1"))
	if _, err := s.StartCappedAttempt("start-1", "app", "t1", "build", AttemptCaps{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCappedAttempt("start-2", "app", "t1", "build", AttemptCaps{MaxRunning: 5}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("start while the step runs = %v", err)
	}
}

// TestQueuedStartWritesOnlyItsEntry: a queued start records its entry under its own mutation id and leaves the start's
// id free, admission removes the entry with the start's receipt, a Director stage is left out of Running, and
// LeaveStartQueue removes only the entry it names.
func TestQueuedStartWritesOnlyItsEntry(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	for _, id := range []string{"t1", "t2", "t3"} {
		mustCreate(t, s, "task-"+id, sampleTask("app", id))
	}
	caps := AttemptCaps{MaxRunning: 1, Queue: &StartRequest{Kind: StartByRetry, Retried: 1}}
	if _, err := s.StartCappedAttempt("start-t1", "app", "t1", "build", caps); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDirectorAttempt("grill-t3", "app", "t3", "grill", true); err != nil {
		t.Fatal(err)
	}
	if u, err := s.AttemptUsage(); err != nil || u.Running != 1 || u.Today != 2 {
		t.Fatalf("AttemptUsage = %+v, %v; want the Director stage counted today but not running", u, err)
	}
	_, err := s.StartCappedAttempt("start-t2", "app", "t2", "build", caps)
	q, ok := errors.AsType[*StartQueued](err)
	if !ok || q.Position != 1 || q.Entry.Attempt != 1 || q.Entry.Kind != StartByRetry || q.Entry.Retried != 1 || !q.Entry.QueuedAt.Equal(now) {
		t.Fatalf("start past the running cap = %v, want it queued at place 1 under the retry", err)
	}
	if _, err := s.Receipt("start-t2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a queued start left its own receipt: %v", err)
	}
	r, err := s.Receipt(QueueStartMutation("app", "t2", "build", 1))
	if err != nil || r.Operation != "queue-start" || r.Record != StartQueueRecord("build") || r.Attempt != 1 {
		t.Fatalf("queue receipt = %+v, %v", r, err)
	}
	if _, err := s.StartCappedAttempt("start-t2", "app", "t2", "build", AttemptCaps{MaxRunning: 1, Queue: &StartRequest{Kind: StartByRetry}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a retry start naming no retried attempt = %v, want it refused", err)
	}

	// Leaving names the entry by its queue time: a stale one changes nothing.
	stale := q.Entry
	stale.QueuedAt = stale.QueuedAt.Add(-time.Minute)
	if _, err := s.LeaveStartQueue(LeaveStartQueueMutation(stale), stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StartQueue(); len(got) != 1 {
		t.Fatalf("StartQueue after a stale leave = %+v, want the entry kept", got)
	}
	if _, err := s.FinishAttempt("finish-t1", "app", "t1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if r, err := s.StartCappedAttempt("start-t2", "app", "t2", "build", caps); err != nil || r.Attempt != 1 {
		t.Fatalf("the front's start = %+v, %v", r, err)
	}
	if got, _ := s.StartQueue(); len(got) != 0 {
		t.Fatalf("StartQueue after admission = %+v, want it empty", got)
	}

	// Queued again and then left: the entry goes.
	_, err = s.StartCappedAttempt("start-t1-review", "app", "t1", "review", caps)
	q, ok = errors.AsType[*StartQueued](err)
	if !ok {
		t.Fatalf("start past the running cap = %v", err)
	}
	if _, err := s.LeaveStartQueue(LeaveStartQueueMutation(q.Entry), q.Entry); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StartQueue(); len(got) != 0 {
		t.Fatalf("StartQueue after leaving = %+v, want it empty", got)
	}
}
