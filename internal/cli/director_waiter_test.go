package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// fakeWaiter is the session endpoint's waiter registration as the supervisor reads it: the test moves its state
// and it records each line the supervisor hands it, and each it releases, which a registered waiter takes at once.
type fakeWaiter struct {
	state    session.WaiterState
	handed   []string
	released []string
}

func (f *fakeWaiter) Waiter() session.WaiterState { return f.state }

func (f *fakeWaiter) Hand(line string) error {
	if !f.state.Registered {
		return session.ErrNoWaiter
	}
	f.handed = append(f.handed, line)
	f.state.Handed++
	return nil
}

func (f *fakeWaiter) Release(line string) error {
	f.state.Pending, f.state.Due = false, false
	f.released = append(f.released, line)
	if line == "" {
		return nil
	}
	if !f.state.Registered {
		return session.ErrNoWaiter
	}
	f.state.Handed++
	return nil
}

// hold is a sender's message wake the endpoint holds.
func (f *fakeWaiter) hold() {
	f.state.Held++
	f.state.Pending = true
}

// notices records the wake notice shown, "" once cleared.
type notices struct{ shown []string }

func (n *notices) Show(_ context.Context, text string) error {
	n.shown = append(n.shown, text)
	return nil
}

func (n *notices) Clear(context.Context) error {
	n.shown = append(n.shown, "")
	return nil
}

// handPanes is the Director seam for a Claude session's wakes in command tests: every hand finds no waiter, and
// anything typed is recorded, which a Claude session must never get.
type handPanes struct{ typedPanes }

func (*handPanes) HandWake(context.Context, store.DirectorSession, string, bool, []string) error {
	return session.ErrNoWaiter
}

// waiterWatch records Claude Code session 1 running in a pane in a new store and returns a hooked watch over it
// with a fake waiter, a notice double and a clock the test moves.
func waiterWatch(t *testing.T) (*sessionWatch, *time.Time, *fakeWaiter, *notices, string) {
	t.Helper()
	path := messagingStorePath(t)
	startRuntimeSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"}, "claude")
	clock := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	waiter, notice := &fakeWaiter{}, &notices{}
	w := &sessionWatch{storePath: path, n: 1, grace: time.Minute, hook: true, idleAfter: 5 * time.Minute,
		pane: &typedPanes{}, now: func() time.Time { return clock }, waiter: waiter, notice: notice}
	return w, &clock, waiter, notice, path
}

// endpointPanes is the Director seam for a Claude session's wakes through a real session endpoint: every hand goes
// to session 1's endpoint in dir, and anything typed is recorded, which a Claude session must never get.
type endpointPanes struct {
	typedPanes
	dir string
}

func (p *endpointPanes) HandWake(ctx context.Context, _ store.DirectorSession, line string, hold bool, due []string) error {
	return session.WakeDue(ctx, p.dir, 1, line, hold, due)
}

var _ messaging.EndpointWaker = (*endpointPanes)(nil)

// sendNote sends a note to the Director with `messages send`, through a Claude session's seam that finds no
// waiter, and fails the test on anything typed.
func sendNote(t *testing.T, path, id string) {
	t.Helper()
	panes := &handPanes{}
	sendNoteThrough(t, path, id, panes, &panes.typedPanes)
}

// sendNoteThrough is sendNote through the seam pane, which records what it types in typed.
func sendNoteThrough(t *testing.T, path, id string, pane messaging.DirectorPane, typed *typedPanes) {
	t.Helper()
	app, _, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(envelope(id, "director"))
	app.DirectorPane = pane
	if code := app.Run([]string{"messages", "send", "--store", path, "director", "-"}); code != 0 {
		t.Fatalf("send: exit %d, %s", code, stderr)
	}
	if len(typed.lines) != 0 {
		t.Fatalf("typed %q into a Claude session", typed.lines)
	}
}

func requestReload(t *testing.T, path string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.DirectorReload, error) { return s.RequestDirectorReload(false) }); err != nil {
		t.Fatal(err)
	}
}

// TestARewakeKeepsTheQuietPoint: a wake the waiter took, or a waiter's refresh, is the Director's activity, so a
// plain reload waits for the turn it starts to end, as it waits after a typed wake, and applies once it has.
func TestARewakeKeepsTheQuietPoint(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		w, _, waiter, _, path := waiterWatch(t)
		waiter.state.Registered = true
		w.decide(session.TurnEnded)
		if refresh {
			waiter.state.Refreshed++
		} else {
			waiter.state.Handed++
		}
		requestReload(t, path)
		if got := w.decide(""); got != keepRunning {
			t.Fatalf("refresh %v: a reload applied straight after a rewake: %d", refresh, got)
		}
		// The rewake turn fires UserPromptSubmit too (docs/evidence/director-input-2026-10-27.txt).
		w.decide(session.TurnStarted)
		if got := w.decide(""); got != keepRunning {
			t.Fatalf("refresh %v: a reload applied mid-turn: %d", refresh, got)
		}
		if got := w.decide(session.TurnEnded); got != endForReload {
			t.Fatalf("refresh %v: the rewake turn ended but the reload did not apply: %d", refresh, got)
		}
	}
}

// TestARewakeHandedJustBeforeItsTurnEndsCounts: a short rewake turn's end can reach the supervisor before its next
// read of the waiter; the wake is counted first, so that turn end still makes the Director idle.
func TestARewakeHandedJustBeforeItsTurnEndsCounts(t *testing.T) {
	w, _, waiter, _, path := waiterWatch(t)
	waiter.state.Registered = true
	w.decide(session.TurnEnded)
	waiter.state.Handed++
	requestReload(t, path)
	if got := w.decide(session.TurnEnded); got != endForReload {
		t.Fatalf("the rewake turn's end = %d, want the reload applied", got)
	}
}

// TestAMissedWakeIsHandedOnceWithTheNotice (d8 done-test 1, without a waiter): a wake recorded "no waiter" shows
// the notice while nothing is registered; once a waiter registers the supervisor hands it one wake of the current
// counts and clears the notice, and hands nothing more for that wake.
func TestAMissedWakeIsHandedOnceWithTheNotice(t *testing.T) {
	w, _, waiter, notice, path := waiterWatch(t)
	w.decide(session.TurnEnded)
	sendNote(t, path, "n-1")
	if ws := readWakes(t, path); len(ws) != 1 || ws[0].Decision != store.WakeQueued || ws[0].Reason != "no waiter" {
		t.Fatalf("wakes = %+v, want queued: no waiter", ws)
	}
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(notice.shown, want) || len(waiter.handed) != 0 {
		t.Fatalf("notice %q, handed %q; want the notice %q", notice.shown, waiter.handed, want)
	}
	waiter.state.Registered = true
	w.decide("")
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(waiter.handed, want) {
		t.Fatalf("handed %q, want once %q", waiter.handed, want)
	}
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)", ""}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want shown then cleared", notice.shown)
	}
}

// TestAWaiterEndingWithoutAReplacementShowsTheNotice: a waiter that ends with no newer one, killed for instance,
// leaves an idle Director with unread messages showing the notice, until the unread count reaches zero; a paused
// session shows none.
func TestAWaiterEndingWithoutAReplacementShowsTheNotice(t *testing.T) {
	w, _, waiter, notice, path := waiterWatch(t)
	waiter.state.Registered = true
	w.decide(session.TurnEnded)
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.SendMessage(store.Message{ID: "n-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-1", Type: "note", Text: "x", Sender: store.SenderGuildmaster})
	}); err != nil {
		t.Fatal(err)
	}
	w.decide("")
	if len(notice.shown) != 0 {
		t.Fatalf("a registered waiter showed the notice %q", notice.shown)
	}
	waiter.state.Registered = false
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want %q", notice.shown, want)
	}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.AckMessage(store.Recipient{Role: store.RecipientDirector}, "n-1")
	}); err != nil {
		t.Fatal(err)
	}
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)", ""}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want it cleared at zero unread", notice.shown)
	}
}

type pollResult struct {
	line string
	err  error
}

// waitAs polls session 1's endpoint in dir as waiter id, in the background, until it is handed a line or answered
// with an error, as the wait-wake hook does.
func waitAs(dir, id string) <-chan pollResult {
	out := make(chan pollResult, 1)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			line, err := session.WaitWake(ctx, dir, 1, id)
			cancel()
			if line != "" || err != nil {
				out <- pollResult{line, err}
				return
			}
		}
	}()
	return out
}

// TestASecondWakeWithinTheLeaseIsCaughtUpOnceByTheNextWaiter (d8 review-7 s1): through the real session endpoint,
// a waiter that took a wake is spent, so a second wake inside its lease is recorded "no waiter"; the supervisor
// shows the notice, hands the next waiter to register one catch-up wake of the current counts, clears the notice
// and hands nothing more.
func TestASecondWakeWithinTheLeaseIsCaughtUpOnceByTheNextWaiter(t *testing.T) {
	oldHold, oldLease := session.PollHold, session.WaiterLease
	session.PollHold, session.WaiterLease = 100*time.Millisecond, time.Minute
	t.Cleanup(func() { session.PollHold, session.WaiterLease = oldHold, oldLease })
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	w, _, _, notice, path := waiterWatch(t)
	w.waiter = ep
	panes := &endpointPanes{dir: dir}

	w1 := waitAs(dir, "w-1")
	waitRegistered(t, ep)
	sendNoteThrough(t, path, "n-1", panes, &panes.typedPanes)
	if ws := readWakes(t, path); len(ws) != 1 || ws[0].Decision != store.WakeWoke {
		t.Fatalf("wakes = %+v, want n-1 woke", ws)
	}
	if r := <-w1; r.err != nil || r.line != "guildhall: 1 unread (note 1 from guildmaster)" {
		t.Fatalf("w-1 = %+v, want the line of note 1", r)
	}
	if st := ep.Waiter(); st.Handed != 1 {
		t.Fatalf("after w-1 took its wake, waiter = %+v, want handed 1", st)
	}

	sendNoteThrough(t, path, "n-2", panes, &panes.typedPanes)
	if ws := readWakes(t, path); len(ws) != 2 || ws[1].Decision != store.WakeQueued || ws[1].Reason != "no waiter" {
		t.Fatalf("wakes = %+v, want n-2 queued: no waiter", ws)
	}
	if st := ep.Waiter(); st.Registered {
		t.Fatalf("the spent w-1 still counts as registered within its lease: %+v", st)
	}

	w.decide("")
	if !w.missed {
		t.Fatal("the no-waiter wake was not counted missed")
	}
	if want := []string{"guildhall: 2 unread (note 2 from guildmaster)"}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want %q", notice.shown, want)
	}
	if st := ep.Waiter(); st.Handed != 1 {
		t.Fatalf("with no waiter registered, waiter = %+v, want still handed 1", st)
	}

	w2 := waitAs(dir, "w-2")
	waitRegistered(t, ep)
	w.decide("")
	if r := <-w2; r.err != nil || r.line != "guildhall: 2 unread (note 2 from guildmaster)" {
		t.Fatalf("w-2 = %+v, want the catch-up line of 2 notes", r)
	}
	if want := []string{"guildhall: 2 unread (note 2 from guildmaster)", ""}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want shown then cleared", notice.shown)
	}

	w.decide("")
	if st := ep.Waiter(); st.Handed != 2 {
		t.Fatalf("waiter = %+v, want handed 2: one catch-up wake", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if line, err := session.WaitWake(ctx, dir, 1, "w-3"); err != nil || line != "" {
		t.Fatalf("w-3 = %q, %v; want nothing more handed", line, err)
	}
}

// storeNote stores note id to the Director without waking it, as a sender does before its wake.
func storeNote(t *testing.T, path, id string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.SendMessage(store.Message{ID: id, Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-1", Type: "note", Text: "x", Sender: store.SenderGuildmaster})
	}); err != nil {
		t.Fatal(err)
	}
}

// holdingWatch is waiterWatch with a registered waiter and holding on at the default wake_quiet and
// wake_max_delay, after a turn has ended.
func holdingWatch(t *testing.T) (*sessionWatch, *time.Time, *fakeWaiter, *notices, string) {
	t.Helper()
	w, clock, waiter, notice, path := waiterWatch(t)
	w.quiet, w.maxDelay = 3*time.Second, 15*time.Second
	waiter.state.Registered = true
	w.decide(session.TurnEnded)
	return w, clock, waiter, notice, path
}

// TestABurstIsReleasedOnceAfterTheQuietPeriod (d20): five message wakes a second apart are held, and once none has
// arrived for wake_quiet the supervisor releases one line whose counts are the store's at the release, not at
// any wake.
func TestABurstIsReleasedOnceAfterTheQuietPeriod(t *testing.T) {
	w, clock, waiter, _, path := holdingWatch(t)
	for i := range 5 {
		storeNote(t, path, fmt.Sprintf("n-%d", i+1))
		waiter.hold()
		w.decide("")
		*clock = clock.Add(time.Second)
	}
	// 4s after the first wake, 1s after the newest.
	w.decide("")
	*clock = clock.Add(time.Second)
	w.decide("")
	if len(waiter.released) != 0 {
		t.Fatalf("released %q within the quiet period", waiter.released)
	}
	storeNote(t, path, "n-6")
	*clock = clock.Add(time.Second)
	w.decide("")
	w.decide("")
	if want := []string{"guildhall: 6 unread (note 6 from guildmaster)"}; !slices.Equal(waiter.released, want) || len(waiter.handed) != 0 {
		t.Fatalf("released %q, handed %q; want once %q", waiter.released, waiter.handed, want)
	}
}

// TestAHoldIsReleasedAtTheMaximumDelay (d20): message wakes every 2s never leave wake_quiet without one, so the
// hold is released wake_max_delay after the first.
func TestAHoldIsReleasedAtTheMaximumDelay(t *testing.T) {
	w, clock, waiter, _, path := holdingWatch(t)
	start := *clock
	for i := 0; len(waiter.released) == 0 && i < 30; i++ {
		if i%2 == 0 {
			storeNote(t, path, fmt.Sprintf("n-%d", i))
			waiter.hold()
		}
		w.decide("")
		*clock = clock.Add(time.Second)
	}
	if at := clock.Add(-time.Second).Sub(start); len(waiter.released) != 1 || at != 15*time.Second {
		t.Fatalf("released %q at %s, want once at 15s", waiter.released, at)
	}
}

// TestWakeQuietZeroReleasesNothing (d20): with wake_quiet "0s" the endpoint hands every wake at once, and the
// supervisor makes no release.
func TestWakeQuietZeroReleasesNothing(t *testing.T) {
	w, clock, waiter, _, path := holdingWatch(t)
	w.quiet = 0
	storeNote(t, path, "n-1")
	waiter.hold()
	for range 20 {
		w.decide("")
		*clock = clock.Add(time.Second)
	}
	if len(waiter.released) != 0 {
		t.Fatalf("released %q with holding off", waiter.released)
	}
}

// TestAReleaseWithNoWaiterIsAMissedWake (d20): a release that finds the waiter gone sets the missed wake, so the
// notice shows meanwhile and the next registered waiter is handed one catch-up line.
func TestAReleaseWithNoWaiterIsAMissedWake(t *testing.T) {
	w, clock, waiter, notice, path := holdingWatch(t)
	storeNote(t, path, "n-1")
	waiter.hold()
	w.decide("")
	waiter.state.Registered = false
	*clock = clock.Add(3 * time.Second)
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(waiter.released, want) || !w.missed {
		t.Fatalf("released %q, missed %v; want %q missed", waiter.released, w.missed, want)
	}
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want %q", notice.shown, want)
	}
	waiter.state.Registered = true
	w.decide("")
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)"}; !slices.Equal(waiter.handed, want) {
		t.Fatalf("handed %q, want one catch-up %q", waiter.handed, want)
	}
	if want := []string{"guildhall: 1 unread (note 1 from guildmaster)", ""}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want shown then cleared", notice.shown)
	}
}

// TestAPendingHoldKeepsAReloadWaiting (d20): a held wake keeps the Director from being idle, so a plain reload
// requested while it is pending waits for the release and the turn it starts, and applies once that turn ends.
func TestAPendingHoldKeepsAReloadWaiting(t *testing.T) {
	w, clock, waiter, _, path := holdingWatch(t)
	storeNote(t, path, "n-1")
	waiter.hold()
	requestReload(t, path)
	for range 2 {
		if got := w.decide(""); got != keepRunning {
			t.Fatalf("a reload applied with a wake held: %d", got)
		}
		*clock = clock.Add(time.Second)
	}
	if got := w.decide(session.TurnEnded); got != keepRunning {
		t.Fatalf("a reload applied at a turn end with a wake held: %d", got)
	}
	*clock = clock.Add(time.Second)
	if got := w.decide(""); got != keepRunning || len(waiter.released) != 1 {
		t.Fatalf("at the release: %d, released %q", got, waiter.released)
	}
	w.decide(session.TurnStarted)
	if got := w.decide(session.TurnEnded); got != endForReload {
		t.Fatalf("the rewake turn ended but the reload did not apply: %d", got)
	}
}

// TestNothingLeftAtReleaseClearsTheHold (d20): a hold released once the Director has read everything, with no
// handoff due, is cleared without a wake; with a handoff due it still wakes with the counts.
func TestNothingLeftAtReleaseClearsTheHold(t *testing.T) {
	for due, want := range map[bool]string{false: "", true: "guildhall: 0 unread"} {
		w, clock, waiter, _, _ := holdingWatch(t)
		waiter.hold()
		waiter.state.Due = due
		w.decide("")
		*clock = clock.Add(3 * time.Second)
		w.decide("")
		if !slices.Equal(waiter.released, []string{want}) || w.missed {
			t.Errorf("due %v: released %q, missed %v; want %q", due, waiter.released, w.missed, want)
		}
	}
}
