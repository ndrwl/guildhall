package messaging

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// Sender types a wake records (docs/messaging.md#waking-the-director). Every type wakes on every message
// today; they are the inputs a per-sender wake policy reads.
const (
	// SenderGuildhall is Guildhall itself: the engine's outbox and the launcher.
	SenderGuildhall = "guildhall"
	// SenderLocal is `guildhall messages send`, run by anyone who can write the store: the Guildmaster,
	// firstmate or an Artisan. It is asserted, not authenticated. The message's own sender (store.MessageSenders)
	// is the finer field that tells them apart.
	SenderLocal = "local"
	// SenderPage is the task page (docs/web.md#answering): an answer or words the Guildmaster gave on it, from the
	// Tailscale login the page checked.
	SenderPage = "page"
)

// ErrNotRunning reports a Director pane whose runtime is not running, so a typed line would reach a shell.
var ErrNotRunning = errors.New("the director runtime is not running in its pane")

// DirectorPane is the terminal a wake types into.
// TypeLine types line and Enter into pane, a pane an earlier Guildhall process created for the Director and
// recorded in its session. It returns ErrNotRunning, typing nothing, when the pane's runtime has ended.
// Success means the terminal accepted the line, not that the Director read it.
type DirectorPane interface {
	TypeLine(ctx context.Context, pane store.DirectorPane, line string) error
}

// EndpointWaker is the seam beside TypeLine that wakes a Director without touching its input box
// (docs/decisions/director-wake-hook.md): HandWake hands line, counts only, with due, the handoff reasons due, as
// its suffix, to the waiter session d's runtime armed with its wait-wake hook, through the supervisor's session
// endpoint. hold asks the supervisor to hold the wake and release one line for a burst. It returns
// session.ErrNoWaiter, handing nothing, when no waiter is registered or no supervisor listens. A DirectorPane that
// is not an EndpointWaker wakes only typed sessions; every other session is recorded "no waiter".
type EndpointWaker interface {
	HandWake(ctx context.Context, d store.DirectorSession, line string, hold bool, due []string) error
}

// ReasonNoWaiter is the queued reason of a wake no waiter took: the session's runtime is not typed into and had no
// wait-wake hook registered with its supervisor. The supervisor hands the next registered waiter one wake for it.
const ReasonNoWaiter = "no waiter"

// deliver wakes session d with line and the suffix of dues and returns why it could not, as a queued wake's reason,
// or "". Only a session whose runtime has no hooks, the fake, is typed into (store.DirectorSession.TypedWakes),
// never held, with HandoffLine; any other is handed the line, hold and the dues' reasons through pane's
// EndpointWaker and is never typed into, whatever its input box holds.
func deliver(ctx context.Context, pane DirectorPane, d store.DirectorSession, line string, hold bool, dues []store.HandoffDue) string {
	if d.TypedWakes() {
		return paneReason(pane.TypeLine(ctx, d.Pane, HandoffLine(line, dues)))
	}
	w, ok := pane.(EndpointWaker)
	if !ok {
		return ReasonNoWaiter
	}
	var reasons []string
	for _, due := range dues {
		reasons = append(reasons, due.Reason)
	}
	err := w.HandWake(ctx, d, line, hold, reasons)
	if errors.Is(err, session.ErrNoWaiter) || errors.Is(err, session.ErrNoSupervisor) {
		return ReasonNoWaiter
	}
	return paneReason(err)
}

// WakeStore is the store as Wake reads and writes it. *store.Store implements it; a caller that must not hold
// the store's lock while Wake types implements it by opening the store for each call.
type WakeStore interface {
	WakeFor(messageID string) (store.Wake, error)
	Message(id string) (store.Message, error)
	LatestDirectorSession() (store.DirectorSession, error)
	DirectorUnread() (map[string]int, error)
	RecordWake(w store.Wake) (store.Receipt, error)
	// HandoffDues, FinishedTasks and RecordHandoffDue are the automatic handoff triggers' records
	// (docs/director.md#automatic-handoff).
	HandoffDues(session int) ([]store.HandoffDue, error)
	FinishedTasks(session int) (int, error)
	RecordHandoffDue(d store.HandoffDue) (store.Receipt, error)
}

// Wake is the one wake function: after a message to the Director is stored, it decides whether to wake the
// Director, acts on that decision and records it as the message's store.Wake.
//
// With the newest session running or recovering and a bound pane, it wakes the Director with one fixed line,
// WakeLine of the Director's unread counts, which carries counts only and never message text, with HandoffLine's
// suffix of the handoff crossings DueHandoffs finds, and records woke and each crossing delivered. The line is
// handed to the session's waiter (EndpointWaker); only a fake runtime's session is typed into. Otherwise it
// delivers nothing and records queued with the reason: "no session", the session's state ("paused", "stopping" or
// "stopped"), "no pane", "nothing unread" when the message was acknowledged first, "no waiter" when no waiter took
// the line, "runtime not running", or "pane: <error>" when typing or handing failed.
// A queued message still waits in the inbox for the Director's next turn.
//
// A message already woken for, such as a replayed send, returns its recorded wake and types nothing new.
// Two concurrent wakes for one message may both type; the first decision recorded stands and both return it.
// A nil pane is a Director with no terminal: it records "no pane".
func Wake(ctx context.Context, st WakeStore, pane DirectorPane, messageID, senderType string) (store.Wake, error) {
	if w, err := st.WakeFor(messageID); !errors.Is(err, store.ErrNotFound) {
		return w, err
	}
	m, err := st.Message(messageID)
	if err != nil {
		return store.Wake{}, err
	}
	if !m.Recipient.SessionInbox() {
		return store.Wake{}, fmt.Errorf("messaging: wake: %w: message %q is addressed to %s", store.ErrInvalid, messageID, FormatRecipient(m.To()))
	}
	w := store.Wake{MessageID: messageID, SenderType: senderType, Decision: store.WakeQueued}
	var dues []store.HandoffDue
	if w.Reason, dues, err = typeWake(ctx, st, pane, 0); err != nil {
		return store.Wake{}, err
	}
	if w.Reason == "" {
		w.Decision = store.WakeWoke
	}
	if err := recordDues(st, dues, messageID); err != nil {
		return store.Wake{}, err
	}
	// A conflict means a concurrent wake recorded its decision first; that one stands.
	if _, err := st.RecordWake(w); err != nil && !errors.Is(err, store.ErrMutationConflict) {
		return store.Wake{}, err
	}
	return st.WakeFor(messageID)
}

// typeWake types the wake line, with the suffix of the handoff crossings due, into the newest session's pane when
// the Director can be woken, and returns why not otherwise, and the crossings typed. A positive idle adds the idle
// trigger's next crossing and types the line with nothing unread; an error is the store's.
func typeWake(ctx context.Context, st WakeStore, pane DirectorPane, idle time.Duration) (string, []store.HandoffDue, error) {
	d, reason, err := wakeable(st, pane)
	if reason != "" || err != nil {
		return reason, nil, err
	}
	unread, err := st.DirectorUnread()
	if err != nil {
		return "", nil, err
	}
	if len(unread) == 0 && idle <= 0 {
		return "nothing unread", nil, nil
	}
	dues, err := DueHandoffs(st, d)
	if err != nil {
		return "", nil, err
	}
	if idle > 0 {
		if dues, err = appendIdle(st, d.Number, dues, idle); err != nil {
			return "", nil, err
		}
	}
	// A message's wake asks to be held, so the supervisor wakes the Director once for a burst; an idle wake is not.
	if reason := deliver(ctx, pane, d, WakeLine(unread), idle <= 0, dues); reason != "" {
		return reason, nil, nil
	}
	return "", dues, nil
}

// wakeable returns the newest session when the Director can be woken through pane, and otherwise why not: "no
// session", the session's state when it is not running or recovering, or "no pane". An error is the store's.
func wakeable(st SessionReader, pane DirectorPane) (store.DirectorSession, string, error) {
	d, err := st.LatestDirectorSession()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return d, "no session", nil
	case err != nil:
		return d, "", err
	case d.State != store.DirectorRunning && d.State != store.DirectorRecovering:
		return d, string(d.State), nil
	case d.Pane == (store.DirectorPane{}) || pane == nil:
		return d, "no pane", nil
	}
	return d, "", nil
}

// paneReason is why a typed line did not reach the Director, from TypeLine's error: "" for none, "runtime not
// running", or "pane: <error>" on one line bounded by MaxWakeReasonBytes.
func paneReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotRunning):
		return "runtime not running"
	}
	reason := strings.Join(strings.Fields("pane: "+err.Error()), " ")
	if len(reason) > store.MaxWakeReasonBytes {
		reason = strings.ToValidUTF8(reason[:store.MaxWakeReasonBytes], "")
	}
	return reason
}

// WakeLine is the line a wake types: "guildhall: <n> unread (<type> <count>, ...)", types in name order, or
// "guildhall: 0 unread" with nothing unread. A count DirectorUnread keys by sender, store.UnreadFrom, reads
// "<type> <count> from <sender>", as in "note 1 from guildmaster"; the sender is one of store.MessageSenders,
// Guildhall's own word, never message text.
func WakeLine(unread map[string]int) string {
	if len(unread) == 0 {
		return "guildhall: 0 unread"
	}
	total, parts := counts(unread)
	return fmt.Sprintf("guildhall: %d unread (%s)", total, parts)
}

// counts sums n and lists it as "<name> <count>, ..." in name order, a name "<type> from <sender>" as
// "<type> <count> from <sender>".
func counts(n map[string]int) (int, string) {
	total, parts := 0, make([]string, 0, len(n))
	for _, name := range slices.Sorted(maps.Keys(n)) {
		total += n[name]
		if t, sender, ok := strings.Cut(name, " from "); ok {
			parts = append(parts, fmt.Sprintf("%s %d from %s", t, n[name], sender))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, n[name]))
	}
	return total, strings.Join(parts, ", ")
}

// ResumeWake is what WakeResumed did: woke when it typed the resume line, otherwise queued with the reason.
type ResumeWake struct {
	Decision store.WakeDecision `json:"decision"`
	Reason   string             `json:"reason,omitempty"`
}

// SessionReader reads the newest Director session, all WakeResumed needs of the store.
type SessionReader interface {
	LatestDirectorSession() (store.DirectorSession, error)
}

// WakeResumed wakes the Director once after `director resume` has moved its session to running, for the work
// that waited while it was paused (docs/messaging.md#waking-the-director). waiting counts what the status feed's
// needs_me lists for the Director to act on, by kind.
//
// With work waiting, the newest session running and a bound pane, it types ResumeLine(waiting), which carries
// counts only, and returns woke. Otherwise it types nothing and returns queued with the reason: "nothing
// waiting", or one of Wake's session and pane reasons. It records nothing: a resume is not a message, so each
// message keeps the one wake recorded for it, and the caller wakes once for the one resume that moved the session.
func WakeResumed(ctx context.Context, st SessionReader, pane DirectorPane, waiting map[string]int) (ResumeWake, error) {
	if len(waiting) == 0 {
		return ResumeWake{Decision: store.WakeQueued, Reason: "nothing waiting"}, nil
	}
	d, reason, err := wakeable(st, pane)
	if err != nil {
		return ResumeWake{}, err
	}
	if reason == "" {
		reason = deliver(ctx, pane, d, ResumeLine(waiting), false, nil)
	}
	if reason != "" {
		return ResumeWake{Decision: store.WakeQueued, Reason: reason}, nil
	}
	return ResumeWake{Decision: store.WakeWoke}, nil
}

// ResumeLine is the line WakeResumed types: "guildhall: resumed, <n> waiting (<kind> <count>, ...)", kinds in
// name order.
func ResumeLine(waiting map[string]int) string {
	total, parts := counts(waiting)
	return fmt.Sprintf("guildhall: resumed, %d waiting (%s)", total, parts)
}
