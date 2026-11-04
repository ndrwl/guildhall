package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The reserved messages repository's inbox alert decisions (docs/messaging.md#closing-the-inbox-to-a-sender): one task
// per external sender, "inbox-<sender>", whose decisions only the Guildmaster answers.
const (
	inboxReopen     = "reopen"
	inboxKeepClosed = "keep-closed"
)

// inboxAlertDigest stands in for the reserved inbox tasks' workflow and config digests: they run no workflow.
var inboxAlertDigest = string(digest.Of([]byte("guildhall messages reserved task")))

// inboxAlertTask is the reserved task holding the alert decisions of sender's inbox.
func inboxAlertTask(sender string) string { return "inbox-" + sender }

// summaryHeaders bounds the headers `messages summary` prints.
const summaryHeaders = 20

// externalCopy finds the Director copy an external messages command acts on, from --copy or copy discovery, and
// returns its directory and store; a Steward copy, which has no external inbox, is refused.
func (a *App) externalCopy(name string, fl *flag.FlagSet, cp *copyFlags, args []string) (dir, storePath string, code int) {
	if err := parseInterspersed(fl, args); err != nil {
		return "", "", a.messagesUsage(name + ": " + err.Error())
	}
	var path string
	switch err := cp.apply(a, &path, nil, nil); {
	case err != nil:
		return "", "", a.messagesUsage(name + ": " + err.Error())
	case cp.dir == "":
		return "", "", a.messagesUsage(name + ": " + cp.missing(a).Error())
	case filepath.Base(path) == stewardStoreFile:
		return "", "", a.fail(fmt.Errorf("%s: %s is a Steward copy; external messages reach a Director copy", name, cp.dir))
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", "", a.fail(fmt.Errorf("%s: %w", name, noStore(path)))
	}
	return cp.dir, path, -1
}

// messagesReceive runs `guildhall messages receive --copy <dir>`, the fixed remote command a Steward runs after copying
// one Steward message to steward.MessageDropPath in the copy (docs/messaging.md#messages-receive). It decodes the
// envelope, records it from the steward sender, which no envelope or environment chooses, deletes the drop file and
// prints a messaging.ReceiveResult: delivered with the receipt, or refused with the reason when the envelope is
// invalid, conflicts with one received before, or a new message finds the sender's inbox closed or the mailbox full.
// An identical message received before is delivered with its original receipt, whatever the inbox's state now. It
// exits 1, keeping the drop file, when there is no message to receive or the store fails, so the Steward retries.
// A new message is surfaced to the Guildmaster and wakes the Director; neither can fail the receive.
func (a *App) messagesReceive(args []string) int {
	const name = "messages receive"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	dir, storePath, code := a.externalCopy(name, fl, &cp, args)
	if code >= 0 {
		return code
	}
	if fl.NArg() > 0 {
		return a.messagesUsage(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	}
	drop := filepath.Join(dir, filepath.FromSlash(steward.MessageDropPath))
	data, err := os.ReadFile(drop)
	if err != nil {
		return a.fail(fmt.Errorf("%s: no message to receive: %w", name, err))
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	most, err := cfg.MailboxMax()
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	refused := func(reason string) int {
		if err := os.Remove(drop); err != nil {
			return a.fail(fmt.Errorf("%s: refused (%s), but the message stays: %w", name, reason, err))
		}
		return a.printJSON(messaging.ReceiveResult{Schema: messaging.ReceiveSchema, Status: messaging.ReceiveRefused, Reason: oneLine(reason)})
	}
	env, err := messaging.DecodeDirector(bytes.NewReader(data))
	if err != nil {
		return refused("invalid: " + err.Error())
	}
	m, err := messaging.ExternalMessage(store.SenderSteward, cfg.Name, env)
	if err != nil {
		return refused("invalid: " + err.Error())
	}
	r, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.Receipt, error) { return s.ReceiveExternal(m, most) })
	switch {
	case errors.Is(err, store.ErrInboxClosed):
		return refused(store.ErrInboxClosed.Error() + ": " + afterSentinel(err, store.ErrInboxClosed))
	case errors.Is(err, store.ErrMailboxFull):
		return refused(store.ErrMailboxFull.Error() + ": " + afterSentinel(err, store.ErrMailboxFull))
	case errors.Is(err, store.ErrMutationConflict):
		return refused("conflict: message " + m.ID + " was received before with different content")
	case errors.Is(err, store.ErrInvalid):
		return refused("invalid: " + err.Error())
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if err := os.Remove(drop); err != nil {
		return a.fail(fmt.Errorf("%s: received, but the drop file stays: %w", name, err))
	}
	if code := a.printJSON(messaging.ReceiveResult{Schema: messaging.ReceiveSchema, Status: messaging.ReceiveDelivered, Receipt: &r}); code != 0 {
		return code
	}
	// The message is stored whatever the surfacing and wake do; a failure of either is reported, not an error.
	if err := a.surfaceExternal(storePath, m.Sender, m.ID); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: surface: %v\n", name, err)
	}
	if _, err := a.wakeExternal(storePath, m.Sender, m.ID); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: wake: %v\n", name, err)
	}
	return 0
}

// afterSentinel is err's text after its sentinel's, the refusal's detail.
func afterSentinel(err, sentinel error) string {
	_, rest, ok := strings.Cut(err.Error(), sentinel.Error()+": ")
	if !ok {
		return err.Error()
	}
	return rest
}

// externalHeader is one unread external message as `messages summary` lists it: its identity and header, never its text.
type externalHeader struct {
	Sender   string `json:"sender"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Kind     string `json:"kind,omitempty"`
	Subject  string `json:"subject"`
	Sequence uint64 `json:"sequence"`
}

// messagesSummary is what `messages summary` prints.
type messagesSummary struct {
	// Unread counts the unread external messages by sender.
	Unread map[string]int `json:"unread"`
	// Inbox is each external sender's inbox state.
	Inbox []store.InboxState `json:"inbox"`
	// Messages are the newest unread external messages' headers, newest last.
	Messages []externalHeader `json:"messages"`
}

// messagesSummaryCmd runs `guildhall messages summary [--copy <dir>]`: the unread external messages counted by sender,
// each sender's inbox state, and the headers of the newest summaryHeaders unread, newest last. It reads only.
func (a *App) messagesSummaryCmd(args []string) int {
	const name = "messages summary"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	_, storePath, code := a.externalCopy(name, fl, &cp, args)
	if code >= 0 {
		return code
	}
	if fl.NArg() > 0 {
		return a.messagesUsage(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	}
	out, err := withStore(storePath, true, func(s *store.Store) (messagesSummary, error) {
		sum := messagesSummary{Unread: map[string]int{}, Inbox: []store.InboxState{}, Messages: []externalHeader{}}
		for _, sender := range store.ExternalSenders {
			st, err := s.InboxStateOf(sender)
			if err != nil {
				return sum, err
			}
			sum.Inbox = append(sum.Inbox, st)
		}
		var after uint64
		for {
			page, more, err := s.ExternalInbox("", after, store.MaxInboxLimit)
			if err != nil {
				return sum, err
			}
			for _, m := range page {
				sum.Unread[m.Sender]++
				sum.Messages = append(sum.Messages, externalHeader{Sender: m.Sender, ID: m.ID, Type: m.Type, Kind: m.Kind, Subject: m.Subject, Sequence: m.Sequence})
				after = m.Sequence
			}
			if !more {
				break
			}
		}
		if len(sum.Messages) > summaryHeaders {
			sum.Messages = sum.Messages[len(sum.Messages)-summaryHeaders:]
		}
		return sum, nil
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(out)
}

// listedExternal is a listed external message with the wake recorded for it, if any.
type listedExternal struct {
	store.ExternalMessage
	Wake *store.Wake `json:"wake,omitempty"`
}

// externalList is `messages list --from <sender>`: a page of the external messages from sender, each with its wake.
func externalList(s *store.Store, sender string, after uint64, limit int) (any, error) {
	msgs, more, err := s.ExternalInbox(sender, after, limit)
	if err != nil {
		return nil, err
	}
	out := struct {
		Messages []listedExternal `json:"messages"`
		More     bool             `json:"more"`
	}{Messages: make([]listedExternal, 0, len(msgs)), More: more}
	for _, m := range msgs {
		l := listedExternal{ExternalMessage: m}
		switch w, err := s.ExternalWakeFor(m.Sender, m.ID); {
		case err == nil:
			l.Wake = &w
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
		out.Messages = append(out.Messages, l)
	}
	return out, nil
}

// messagesOutboxCmd runs `guildhall messages outbox [--all] [--copy <dir>]`: the copy's outbox entries not delivered,
// pending and refused, or every one with --all. It reads only; the session supervisor retries pending entries.
func (a *App) messagesOutboxCmd(args []string) int {
	const name = "messages outbox"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	all := fl.Bool("all", false, "list delivered entries too")
	if err := parseInterspersed(fl, args); err != nil {
		return a.messagesUsage(name + ": " + err.Error())
	}
	var path string
	switch err := cp.apply(a, &path, nil, nil); {
	case err != nil:
		return a.messagesUsage(name + ": " + err.Error())
	case cp.dir == "":
		return a.messagesUsage(name + ": " + cp.missing(a).Error())
	case fl.NArg() > 0:
		return a.messagesUsage(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("%s: %w", name, noStore(path)))
	}
	entries, err := withStore(path, true, func(s *store.Store) ([]store.OutboxEntry, error) { return s.Outbox(*all) })
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(struct {
		Entries []store.OutboxEntry `json:"entries"`
	}{entries})
}

// messagesInboxCmd runs `guildhall messages inbox close --sender <s> --reason <text>` and `messages inbox open
// --sender <s>` (docs/messaging.md#closing-the-inbox-to-a-sender). Close refuses that sender's new messages and opens
// its alert, a reopen/keep-closed decision on the reserved task messages/inbox-<sender> that only the Guildmaster
// answers; open is refused unless the Guildmaster answered the newest close's alert reopen.
func (a *App) messagesInboxCmd(args []string) int {
	if len(args) == 0 || args[0] != "close" && args[0] != "open" {
		return a.messagesUsage("messages inbox: the subcommand is close or open")
	}
	sub := args[0]
	name := "messages inbox " + sub
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	sender := fl.String("sender", "", "the external sender")
	reason := fl.String("reason", "", "why the inbox is closed")
	_, storePath, code := a.externalCopy(name, fl, &cp, args[1:])
	if code >= 0 {
		return code
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case fl.NArg() > 0:
		return a.messagesUsage(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case !slices.Contains(store.ExternalSenders, *sender):
		return a.messagesUsage(fmt.Sprintf("%s: --sender must be one of %s", name, strings.Join(store.ExternalSenders, ", ")))
	case sub == "close" && strings.TrimSpace(*reason) == "":
		return a.messagesUsage(name + ": --reason is required")
	case sub == "open" && set["reason"]:
		return a.messagesUsage(name + ": --reason applies only to close")
	}
	var out any
	var err error
	if sub == "close" {
		out, err = withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.InboxState, error) { return closeInbox(s, *sender, oneLine(*reason)) })
	} else {
		out, err = withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.InboxState, error) { return reopenInbox(s, *sender) })
	}
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(out)
}

// closeInbox closes sender's inbox in s and opens its alert decision, once each, and returns the new state.
func closeInbox(s *store.Store, sender, reason string) (store.InboxState, error) {
	st, err := s.InboxStateOf(sender)
	if err != nil {
		return st, err
	}
	if st.State == store.InboxClosed {
		return st, fmt.Errorf("%w: the inbox is already closed to %s: %s", store.ErrInvalid, sender, st.Reason)
	}
	n := st.Closes + 1
	task := inboxAlertTask(sender)
	t := store.Task{Repository: config.MessagesRepository, ID: task, Title: "Inbox closed to " + sender,
		Detail:       "The Guildmaster's decisions on the Director's inbox closed to " + sender + ", one per close: reopen or keep-closed.",
		Workflow:     workflow.Revision{Name: "messages", Digest: inboxAlertDigest},
		ConfigDigest: inboxAlertDigest}
	if _, err := s.CreateTask("messages-inbox-task:"+sender, t); err != nil {
		return st, err
	}
	id := store.InboxAlertID(sender, n)
	d := store.Decision{Repository: config.MessagesRepository, TaskID: task, ID: id, Kind: store.DecisionQuestion,
		Options: []string{inboxReopen, inboxKeepClosed},
		Question: truncateUTF8(fmt.Sprintf("The Director closed its inbox to %s: %q. While it is closed, new messages from %s are refused and %s is told so. "+
			"Reopen it? reopen lets the Director open it again with guildhall messages inbox open --sender %s; keep-closed keeps refusing them. "+
			"Answer: guildhall decide answer --repo %s --task %s --decision %s --answer-file -",
			sender, reason, sender, sender, sender, config.MessagesRepository, task, id), store.MaxTextBytes)}
	if _, err := s.OpenDecision(fmt.Sprintf("messages-inbox-alert:%s:%d", sender, n), d); err != nil && !errors.Is(err, store.ErrExists) {
		return st, err
	}
	if _, err := s.CloseInbox(sender, reason, n); err != nil {
		return st, err
	}
	return s.InboxStateOf(sender)
}

// reopenInbox opens sender's inbox in s, refused unless the Guildmaster answered its newest close's alert reopen.
func reopenInbox(s *store.Store, sender string) (store.InboxState, error) {
	st, err := s.InboxStateOf(sender)
	if err != nil {
		return st, err
	}
	if st.State != store.InboxClosed {
		return st, fmt.Errorf("%w: the inbox is open to %s", store.ErrInvalid, sender)
	}
	ds, err := s.Decisions(config.MessagesRepository, inboxAlertTask(sender))
	if err != nil {
		return st, err
	}
	ref := fmt.Sprintf("%s/%s/%s", config.MessagesRepository, inboxAlertTask(sender), st.Decision)
	for _, d := range ds {
		if d.ID != st.Decision {
			continue
		}
		switch {
		case d.Answered && d.Choice == inboxReopen:
			if _, err := s.ReopenInbox(sender, st.Closes); err != nil {
				return st, err
			}
			return s.InboxStateOf(sender)
		case d.Answered:
			return st, fmt.Errorf("%w: the Guildmaster answered %s %q, so the inbox stays closed to %s", store.ErrInvalid, ref, d.Choice, sender)
		}
		return st, fmt.Errorf("%w: only the Guildmaster reopens the inbox to %s, by answering %s reopen", store.ErrInvalid, sender, ref)
	}
	return st, fmt.Errorf("%w: the alert decision %s is not recorded", store.ErrNotFound, ref)
}

// surfaceExternal shows the Guildmaster an external message just received, as surface does a note: one Herdr toast on
// the newest Director session's Herdr server naming its sender and type, with its subject sanitised and the command
// that reads it. It is skipped, silently, for a message already woken for and when that session is stopped or has no
// Herdr pane, and never wakes the Director, reaches its input or acknowledges the message.
func (a *App) surfaceExternal(path, sender, id string) error {
	type target struct {
		m    store.ExternalMessage
		pane store.DirectorPane
	}
	t, err := withStore(path, true, func(s *store.Store) (target, error) {
		m, err := s.External(sender, id)
		if err != nil {
			return target{}, err
		}
		if _, err := s.ExternalWakeFor(sender, id); !errors.Is(err, store.ErrNotFound) {
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
	if err != nil || t.pane.Session == "" {
		return err
	}
	what := t.m.Type
	if t.m.Kind != "" {
		what += " " + t.m.Kind
	}
	title := "guildhall: " + what + " from " + t.m.Sender
	body := "guildhall messages read --sender " + t.m.Sender + " " + t.m.ID
	if line := surfaceLine(t.m.Subject); line != "" {
		body = line + " · " + body
	}
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	return a.notify(ctx, t.pane, title, body)
}
