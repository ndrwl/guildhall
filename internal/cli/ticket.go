package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Tickets (docs/messaging.md#tickets): a Director files a handoff-offer, dependency-notice or steward-report it judged
// worth doing as a ticket, which opens an accept/decline decision on the reserved task messages/<ticket id> that only
// the Guildmaster answers. Only accept lets `task new --ticket <id>` create the task, which the ticket then records.
const (
	ticketAccept  = "accept"
	ticketDecline = "decline"
)

const ticketUsage = `usage:
  guildhall ticket file --sender <sender> --from-message <id> --repo <id> --title <text> [--copy <dir>]
  guildhall ticket list [--copy <dir>]
ticket file files the external message (sender, id), a handoff-offer, dependency-notice or steward-report, as a ticket
proposing a task titled --title in the repository --repo, and opens its accept/decline decision, which only the
Guildmaster answers. Filing the same message again prints its ticket. Once the Guildmaster answers accept, create the
task with guildhall task new --ticket <ticket id>; decline closes the ticket.
`

// ticketView is one ticket as `ticket file` and `ticket list` print it: the record, its state and its decision.
type ticketView struct {
	store.Ticket
	// State is proposed until the Guildmaster answers the decision, then accepted or declined.
	State string `json:"state"`
	// Decision is the ticket's decision, "messages/<ticket id>/<ticket id>", and Choice the Guildmaster's answer.
	Decision string `json:"decision"`
	Choice   string `json:"choice,omitempty"`
}

func (a *App) ticket(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "file":
			return a.ticketFile(args[1:])
		case "list":
			return a.ticketList(args[1:])
		case "-h", "--help", "help":
			return a.printf("%s", ticketUsage)
		}
	}
	return a.ticketUsageError("ticket: the commands are file and list")
}

func (a *App) ticketUsageError(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n\n%s", msg, ticketUsage)
	return 2
}

// ticketFile runs `guildhall ticket file --sender <s> --from-message <id> --repo <id> --title <text> [--copy <dir>]`.
func (a *App) ticketFile(args []string) int {
	const name = "ticket file"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	sender := fl.String("sender", "", "the message's sender")
	message := fl.String("from-message", "", "the message's id")
	repo := fl.String("repo", "", "the repository the proposed task belongs to")
	title := fl.String("title", "", "the proposed task's title")
	dir, storePath, code := a.ticketCopy(name, fl, &cp, args)
	if code >= 0 {
		return code
	}
	switch {
	case fl.NArg() > 0:
		return a.ticketUsageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case *sender == "" || *message == "":
		return a.ticketUsageError(name + ": --sender and --from-message are both required: a message is named by its sender and id")
	case store.ValidateExternalSender(*sender) != nil:
		return a.ticketUsageError(fmt.Sprintf("%s: --sender must be %s or a peer Director's name", name, store.SenderSteward))
	case *repo == "" || strings.TrimSpace(*title) == "":
		return a.ticketUsageError(name + ": --repo and --title are required: the task the ticket proposes")
	case strings.ContainsAny(*title, "\r\n"):
		return a.ticketUsageError(name + ": --title is one line")
	case utf8.RuneCountInString(*title) > store.MaxTitleRunes:
		return a.ticketUsageError(fmt.Sprintf("%s: --title is %d characters; the limit is %d", name, utf8.RuneCountInString(*title), store.MaxTitleRunes))
	}
	// ticket file writes to the store, so it acts on the admitted config, as messages receive does.
	cfg, code := a.receiveConfig(name, dir)
	if code >= 0 {
		return code
	}
	if _, err := cfg.ListedRepository(*repo); err != nil {
		return a.fail(fmt.Errorf("%s: --repo: %w", name, err))
	}
	out, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (ticketView, error) {
		return fileTicket(s, *sender, *message, *repo, *title)
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(out)
}

// fileTicket files the message (sender, message) as a ticket proposing title in repo, once, then opens its reserved
// task and decision, once each, and returns the ticket. Filing a message already filed returns its ticket when the
// proposal is the same, so a retry after a crash opens the decision it missed.
func fileTicket(s *store.Store, sender, message, repo, title string) (ticketView, error) {
	t, err := s.TicketFor(sender, message)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if _, err := s.FileTicket(sender, message, repo, title); err != nil {
			return ticketView{}, err
		}
		if t, err = s.TicketFor(sender, message); err != nil {
			return ticketView{}, err
		}
	case err != nil:
		return ticketView{}, err
	case t.Repository != repo || t.Title != title:
		return ticketView{}, fmt.Errorf("%w: message %s/%s is already %s, proposing %q in %s", store.ErrInvalid, sender, message, t.ID, t.Title, t.Repository)
	}
	m, err := s.External(sender, message)
	if err != nil {
		return ticketView{}, err
	}
	task := store.Task{Repository: config.MessagesRepository, ID: t.ID, Title: truncateUTF8("Ticket: "+t.Title, store.MaxTitleRunes),
		Detail: fmt.Sprintf("The Guildmaster's decision on %s, filed by the Director from the %s %s from %s: is the task %q in %s worth doing?",
			t.ID, m.Type, m.ID, m.Sender, t.Title, t.Repository),
		Workflow:     workflow.Revision{Name: "messages", Digest: inboxAlertDigest},
		ConfigDigest: inboxAlertDigest}
	if _, err := s.CreateTask("messages-ticket-task:"+t.ID, task); err != nil {
		return ticketView{}, err
	}
	what := m.Type
	if m.Kind != "" {
		what += " " + m.Kind
	}
	d := store.Decision{Repository: config.MessagesRepository, TaskID: t.ID, ID: t.ID, Kind: store.DecisionQuestion,
		Options: []string{ticketAccept, ticketDecline},
		Question: truncateUTF8(fmt.Sprintf("The Director filed %s from the %s %s from %s, %q, proposing the task %q in %s. "+
			"A ticket is not a task: accept lets the Director create it with guildhall task new --repo %s --ticket %s; decline closes the ticket. "+
			"Read the message: guildhall messages read --sender %s %s. "+
			"Answer: guildhall decide answer --repo %s --task %s --decision %s --answer-file -",
			t.ID, what, m.ID, m.Sender, m.Subject, t.Title, t.Repository, t.Repository, t.ID, m.Sender, m.ID,
			config.MessagesRepository, t.ID, t.ID), store.MaxTextBytes)}
	if _, err := s.OpenDecision("messages-ticket-decision:"+t.ID, d); err != nil && !errors.Is(err, store.ErrExists) {
		return ticketView{}, err
	}
	return viewTicket(s, t)
}

// viewTicket returns t with its state, read from its decision.
func viewTicket(s *store.Store, t store.Ticket) (ticketView, error) {
	v := ticketView{Ticket: t, State: store.TicketProposed, Decision: fmt.Sprintf("%s/%s/%s", config.MessagesRepository, t.ID, t.ID)}
	ds, err := s.Decisions(config.MessagesRepository, t.ID)
	if errors.Is(err, store.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	for _, d := range ds {
		if d.ID != t.ID || !d.Answered {
			continue
		}
		v.Choice = d.Choice
		switch d.Choice {
		case ticketAccept:
			v.State = store.TicketAccepted
		case ticketDecline:
			v.State = store.TicketDeclined
		}
	}
	return v, nil
}

// ticketList runs `guildhall ticket list [--copy <dir>]`: every ticket in filing order with its state. It reads only.
func (a *App) ticketList(args []string) int {
	const name = "ticket list"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	_, storePath, code := a.ticketCopy(name, fl, &cp, args)
	if code >= 0 {
		return code
	}
	if fl.NArg() > 0 {
		return a.ticketUsageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	}
	out, err := withStore(storePath, true, func(s *store.Store) ([]ticketView, error) {
		all, err := s.Tickets()
		if err != nil {
			return nil, err
		}
		views := make([]ticketView, 0, len(all))
		for _, t := range all {
			v, err := viewTicket(s, t)
			if err != nil {
				return nil, err
			}
			views = append(views, v)
		}
		return views, nil
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printJSON(struct {
		Tickets []ticketView `json:"tickets"`
	}{out})
}

// ticketCopy is externalCopy for the ticket commands, whose usage errors print the ticket usage.
func (a *App) ticketCopy(name string, fl *flag.FlagSet, cp *copyFlags, args []string) (string, string, int) {
	return a.directorCopy(name, fl, cp, args, a.ticketUsageError)
}

// ticketForTask checks that the Guildmaster answered ticket id's decision accept, that it proposes a task in repo and
// that it has not yet become a task, so `task new --ticket` may create its task.
func ticketForTask(s *store.Store, id, repo string) error {
	t, err := s.TicketOf(id)
	if err != nil {
		return err
	}
	v, err := viewTicket(s, t)
	if err != nil {
		return err
	}
	switch {
	case t.Task != "":
		return fmt.Errorf("%w: %s already became the task %s/%s", store.ErrInvalid, id, t.Repository, t.Task)
	case t.Repository != repo:
		return fmt.Errorf("%w: %s proposes a task in %s, not %s", store.ErrInvalid, id, t.Repository, repo)
	case v.State == store.TicketDeclined:
		return fmt.Errorf("%w: the Guildmaster declined %s (decision %s), so no task is created from it", store.ErrInvalid, id, v.Decision)
	case v.State != store.TicketAccepted:
		return fmt.Errorf("%w: %s waits for the Guildmaster's answer to decision %s; only accept lets a task be created from it", store.ErrInvalid, id, v.Decision)
	}
	return nil
}
