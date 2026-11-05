package store

import (
	"errors"
	"strings"
	"testing"
)

// TestFileTicket: a handoff-offer, dependency-notice or steward-report is filed once as the next ticket, keeping its
// source's full identity; a question or an answer never is, nor a message not received; filing it again with the
// same proposal replays the receipt and with another conflicts. TicketTask records the created task once.
func TestFileTicket(t *testing.T) {
	s, _ := newStore(t)
	for _, m := range []ExternalMessage{
		peerMessage("web", "offer-1", MessageHandoffOffer),
		peerMessage("web", "notice-1", MessageDependencyNotice),
		peerMessage("web", "q-1", MessageQuestion),
		{Sender: SenderSteward, ID: "offer-1", From: SenderSteward, To: "alpha", Type: MessageStewardReport, Kind: ReportReleaseBehind, Subject: "s", Text: "t"},
	} {
		if _, err := s.ReceiveExternal(m, DefaultMailboxMax); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.FileTicket("web", "offer-1", "app", "Take the offer")
	if err != nil || r.MutationID != "ticket:web:offer-1" || r.Record != "ticket/ticket-1" {
		t.Fatalf("file: %+v, %v", r, err)
	}
	if again, err := s.FileTicket("web", "offer-1", "app", "Take the offer"); err != nil || again.Sequence != r.Sequence {
		t.Errorf("filing again: %+v, %v; want the original receipt", again, err)
	}
	if _, err := s.FileTicket("web", "offer-1", "app", "Another title"); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("another proposal: %v, want ErrMutationConflict", err)
	}
	// The same id from the Steward is another message, and another ticket.
	if r, err := s.FileTicket(SenderSteward, "offer-1", "app", "Update the release"); err != nil || r.Record != "ticket/ticket-2" {
		t.Fatalf("steward-report: %+v, %v", r, err)
	}
	if _, err := s.FileTicket("web", "notice-1", "app", "Follow the notice"); err != nil {
		t.Fatalf("dependency-notice: %v", err)
	}
	if _, err := s.FileTicket("web", "q-1", "app", "T"); !errors.Is(err, ErrInvalid) {
		t.Errorf("question: %v, want ErrInvalid", err)
	}
	if _, err := s.FileTicket("web", "none", "app", "T"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such message: %v, want ErrNotFound", err)
	}
	for name, title := range map[string]string{"blank": " ", "two lines": "a\nb", "long": strings.Repeat("a", MaxTitleRunes+1)} {
		if _, err := s.FileTicket("web", "notice-1", "app", title); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s title: %v, want ErrInvalid", name, err)
		}
	}
	all, err := s.Tickets()
	if err != nil || len(all) != 3 || all[0].ID != "ticket-1" || all[1].Sender != SenderSteward || all[1].Kind != ReportReleaseBehind || all[2].Type != MessageDependencyNotice {
		t.Fatalf("tickets: %+v, %v", all, err)
	}
	if got, err := s.TicketFor(SenderSteward, "offer-1"); err != nil || got.ID != "ticket-2" {
		t.Errorf("ticket for: %+v, %v", got, err)
	}

	if _, err := s.TicketTask("ticket-1", "t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a task that does not exist: %v, want ErrNotFound", err)
	}
	if _, err := s.CreateTask("task:app:t1", sampleTask("app", "t1")); err != nil {
		t.Fatal(err)
	}
	r, err = s.TicketTask("ticket-1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.TicketTask("ticket-1", "t1"); err != nil || again.Sequence != r.Sequence {
		t.Errorf("recording again: %+v, %v", again, err)
	}
	if _, err := s.TicketTask("ticket-1", "t2"); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("another task: %v, want ErrMutationConflict", err)
	}
	if got, err := s.TicketOf("ticket-1"); err != nil || got.Task != "t1" {
		t.Errorf("ticket: %+v, %v", got, err)
	}
	for _, id := range []string{"ticket-0", "ticket-01", "ticket-", "t-1"} {
		if _, err := s.TicketOf(id); !errors.Is(err, ErrInvalid) {
			t.Errorf("ticket id %q: %v, want ErrInvalid", id, err)
		}
	}
}
