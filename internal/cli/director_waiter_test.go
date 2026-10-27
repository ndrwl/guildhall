package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// fakeWaiter is the session endpoint's waiter registration as the supervisor reads it: the test moves its state
// and it records each line the supervisor hands it.
type fakeWaiter struct {
	state  session.WaiterState
	handed []string
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

func (*handPanes) HandWake(context.Context, store.DirectorSession, string) error {
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
	w := &sessionWatch{storePath: path, state: t.TempDir(), n: 1, grace: time.Minute, hook: true, idleAfter: 5 * time.Minute,
		pane: &typedPanes{}, now: func() time.Time { return clock }, waiter: waiter, notice: notice}
	return w, &clock, waiter, notice, path
}

// sendNote sends a note to the Director with `messages send`, through a Claude session's seam that finds no
// waiter, and fails the test on anything typed.
func sendNote(t *testing.T, path, id string) {
	t.Helper()
	panes := &handPanes{}
	app, _, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(envelope(id, "director"))
	app.DirectorPane = panes
	if code := app.Run([]string{"messages", "send", "--store", path, "director", "-"}); code != 0 {
		t.Fatalf("send: exit %d, %s", code, stderr)
	}
	if len(panes.lines) != 0 {
		t.Fatalf("typed %q into a Claude session", panes.lines)
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
	if want := []string{"guildhall: 1 unread (note 1)"}; !slices.Equal(notice.shown, want) || len(waiter.handed) != 0 {
		t.Fatalf("notice %q, handed %q; want the notice %q", notice.shown, waiter.handed, want)
	}
	waiter.state.Registered = true
	w.decide("")
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1)"}; !slices.Equal(waiter.handed, want) {
		t.Fatalf("handed %q, want once %q", waiter.handed, want)
	}
	if want := []string{"guildhall: 1 unread (note 1)", ""}; !slices.Equal(notice.shown, want) {
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
		return s.SendMessage(store.Message{ID: "n-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-1", Type: "note", Text: "x"})
	}); err != nil {
		t.Fatal(err)
	}
	w.decide("")
	if len(notice.shown) != 0 {
		t.Fatalf("a registered waiter showed the notice %q", notice.shown)
	}
	waiter.state.Registered = false
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1)"}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want %q", notice.shown, want)
	}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.AckMessage(store.Recipient{Role: store.RecipientDirector}, "n-1")
	}); err != nil {
		t.Fatal(err)
	}
	w.decide("")
	if want := []string{"guildhall: 1 unread (note 1)", ""}; !slices.Equal(notice.shown, want) {
		t.Fatalf("notice %q, want it cleared at zero unread", notice.shown)
	}
}
