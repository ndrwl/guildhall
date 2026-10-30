package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// messageSender is the sender `messages send` records for a message to the store at storePath, from where it
// runs (docs/messaging.md#senders): an Artisan attempt the launcher marked, a session of this copy's Director or of
// another copy's, or the Guildmaster outside any session or attempt. A session's copy is the copy discovery finds
// from where the command runs, since a Director session runs inside its copy; --copy does not choose it. It is
// asserted by the environment, not authenticated.
func messageSender(getenv func(string) string, getwd func() (string, error), storePath string) string {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	switch {
	case getenv(artisan.AttemptEnv) != "":
		return store.SenderArtisan
	case getenv(session.NumberEnv) == "":
		return store.SenderGuildmaster
	}
	copyDir, ok, err := discoverCopy(getwd)
	switch {
	case err != nil || !ok:
		return store.SenderUnknown
	case samePath(filepath.Join(copyDir, "director.store"), storePath):
		return store.SenderDirector
	}
	return store.SenderOtherDirector
}

// surfaceMaxRunes bounds the first line a surfaced note shows.
const surfaceMaxRunes = 120

// surface shows the Guildmaster a message just sent to the Director, outside the model, when it is a note or comes
// from another Director (docs/messaging.md#surfacing-a-note): one Herdr toast on the newest Director session's
// Herdr server naming its sender and task, with its first line sanitised and the command that reads it. It is
// skipped, silently, for any other message, for a resend already woken for, and when that session is stopped or
// has no Herdr pane. It never wakes
// the Director, never reaches its input and never acknowledges the message; an error is Herdr's or the store's.
func (a *App) surface(path, id string) error {
	type target struct {
		m    store.Message
		pane store.DirectorPane
	}
	t, err := withStore(path, true, func(s *store.Store) (target, error) {
		m, err := s.Message(id)
		if err != nil {
			return target{}, err
		}
		// A message already woken for is a resend, surfaced the first time.
		if _, err := s.WakeFor(id); !errors.Is(err, store.ErrNotFound) {
			return target{m: m}, err
		}
		d, err := s.LatestDirectorSession()
		if errors.Is(err, store.ErrNotFound) {
			return target{m: m}, nil
		}
		if err != nil || d.State == store.DirectorStopped {
			return target{m: m}, err
		}
		return target{m: m, pane: d.Pane}, nil
	})
	if err != nil {
		return err
	}
	sender := store.MessageSender(t.m)
	if t.m.Type != store.MessageNote && sender != store.SenderOtherDirector || t.pane.Session == "" {
		return nil
	}
	title := "guildhall: note from " + sender + " about " + t.m.TaskID
	body := "guildhall messages read " + t.m.ID
	if line := surfaceLine(t.m.Text); line != "" {
		body = line + " · " + body
	}
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	return a.notify(ctx, t.pane, title, body)
}

// notify shows a toast on the Herdr server of pane: App.Notify when set, otherwise Herdr on PATH.
func (a *App) notify(ctx context.Context, pane store.DirectorPane, title, body string) error {
	if a.Notify != nil {
		return a.Notify(ctx, pane, title, body)
	}
	c, err := herdr.New(herdr.Options{Session: pane.Session, Command: []string{"herdr"}})
	if err != nil {
		return err
	}
	return c.Notify(ctx, title, body)
}

// surfaceLine is text's first line as a surfaced note shows it: every control character, ESC and the rest of C0
// and C1 among them, dropped, spaces trimmed, and cut to surfaceMaxRunes runes ending in "…".
func surfaceLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line))
	if utf8.RuneCountInString(line) <= surfaceMaxRunes {
		return line
	}
	return string([]rune(line)[:surfaceMaxRunes-1]) + "…"
}
