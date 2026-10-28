package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// wakeTimeout bounds one wake's typing into the Director's pane.
const wakeTimeout = 10 * time.Second

// wake runs messaging.Wake for a message sent to the Director, over the store file at path opened for each
// record operation, so no lock is held while it types.
func (a *App) wake(path, messageID, sender string) (store.Wake, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	return messaging.Wake(ctx, storeRecords{path: path}, a.directorPane(), messageID, sender)
}

func (a *App) directorPane() messaging.DirectorPane {
	if a.DirectorPane != nil {
		return a.DirectorPane
	}
	return herdrPanes{command: []string{"herdr"}}
}

// herdrPanes types into the Director's pane through Herdr, adopting the recorded tab in the recorded session.
type herdrPanes struct{ command []string }

func (h herdrPanes) TypeLine(ctx context.Context, p store.DirectorPane, line string) error {
	c, err := herdr.New(herdr.Options{Session: p.Session, Command: h.command})
	if err != nil {
		return err
	}
	ref := adapter.TabRef{Session: p.Session, WorkspaceID: p.Workspace, TabID: p.Tab, PaneID: p.Pane}
	err = adapter.NewHerdrTabs(c).TypeRecorded(ctx, ref, line)
	if errors.Is(err, adapter.ErrShellIdle) {
		return fmt.Errorf("%w: %w", messaging.ErrNotRunning, err)
	}
	return err
}

// HandWake hands line to the waiter of session d through its supervisor's endpoint in the session's recorded
// directory; a session recorded with no directory has no endpoint to reach, so no waiter.
func (herdrPanes) HandWake(ctx context.Context, d store.DirectorSession, line string) error {
	if d.Dir == "" {
		return session.ErrNoWaiter
	}
	return session.Wake(ctx, d.Dir, d.Number, line)
}

var _ messaging.EndpointWaker = herdrPanes{}

// noticeSource is the source Guildhall reports the wake notice under in the pane's metadata.
const noticeSource = "guildhall"

// wakeNotice is the wake notice for the Director's recorded pane: App.WakeNotice when set, otherwise its Herdr
// pane title.
func (a *App) wakeNotice(p store.DirectorPane) wakeNotice {
	if a.WakeNotice != nil {
		return a.WakeNotice(p)
	}
	return herdrNotice{pane: p, command: []string{"herdr"}}
}

// herdrNotice shows the wake notice as the title of the Director's recorded pane, outside its input box, adopting
// the recorded tab as a wake does.
type herdrNotice struct {
	pane    store.DirectorPane
	command []string
}

func (h herdrNotice) title(ctx context.Context, text string) error {
	c, err := herdr.New(herdr.Options{Session: h.pane.Session, Command: h.command})
	if err != nil {
		return err
	}
	t, err := c.RecordedTab(ctx, h.pane.Workspace, h.pane.Tab, h.pane.Pane)
	if err != nil {
		return err
	}
	return c.PaneTitle(ctx, t, noticeSource, text)
}

func (h herdrNotice) Show(ctx context.Context, text string) error { return h.title(ctx, text) }
func (h herdrNotice) Clear(ctx context.Context) error             { return h.title(ctx, "") }

// wakingOutbox is the launcher's outbox: it sends through records and then wakes the Director for a message
// addressed to it (docs/messaging.md#waking-the-director). A failed wake does not fail the send: the message
// is stored and waits for the Director's next turn, the failure is reported on stderr, and a replayed send,
// as the engine's recovery makes, tries the wake again.
type wakingOutbox struct {
	records storeRecords
	pane    messaging.DirectorPane
	stderr  io.Writer
}

func (o wakingOutbox) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	r, err := o.records.Send(to, e)
	if err == nil && to.Role == store.RecipientDirector {
		o.wake(e.ID)
	}
	return r, err
}

// wake wakes the Director for message id, bounded by wakeTimeout, reporting a failure on stderr.
func (o wakingOutbox) wake(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	if _, err := messaging.Wake(ctx, o.records, o.pane, id, messaging.SenderGuildhall); err != nil {
		_, _ = fmt.Fprintf(o.stderr, "guildhall: wake: %v\n", err)
	}
}

// deferredOutbox is the engine's outbox in an advance: it sends through records under the task lock and keeps the
// ids of the messages sent to the Director, which wakeAll wakes once the advance has released the lock, one wake
// per message as wakingOutbox makes, so the lock is held for records and launches only.
type deferredOutbox struct {
	records storeRecords
	ids     []string
}

func (o *deferredOutbox) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	r, err := o.records.Send(to, e)
	if err == nil && to.Role == store.RecipientDirector {
		o.ids = append(o.ids, e.ID)
	}
	return r, err
}

// wakeAll wakes the Director for each message sent, in send order, through w.
func (o *deferredOutbox) wakeAll(w wakingOutbox) {
	for _, id := range o.ids {
		w.wake(id)
	}
}

func (p storeRecords) WakeFor(id string) (store.Wake, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.Wake, error) { return s.WakeFor(id) })
}

func (p storeRecords) Message(id string) (store.Message, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.Message, error) { return s.Message(id) })
}

func (p storeRecords) LatestDirectorSession() (store.DirectorSession, error) {
	return withRecords(p, true, nil, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
}

func (p storeRecords) DirectorUnread() (map[string]int, error) {
	return withRecords(p, true, nil, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
}

func (p storeRecords) RecordWake(w store.Wake) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.RecordWake(w) })
}

func (p storeRecords) HandoffDues(n int) ([]store.HandoffDue, error) {
	return withRecords(p, true, nil, func(s *store.Store) ([]store.HandoffDue, error) { return s.HandoffDues(n) })
}

func (p storeRecords) FinishedTasks(n int) (int, error) {
	return withRecords(p, true, nil, func(s *store.Store) (int, error) { return s.FinishedTasks(n) })
}

func (p storeRecords) RecordHandoffDue(d store.HandoffDue) (store.Receipt, error) {
	return withRecords(p, false, nil, func(s *store.Store) (store.Receipt, error) { return s.RecordHandoffDue(d) })
}

var _ messaging.WakeStore = storeRecords{}
