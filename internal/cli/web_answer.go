package cli

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// webAnswerer is the task pages' Answerer (docs/web.md#answering): it records an answer through answerDecision, as
// decide answer does, attributed to the page and the Tailscale login, and tells the Director with a
// decision-answered message; words only become a note to the Director, surfaced and woken as messages send's are.
// Each opens the store with webLockWait's bounded wait and closes it before surfacing or waking.
type webAnswerer struct {
	a *App
	c clerkPaths
}

// pageText is the form's words as decide answer reads an answer file (normaliseText): a byte order mark dropped
// and CRLF made LF; more than limit bytes after that, and invalid UTF-8, are refused.
func pageText(what, text string, limit int) (string, error) {
	out, err := normaliseText(what, []byte(text), limit)
	switch {
	case err != nil:
		return "", web.Refuse(http.StatusRequestEntityTooLarge, err.Error())
	case !utf8.ValidString(out):
		return "", web.Refuse(http.StatusBadRequest, what+" is not valid UTF-8")
	case len(out) > limit:
		return "", web.Refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("%s is %d bytes; the limit is %d (%d KiB)", what, len(out), limit, limit>>10))
	}
	return out, nil
}

// webBusy is ErrBusy for a store whose lock was held past the wait, else err.
func webBusy(err error) error {
	if errors.Is(err, store.ErrLocked) {
		return web.ErrBusy
	}
	return err
}

// Answer implements web.Answerer.
func (w webAnswerer) Answer(x web.Answer) error {
	text, err := pageText("your answer", x.Text, store.MaxTextBytes)
	if err != nil {
		return err
	}
	answer := x.Option
	switch {
	case x.Option == "" && strings.TrimSpace(text) == "":
		return web.Refuse(http.StatusBadRequest, "write your answer in the box first")
	case x.Option == "":
		answer = text
	case strings.TrimSpace(text) != "":
		answer += "\n\n" + text
	}
	if len(answer) > store.MaxTextBytes {
		return web.Refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("your answer is over the limit of %d bytes (64 KiB)", store.MaxTextBytes))
	}
	env := webEnvelope(x, web.AnsweredID(x.Repository, x.TaskID, x.Decision), store.MessageDecisionAnswered,
		fmt.Sprintf("Decision %s of %s/%s was answered on the task page; read the answer with guildhall decide show, then continue the task.", x.Decision, x.Repository, x.TaskID))
	// The answer and its message are written in one open of the store, so a busy store refuses both.
	_, err = withRecords(storeRecords{path: w.c.store, wait: webLockWait}, false, nil, func(s *store.Store) (struct{}, error) {
		if err := webRecordAnswer(s, x, answer); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, webSendDirector(s, env)
	})
	if err != nil {
		return webBusy(err)
	}
	w.wake(env.ID)
	return nil
}

// webEnvelope is a message to the Director about x's task.
func webEnvelope(x web.Answer, id, typ, text string) messaging.Envelope {
	return messaging.Envelope{Schema: fmt.Sprintf("%s%d", messaging.SchemaPrefix, messaging.SchemaVersion), ID: id,
		Recipient: store.RecipientDirector, Repository: x.Repository, Task: x.TaskID, Type: typ, Text: text}
}

// webSendDirector sends env to the Director, from the Guildmaster: the page checked their Tailscale login.
func webSendDirector(s *store.Store, env messaging.Envelope) error {
	box := messaging.StoreMailbox{Store: s}
	_, err := box.Send(store.Recipient{Role: store.RecipientDirector}, store.SenderGuildmaster, env)
	return err
}

// webRecordAnswer records answer for x in s, refusing with the page's reasons: a decision void or bound to another
// subject since the page loaded, an option it does not offer, an answer another way or a cancelled task.
func webRecordAnswer(s *store.Store, x web.Answer, answer string) error {
	dec, err := findDecision(s, x.Repository, x.TaskID, x.Decision)
	if errors.Is(err, store.ErrNotFound) {
		return web.Refuse(http.StatusNotFound, "this task has no decision #"+x.Decision)
	}
	if err != nil {
		return err
	}
	subject := ""
	if dec.Subject != nil {
		subject = dec.Subject.Digest
	}
	switch {
	case dec.Void:
		return web.Refuse(http.StatusConflict, "changed since you loaded the page: #"+x.Decision+" is void: "+oneLine(dec.VoidReason))
	case subject != x.Subject:
		return web.Refuse(http.StatusConflict, "changed since you loaded the page: #"+x.Decision+" is about another version now; reload and read it again")
	case x.Option != "" && !slices.Contains(dec.Options, x.Option):
		return web.Refuse(http.StatusBadRequest, fmt.Sprintf("%q is not one of #%s's answers", x.Option, x.Decision))
	case x.Option == "" && len(dec.Options) > 0 && dec.Kind != store.DecisionQuestion:
		return web.Refuse(http.StatusBadRequest, "choose one of #"+x.Decision+"'s answers")
	}
	_, err = answerDecision(s, x.Repository, x.TaskID, x.Decision, answer, store.AnsweredViaPage, x.Login)
	switch {
	case err == nil:
		return nil
	case dec.Answered:
		return web.Refuse(http.StatusConflict, "already answered: "+webAnswered(dec))
	case errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrMutationConflict):
		return web.Refuse(http.StatusConflict, err.Error())
	}
	if t, terr := s.Task(x.Repository, x.TaskID); terr == nil && t.State == store.TaskCancelled {
		return web.Refuse(http.StatusConflict, err.Error())
	}
	return err
}

// webAnswered says how an answered decision was answered: its choice, or its answer's first line, and where.
func webAnswered(d store.Decision) string {
	what := d.Choice
	if what == "" {
		what, _, _ = strings.Cut(strings.TrimSpace(d.Answer), "\n")
		what = webCut(what, 80)
	}
	if d.AnsweredVia == store.AnsweredViaPage {
		return what + " (on the page, by " + d.AnsweredBy + ")"
	}
	return what + " (in chat or with decide answer)"
}

// webNotePrefix begins a words-only note's text, naming its task and decision, so its first line, which the
// Guildmaster's toast shows, says what it is about.
func webNotePrefix(x web.Answer) string {
	return x.TaskID + " #" + x.Decision + ", words only, nothing answered: "
}

// Send implements web.Answerer.
func (w webAnswerer) Send(x web.Answer) (string, error) {
	text, err := pageText("your words", x.Text, store.MaxMessageTextBytes)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", web.Refuse(http.StatusBadRequest, "write your words in the box first; nothing was sent")
	}
	body := webNotePrefix(x) + text
	if len(body) > store.MaxMessageTextBytes {
		return "", web.Refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("your words are over the limit of %d bytes (16 KiB) for a message", store.MaxMessageTextBytes-len(webNotePrefix(x))))
	}
	env := webEnvelope(x, web.NoteID(x.Repository, x.TaskID, x.Decision, text), store.MessageNote, body)
	_, err = withRecords(storeRecords{path: w.c.store, wait: webLockWait}, false, nil, func(s *store.Store) (struct{}, error) {
		if _, err := findDecision(s, x.Repository, x.TaskID, x.Decision); errors.Is(err, store.ErrNotFound) {
			return struct{}{}, web.Refuse(http.StatusNotFound, "this task has no decision #"+x.Decision)
		} else if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, webSendDirector(s, env)
	})
	if err != nil {
		return "", webBusy(err)
	}
	// A note is shown to the Guildmaster as messages send shows one (docs/messaging.md#surfacing-a-note).
	if err := w.a.surface(w.c.store, env.ID); err != nil {
		_, _ = fmt.Fprintf(w.a.Stderr, "web: surface %s: %v\n", env.ID, err)
	}
	w.wake(env.ID)
	return env.ID, nil
}

// wake wakes the Director for a message just stored; the message waits in its inbox whatever the wake does, so a
// failure is logged, not a refusal.
func (w webAnswerer) wake(id string) {
	if _, err := w.a.wake(w.c.store, id, messaging.SenderPage); err != nil {
		_, _ = fmt.Fprintf(w.a.Stderr, "web: wake for %s: %v\n", id, err)
	}
}
