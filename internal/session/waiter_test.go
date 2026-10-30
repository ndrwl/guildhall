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

// TestASecondWakeAfterTheWaiterTookOneIsNoWaiter: a waiter that has taken a line exits to wake the Director, so it
// no longer counts as registered even within its lease. A second wake before the next waiter registers is
// ErrNoWaiter, which the sender records "no waiter" with the wake notice, rather than a line handed to a waiter
// that is gone and dropped when the next one registers.
func TestASecondWakeAfterTheWaiterTookOneIsNoWaiter(t *testing.T) {
	// A lease far longer than the test, so only the taken line can end the registration.
	shortWaits(t, 10*time.Second, time.Minute)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	ctx := context.Background()
	first := poll(dir, 1, "w-1")
	registered(t, ep)
	if err := session.Wake(ctx, dir, 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-first; r.err != nil || r.line != "guildhall: 1 unread (note 1)" {
		t.Fatalf("first poll = %+v", r)
	}
	if ep.Waiter().Registered {
		t.Fatal("a waiter that took a line still counts as registered")
	}
	if err := session.Wake(ctx, dir, 1, "guildhall: 2 unread (note 2)"); !errors.Is(err, session.ErrNoWaiter) {
		t.Fatalf("a second wake within the lease = %v, want ErrNoWaiter", err)
	}
	// The next waiter is not handed a stale line: its poll is held until the catch-up wake reaches it.
	next := poll(dir, 1, "w-2")
	registered(t, ep)
	if err := ep.Hand("guildhall: 2 unread (note 2)"); err != nil {
		t.Fatalf("the catch-up wake = %v", err)
	}
	if r := <-next; r.err != nil || r.line != "guildhall: 2 unread (note 2)" {
		t.Fatalf("next poll = %+v", r)
	}
	if st := ep.Waiter(); st.Handed != 2 {
		t.Fatalf("state = %+v, want two handed", st)
	}
}

// TestALineAnOlderWaiterDidNotTakeReachesTheNewer: a wake handed to a waiter between its polls that a newer waiter
// replaces before it polls again is taken by the newer one, not dropped.
func TestALineAnOlderWaiterDidNotTakeReachesTheNewer(t *testing.T) {
	shortWaits(t, 50*time.Millisecond, time.Minute)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	if r := <-poll(dir, 1, "old"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v, want it answered empty", r)
	}
	if err := session.Wake(context.Background(), dir, 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "new"); r.err != nil || r.line != "guildhall: 1 unread (note 1)" {
		t.Fatalf("newer poll = %+v, want the line the older waiter did not take", r)
	}
	if err := session.Refresh(context.Background(), dir, 1, "old"); !errors.Is(err, session.ErrRetired) {
		t.Fatalf("older refresh = %v, want ErrRetired", err)
	}
}

// holdingEndpoint listens on session 1 in a new directory with holding on, a short poll hold and a lease far
// longer than the test, so only a taken line ends a registration.
func holdingEndpoint(t *testing.T) (*session.Endpoint, string) {
	t.Helper()
	shortWaits(t, 50*time.Millisecond, time.Minute)
	dir := shortDir(t)
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	ep.SetHold(true)
	return ep, dir
}

// TestAHeldWakeDoesNotEndAnOpenPoll (d20): a wake that asks to be held is accepted while a waiter is registered,
// but the waiter's open poll ends with nothing, and the endpoint reports the wake held and pending.
func TestAHeldWakeDoesNotEndAnOpenPoll(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	open := poll(dir, 1, "w-1")
	registered(t, ep)
	if err := session.WakeDue(context.Background(), dir, 1, "guildhall: 1 unread (note 1)", true, nil); err != nil {
		t.Fatalf("a held wake = %v", err)
	}
	if r := <-open; r.err != nil || r.line != "" {
		t.Fatalf("the open poll = %+v, want it answered empty", r)
	}
	if st := ep.Waiter(); st.Held != 1 || !st.Pending || st.Due || st.Handed != 0 {
		t.Fatalf("state = %+v, want one held wake pending", st)
	}
}

// TestReleaseHandsTheLineWithTheMergedDues (d20): Release hands its line with the union of the held wakes'
// handoff reasons, in first-seen order, and the waiter takes it; the hold is then cleared.
func TestReleaseHandsTheLineWithTheMergedDues(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	ctx := context.Background()
	if r := <-poll(dir, 1, "w-1"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v", r)
	}
	for _, due := range [][]string{{"context 72%"}, nil, {"idle 5m0s", "context 72%"}} {
		if err := session.WakeDue(ctx, dir, 1, "guildhall: 1 unread (note 1)", true, due); err != nil {
			t.Fatal(err)
		}
	}
	if st := ep.Waiter(); st.Held != 3 || !st.Pending || !st.Due {
		t.Fatalf("state = %+v, want three held with dues", st)
	}
	if err := ep.Release("guildhall: 4 unread (note 4)"); err != nil {
		t.Fatalf("release = %v", err)
	}
	if r := <-poll(dir, 1, "w-1"); r.err != nil || r.line != "guildhall: 4 unread (note 4); handoff due (context 72%, idle 5m0s)" {
		t.Fatalf("poll = %+v", r)
	}
	if st := ep.Waiter(); st.Held != 3 || st.Pending || st.Due || st.Handed != 1 {
		t.Fatalf("state = %+v, want the hold cleared and one handed", st)
	}
}

// TestAHeldWakeWithNoWaiterIsNotDelivered (d20): with no waiter registered a held wake is ErrNoWaiter, as a
// handed one is, and nothing is held.
func TestAHeldWakeWithNoWaiterIsNotDelivered(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	if err := session.WakeDue(context.Background(), dir, 1, "guildhall: 1 unread (note 1)", true, []string{"context 72%"}); !errors.Is(err, session.ErrNoWaiter) {
		t.Fatalf("a held wake with no waiter = %v, want ErrNoWaiter", err)
	}
	if st := ep.Waiter(); st.Held != 0 || st.Pending || st.Due {
		t.Fatalf("state = %+v, want nothing held", st)
	}
}

// TestAHeldWakeIsReleasedToTheNewerWaiter (d20): the hold is the session's, not one waiter's, so a wake held while
// an older waiter was registered is released to the newer one that replaced it.
func TestAHeldWakeIsReleasedToTheNewerWaiter(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	if r := <-poll(dir, 1, "old"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v", r)
	}
	if err := session.WakeDue(context.Background(), dir, 1, "guildhall: 1 unread (note 1)", true, []string{"context 72%"}); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "new"); r.err != nil || r.line != "" {
		t.Fatalf("the newer waiter's poll before the release = %+v", r)
	}
	if err := ep.Release("guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "new"); r.err != nil || r.line != "guildhall: 1 unread (note 1); handoff due (context 72%)" {
		t.Fatalf("newer poll = %+v", r)
	}
}

// TestReleaseWithNoWaiterClearsTheHold (d20): a release once the waiter has gone, here by taking a line, is
// ErrNoWaiter and still clears the hold; an empty line clears it and hands nothing.
func TestReleaseWithNoWaiterClearsTheHold(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	ctx := context.Background()
	open := poll(dir, 1, "w-1")
	registered(t, ep)
	if err := session.WakeDue(ctx, dir, 1, "guildhall: 1 unread (note 1)", true, []string{"context 72%"}); err != nil {
		t.Fatal(err)
	}
	if r := <-open; r.err != nil || r.line != "" {
		t.Fatalf("open poll = %+v", r)
	}
	if err := session.Wake(ctx, dir, 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "w-1"); r.line != "guildhall: 1 unread (note 1)" {
		t.Fatalf("poll = %+v, want the line handed at once", r)
	}
	if err := ep.Release("guildhall: 1 unread (note 1)"); !errors.Is(err, session.ErrNoWaiter) {
		t.Fatalf("a release with no waiter = %v, want ErrNoWaiter", err)
	}
	if st := ep.Waiter(); st.Pending || st.Due {
		t.Fatalf("state = %+v, want the hold cleared", st)
	}
	if r := <-poll(dir, 1, "w-2"); r.err != nil || r.line != "" {
		t.Fatalf("the next waiter = %+v, want nothing handed", r)
	}
	if err := session.WakeDue(ctx, dir, 1, "guildhall: 1 unread (note 1)", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := ep.Release(""); err != nil || ep.Waiter().Pending {
		t.Fatalf("an empty release = %v, state %+v", err, ep.Waiter())
	}
	if r := <-poll(dir, 1, "w-2"); r.err != nil || r.line != "" {
		t.Fatalf("after an empty release the waiter = %+v, want nothing handed", r)
	}
}

// TestHoldOffHandsAHeldWakeAtOnce (d20): with holding off, which an endpoint is until SetHold, a wake that asks
// to be held is handed at once with its dues, as any wake is.
func TestHoldOffHandsAHeldWakeAtOnce(t *testing.T) {
	ep, dir := holdingEndpoint(t)
	ep.SetHold(false)
	open := poll(dir, 1, "w-1")
	registered(t, ep)
	if err := session.WakeDue(context.Background(), dir, 1, "guildhall: 1 unread (note 1)", true, []string{"context 72%"}); err != nil {
		t.Fatal(err)
	}
	if r := <-open; r.err != nil || r.line != "guildhall: 1 unread (note 1); handoff due (context 72%)" {
		t.Fatalf("poll = %+v, want the line handed at once", r)
	}
	if st := ep.Waiter(); st.Held != 0 || st.Pending || st.Handed != 1 {
		t.Fatalf("state = %+v", st)
	}
}

// TestAReplacingLineKeepsTheUntakenDues (d20): a wake that replaces a line the waiter has not taken yet keeps that
// line's handoff reasons with its own, so a reminder is not lost; a taken line's reasons are not handed again.
func TestAReplacingLineKeepsTheUntakenDues(t *testing.T) {
	_, dir := holdingEndpoint(t)
	ctx := context.Background()
	if r := <-poll(dir, 1, "w-1"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v", r)
	}
	if err := session.WakeDue(ctx, dir, 1, "guildhall: 1 unread (note 1)", false, []string{"context 72%"}); err != nil {
		t.Fatal(err)
	}
	if err := session.WakeDue(ctx, dir, 1, "guildhall: 2 unread (note 2)", false, []string{"idle 5m0s"}); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "w-1"); r.line != "guildhall: 2 unread (note 2); handoff due (context 72%, idle 5m0s)" {
		t.Fatalf("poll = %+v, want the newer line with both reasons", r)
	}
	if r := <-poll(dir, 1, "w-2"); r.err != nil || r.line != "" {
		t.Fatalf("a held poll = %+v", r)
	}
	if err := session.WakeDue(ctx, dir, 1, "guildhall: 2 unread (note 2)", false, nil); err != nil {
		t.Fatal(err)
	}
	if r := <-poll(dir, 1, "w-2"); r.line != "guildhall: 2 unread (note 2)" {
		t.Fatalf("poll = %+v, want a taken line's reasons not handed again", r)
	}
}
