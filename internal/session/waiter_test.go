package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
)

// shortWaits shortens the poll hold and the waiter lease for t.
func shortWaits(t *testing.T, hold, lease time.Duration) {
	t.Helper()
	oldHold, oldLease := session.PollHold, session.WaiterLease
	session.PollHold, session.WaiterLease = hold, lease
	t.Cleanup(func() { session.PollHold, session.WaiterLease = oldHold, oldLease })
}

// poll is one wait-wake poll of waiter id, in the background.
func poll(dir string, n int, id string) <-chan pollResult {
	out := make(chan pollResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		line, err := session.WaitWake(ctx, dir, n, id)
		out <- pollResult{line, err}
	}()
	return out
}

type pollResult struct {
	line string
	err  error
}

// registered waits until the endpoint reports a registered waiter.
func registered(t *testing.T, ep *session.Endpoint) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ep.Waiter().Registered; time.Sleep(5 * time.Millisecond) { // timing:allow polling the waiter registration; the deadline bounds it.
		if time.Now().After(end) {
			t.Fatal("no waiter registered")
		}
	}
}

// TestAWaiterIsHandedTheWake: a registered waiter's poll returns the line a wake hands it, the wake reports it
// delivered and the endpoint counts it; with no waiter a wake is ErrNoWaiter.
func TestAWaiterIsHandedTheWake(t *testing.T) {
	shortWaits(t, 10*time.Second, 200*time.Millisecond)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	ctx := context.Background()
	if err := session.Wake(ctx, dir, 3, "guildhall: 1 unread (note 1)"); !errors.Is(err, session.ErrNoWaiter) {
		t.Fatalf("a wake with no waiter = %v, want ErrNoWaiter", err)
	}
	got := poll(dir, 3, "w-1")
	registered(t, ep)
	if err := session.Wake(ctx, dir, 3, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if r := <-got; r.err != nil || r.line != "guildhall: 1 unread (note 1)" {
		t.Fatalf("poll = %+v", r)
	}
	if st := ep.Waiter(); st.Handed != 1 || st.Refreshed != 0 {
		t.Fatalf("waiter state = %+v, want one handed", st)
	}
}

// TestANewerWaiterRetiresTheOlder: one waiter per session; a newer registration answers the older waiter's poll
// as retired, and the older one's later polls and refresh too, while the newer one takes the next wake.
func TestANewerWaiterRetiresTheOlder(t *testing.T) {
	shortWaits(t, 10*time.Second, 200*time.Millisecond)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	older := poll(dir, 1, "old")
	registered(t, ep)
	newer := poll(dir, 1, "new")
	if r := <-older; !errors.Is(r.err, session.ErrRetired) {
		t.Fatalf("older poll = %+v, want ErrRetired", r)
	}
	if err := session.Refresh(context.Background(), dir, 1, "old"); !errors.Is(err, session.ErrRetired) {
		t.Fatalf("older refresh = %v, want ErrRetired", err)
	}
	if err := session.Wake(context.Background(), dir, 1, "guildhall: 2 unread (note 2)"); err != nil {
		t.Fatal(err)
	}
	if r := <-newer; r.err != nil || r.line != "guildhall: 2 unread (note 2)" {
		t.Fatalf("newer poll = %+v", r)
	}
}

// TestAWaiterIsRegisteredOnlyWhilePolling: a waiter whose poll ended counts as registered for the lease only, so
// one that ended without a replacement, killed for instance, is seen gone; a refresh is counted as a wake.
func TestAWaiterIsRegisteredOnlyWhilePolling(t *testing.T) {
	shortWaits(t, 50*time.Millisecond, 300*time.Millisecond)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	if r := <-poll(dir, 1, "w-1"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v, want it answered empty", r)
	}
	if !ep.Waiter().Registered {
		t.Fatal("a waiter between polls is not registered")
	}
	// A wake handed between polls is kept for the next.
	if err := session.Wake(context.Background(), dir, 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "w-1"); r.line != "guildhall: 1 unread (note 1)" {
		t.Fatalf("the next poll = %+v, want the line handed between polls", r)
	}
	if err := session.Refresh(context.Background(), dir, 1, "w-1"); err != nil {
		t.Fatal(err)
	}
	if st := ep.Waiter(); st.Handed != 1 || st.Refreshed != 1 {
		t.Fatalf("state = %+v", st)
	}
	for end := time.Now().Add(5 * time.Second); ep.Waiter().Registered; time.Sleep(10 * time.Millisecond) { // timing:allow waiting out the shortened lease.
		if time.Now().After(end) {
			t.Fatal("a waiter that stopped polling is still registered")
		}
	}
	if err := session.Wake(context.Background(), dir, 1, "guildhall: 1 unread (note 1)"); !errors.Is(err, session.ErrNoWaiter) {
		t.Fatalf("a wake after the waiter went = %v, want ErrNoWaiter", err)
	}
}

// TestAWaiterSurvivesTheEndpointDropping: with the endpoint gone a poll is ErrNoSupervisor, which the waiter
// retries, and once a supervisor listens again the same waiter registers and is woken; Close answers an open poll
// at once.
func TestAWaiterSurvivesTheEndpointDropping(t *testing.T) {
	shortWaits(t, 10*time.Second, 200*time.Millisecond)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	open := poll(dir, 1, "w-1")
	registered(t, ep)
	start := time.Now()
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	// The poll is held for 10 s; Close answering it well before that is the point.
	if r := <-open; !errors.Is(r.err, session.ErrRetired) || time.Since(start) >= 10*time.Second {
		t.Fatalf("an open poll at Close = %+v after %s", r, time.Since(start))
	}
	if r := <-poll(dir, 1, "w-1"); !errors.Is(r.err, session.ErrNoSupervisor) {
		t.Fatalf("a poll with no endpoint = %+v, want ErrNoSupervisor", r)
	}
	ep, err = session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	again := poll(dir, 1, "w-1")
	registered(t, ep)
	if err := session.Wake(context.Background(), dir, 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-again; r.err != nil || r.line == "" {
		t.Fatalf("the reconnected waiter = %+v", r)
	}
}
