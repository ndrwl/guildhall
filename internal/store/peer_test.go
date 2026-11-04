package store

import (
	"errors"
	"testing"
)

func peerMessage(sender, id, typ string) ExternalMessage {
	return ExternalMessage{Sender: sender, ID: id, From: sender, To: "alpha", Type: typ, Subject: "web needs cli --json",
		Text: "Task web/t-12 parses cli list --json."}
}

// TestReceivePeerMessage: a peer Director is its own sender, by name, kept apart from the Steward and from every other
// peer: its messages have one of the four Director message types, only an answer has in_reply_to and it must, and a
// reserved sender word is never a peer. Its unread messages count by type and peer name, and its inbox closes alone.
func TestReceivePeerMessage(t *testing.T) {
	s, _ := newStore(t)
	for _, typ := range DirectorMessageTypes {
		m := peerMessage("web", "web-"+typ, typ)
		if typ == MessageAnswer {
			m.InReplyTo = "q-1"
		}
		r, err := s.ReceiveExternal(m, DefaultMailboxMax)
		if err != nil || r.MutationID != "dmessage:web:web-"+typ {
			t.Fatalf("%s: %+v, %v", typ, r, err)
		}
	}
	// The same id from another peer is another message.
	if _, err := s.ReceiveExternal(peerMessage("tools", "web-question", MessageQuestion), DefaultMailboxMax); err != nil {
		t.Fatalf("same id from tools: %v", err)
	}
	if m, err := s.External("web", "web-answer"); err != nil || m.InReplyTo != "q-1" {
		t.Fatalf("answer: %+v, %v", m, err)
	}
	for name, bad := range map[string]func(*ExternalMessage){
		"steward-report from a peer":  func(m *ExternalMessage) { m.Type, m.Kind = MessageStewardReport, ReportRolloutFailed },
		"note from a peer":            func(m *ExternalMessage) { m.Type = MessageNote },
		"kind on a question":          func(m *ExternalMessage) { m.Kind = ReportRolloutFailed },
		"from another peer":           func(m *ExternalMessage) { m.From = "tools" },
		"answer without in_reply_to":  func(m *ExternalMessage) { m.Type = MessageAnswer },
		"in_reply_to on a question":   func(m *ExternalMessage) { m.InReplyTo = "q-1" },
		"bad in_reply_to":             func(m *ExternalMessage) { m.Type, m.InReplyTo = MessageAnswer, "q 1" },
		"peer named guildmaster":      func(m *ExternalMessage) { m.Sender, m.From = SenderGuildmaster, SenderGuildmaster },
		"peer named director":         func(m *ExternalMessage) { m.Sender, m.From = SenderDirector, SenderDirector },
		"peer named other-director":   func(m *ExternalMessage) { m.Sender, m.From = SenderOtherDirector, SenderOtherDirector },
		"peer name that is not an id": func(m *ExternalMessage) { m.Sender, m.From = "Web", "Web" },
		"peer name with a colon":      func(m *ExternalMessage) { m.Sender, m.From = "web:x", "web:x" },
	} {
		m := peerMessage("web", "bad-1", MessageQuestion)
		bad(&m)
		if _, err := s.ReceiveExternal(m, DefaultMailboxMax); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	unread, err := s.DirectorUnread()
	if err != nil || unread[UnreadFrom(MessageQuestion, "web")] != 1 || unread[UnreadFrom(MessageQuestion, "tools")] != 1 ||
		unread[UnreadFrom(MessageHandoffOffer, "web")] != 1 || unread[UnreadFrom(MessageAnswer, "web")] != 1 {
		t.Fatalf("unread: %v, %v", unread, err)
	}
	if page, _, err := s.ExternalInbox("tools", 0, 10); err != nil || len(page) != 1 || page[0].Sender != "tools" {
		t.Errorf("tools' inbox: %+v, %v", page, err)
	}
	if _, err := s.CloseInbox("web", "too many", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveExternal(peerMessage("web", "web-2", MessageQuestion), DefaultMailboxMax); !errors.Is(err, ErrInboxClosed) {
		t.Errorf("closed to web: %v", err)
	}
	if _, err := s.ReceiveExternal(peerMessage("tools", "tools-2", MessageQuestion), DefaultMailboxMax); err != nil {
		t.Errorf("tools while closed to web: %v", err)
	}
}

// TestOutboxEntryOf: one outbox entry by id, whatever its state.
func TestOutboxEntryOf(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.OutboxEntryOf("q-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no entry: %v", err)
	}
	if _, err := s.RecordOutbox("q-1", "web", []byte(`{"type":"question"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutboxAttempt("q-1", 1, []byte(`{"sequence":1}`), "", ""); err != nil {
		t.Fatal(err)
	}
	e, err := s.OutboxEntryOf("q-1")
	if err != nil || e.State != OutboxDelivered || e.To != "web" {
		t.Fatalf("entry: %+v, %v", e, err)
	}
	if _, err := s.OutboxEntryOf("q 1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
}
