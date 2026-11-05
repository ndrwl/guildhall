package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// A ticket is a message filed for the Guildmaster's decision on whether the work it describes is worth doing
// (docs/messaging.md#tickets). It is not a task: it has no workflow revision, starts no step and launches no Artisan.
// Only the Guildmaster's answer accept to its decision lets the Director create the task, which the ticket then records.
var bucketTickets = []byte("tickets")

// Operations recorded on ticket receipts.
const (
	opFileTicket   = "file-ticket"
	opTicketTask   = "ticket-task"
	ticketIDPrefix = "ticket-"
)

// TicketTypes are the external message types that may be filed as a ticket: a peer's handoff-offer or
// dependency-notice, and a Steward's steward-report. A question or an answer never is.
var TicketTypes = []string{MessageHandoffOffer, MessageDependencyNotice, MessageStewardReport}

// Ticket states. A ticket is proposed until the Guildmaster answers its decision; accepted or declined follow that
// answer, and an accepted ticket records the task the Director created from it.
const (
	TicketProposed = "proposed"
	TicketAccepted = "accepted"
	TicketDeclined = "declined"
)

// Ticket is one ticket as the store records it.
type Ticket struct {
	// ID is "ticket-<n>", the n-th ticket of this store; it is also the id of its reserved task and its decision.
	ID string `json:"id"`
	// Sender and Message are the source message's full identity; Type and Kind are its type and kind.
	Sender  string `json:"sender"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Kind    string `json:"kind,omitempty"`
	// Repository and Title are the task the Director proposes: a repository of this copy and a task title.
	Repository string `json:"repository"`
	Title      string `json:"title"`
	// Task is the id of the task created in Repository after the Guildmaster accepted; empty until then.
	Task string `json:"task,omitempty"`
	// Sequence and FiledAt are set by the store from the filing receipt.
	Sequence uint64    `json:"sequence"`
	FiledAt  time.Time `json:"filed_at"`
}

// TicketID is the id of the n-th ticket, "ticket-<n>".
func TicketID(n int) string { return ticketIDPrefix + strconv.Itoa(n) }

// TicketNumber is the number of a ticket id, false when id is not "ticket-<n>".
func TicketNumber(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, ticketIDPrefix)
	if !ok || rest == "" || rest[0] < '1' || rest[0] > '9' {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && strconv.Itoa(n) == rest
}

// FileTicket records a ticket from the external message (sender, message) proposing a task titled title in
// repository, with the mutation "ticket:<sender>:<message>", so one message is filed at most once: filing it again with
// the same proposal returns the original receipt, and with another returns ErrMutationConflict. The message must exist
// and be one of TicketTypes. The ticket takes the next number; its decision is opened by the caller.
func (s *Store) FileTicket(sender, message, repository, title string) (Receipt, error) {
	if err := validateExternalRef(sender, message); err != nil {
		return Receipt{}, err
	}
	if err := validateIDs(repository); err != nil {
		return Receipt{}, err
	}
	if err := validateText("ticket title", title, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
		return Receipt{}, invalid("ticket title is %d characters; the limit is %d", n, MaxTitleRunes)
	}
	if strings.ContainsAny(title, "\r\n") {
		return Receipt{}, invalid("ticket title must be one line")
	}
	req := struct {
		Sender     string `json:"sender"`
		Message    string `json:"message"`
		Repository string `json:"repository"`
		Title      string `json:"title"`
	}{sender, message, repository, title}
	return s.mutate("ticket:"+sender+":"+message, opFileTicket, req, func(tx *bolt.Tx, r *Receipt) error {
		m, err := getExternal(tx, sender, message)
		if err != nil {
			return err
		}
		if !slices.Contains(TicketTypes, m.Type) {
			return invalid("a %s is never a ticket; only %s are", m.Type, strings.Join(TicketTypes, ", "))
		}
		b, err := createDirectorBucket(tx, bucketTickets)
		if err != nil {
			return err
		}
		high := 0
		if err := b.ForEach(func(k, v []byte) error {
			var t Ticket
			if err := decode(k, v, &t); err != nil {
				return err
			}
			if t.Sender == sender && t.Message == message {
				return fmt.Errorf("%w: message %s/%s is already %s", ErrExists, sender, message, t.ID)
			}
			if n, ok := TicketNumber(t.ID); ok && n > high {
				high = n
			}
			return nil
		}); err != nil {
			return err
		}
		t := Ticket{ID: TicketID(high + 1), Sender: sender, Message: message, Type: m.Type, Kind: m.Kind,
			Repository: repository, Title: title, Sequence: r.Sequence, FiledAt: s.now().UTC()}
		r.Record, r.CommittedAt = "ticket/"+t.ID, t.FiledAt
		return putJSON(b, []byte(t.ID), t)
	})
}

// TicketTask records that the task task of the ticket's repository was created from the ticket id, with the mutation
// "ticket-task:<id>"; recording the same task again returns its original receipt, and a ticket already recording
// another task is refused. The caller checks first that the Guildmaster answered the ticket's decision accept.
func (s *Store) TicketTask(id, task string) (Receipt, error) {
	if _, ok := TicketNumber(id); !ok {
		return Receipt{}, invalid("ticket id %q must be ticket-<n>", id)
	}
	if err := validateIDs(task); err != nil {
		return Receipt{}, err
	}
	req := struct {
		ID   string `json:"id"`
		Task string `json:"task"`
	}{id, task}
	return s.mutate("ticket-task:"+id, opTicketTask, req, func(tx *bolt.Tx, r *Receipt) error {
		t, err := getTicket(tx, id)
		if err != nil {
			return err
		}
		if t.Task != "" {
			return invalid("%s already became the task %s/%s", id, t.Repository, t.Task)
		}
		if _, err := getTask(tx, t.Repository, task); err != nil {
			return err
		}
		r.Repository, r.TaskID, r.Record = t.Repository, task, "ticket/"+id
		r.CommittedAt = s.now().UTC()
		t.Task = task
		return putJSON(directorBucket(tx, bucketTickets), []byte(id), t)
	})
}

// TicketOf returns one ticket.
func (s *Store) TicketOf(id string) (Ticket, error) {
	if _, ok := TicketNumber(id); !ok {
		return Ticket{}, invalid("ticket id %q must be ticket-<n>", id)
	}
	var t Ticket
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		t, err = getTicket(tx, id)
		return err
	})
	if err != nil {
		return Ticket{}, fmt.Errorf("store: %w", err)
	}
	return t, nil
}

// TicketFor returns the ticket filed from the external message (sender, message), or ErrNotFound.
func (s *Store) TicketFor(sender, message string) (Ticket, error) {
	all, err := s.Tickets()
	if err != nil {
		return Ticket{}, err
	}
	for _, t := range all {
		if t.Sender == sender && t.Message == message {
			return t, nil
		}
	}
	return Ticket{}, fmt.Errorf("store: %w: no ticket from message %s/%s", ErrNotFound, sender, message)
}

// Tickets returns every ticket in filing order.
func (s *Store) Tickets() ([]Ticket, error) {
	out := []Ticket{}
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketTickets)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var t Ticket
			if err := decode(k, v, &t); err != nil {
				return err
			}
			out = append(out, t)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: tickets: %w", err)
	}
	slices.SortFunc(out, func(a, b Ticket) int {
		switch {
		case a.Sequence < b.Sequence:
			return -1
		case a.Sequence > b.Sequence:
			return 1
		}
		return 0
	})
	return out, nil
}

func getTicket(tx *bolt.Tx, id string) (Ticket, error) {
	var t Ticket
	b := directorBucket(tx, bucketTickets)
	if b == nil || b.Get([]byte(id)) == nil {
		return t, fmt.Errorf("%w: ticket %s", ErrNotFound, id)
	}
	err := getJSON(b, []byte(id), &t)
	return t, err
}
