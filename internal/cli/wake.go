package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// wakeTimeout bounds one wake's typing into the Director's pane.
const wakeTimeout = 10 * time.Second

// wake runs messaging.Wake for a message sent to the Director, over the store file at path opened for each
// record operation, so no lock is held while it types.
func (a *App) wake(path, messageID, sender string) (store.Wake, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	return messaging.Wake(ctx, storeRecords(path), a.directorPane(), messageID, sender)
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

// wakingOutbox is the engine's outbox: it sends through records and then wakes the Director for a message
// addressed to it (docs/messaging.md#waking-the-director). A failed wake does not fail the send: the message
// is stored and waits for the Director's next turn, and a replayed send, as the engine's recovery makes,
// tries the wake again.
type wakingOutbox struct {
	records storeRecords
	pane    messaging.DirectorPane
}

func (o wakingOutbox) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	r, err := o.records.Send(to, e)
	if err == nil && to.Role == store.RecipientDirector {
		ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
		defer cancel()
		_, _ = messaging.Wake(ctx, o.records, o.pane, e.ID, messaging.SenderGuildhall)
	}
	return r, err
}

func (p storeRecords) WakeFor(id string) (store.Wake, error) {
	return withStore(string(p), true, func(s *store.Store) (store.Wake, error) { return s.WakeFor(id) })
}

func (p storeRecords) Message(id string) (store.Message, error) {
	return withStore(string(p), true, func(s *store.Store) (store.Message, error) { return s.Message(id) })
}

func (p storeRecords) LatestDirectorSession() (store.DirectorSession, error) {
	return withStore(string(p), true, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
}

func (p storeRecords) DirectorUnread() (map[string]int, error) {
	return withStore(string(p), true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
}

func (p storeRecords) RecordWake(w store.Wake) (store.Receipt, error) {
	return withStore(string(p), false, func(s *store.Store) (store.Receipt, error) { return s.RecordWake(w) })
}

var _ messaging.WakeStore = storeRecords("")
