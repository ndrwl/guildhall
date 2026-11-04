package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func stewardReport(id string) ExternalMessage {
	return ExternalMessage{Sender: SenderSteward, ID: id, From: "steward", To: "alpha", Type: MessageStewardReport, Kind: ReportRolloutFailed,
		Subject: "Rollout failed", Text: "The reload failed."}
}

// TestReceiveExternal: a Steward message is stored outside any repository, with no task, keyed by its sender and id;
// an identical retry returns the original receipt, a different message under that identity conflicts, and nothing
// another sender sends is taken.
func TestReceiveExternal(t *testing.T) {
	s, _ := newStore(t)
	r, err := s.ReceiveExternal(stewardReport("r-1"), DefaultMailboxMax)
	if err != nil || r.MutationID != "dmessage:steward:r-1" || r.Repository != "" || r.Record != "external-message/steward/r-1" {
		t.Fatalf("receive: %+v, %v", r, err)
	}
	again, err := s.ReceiveExternal(stewardReport("r-1"), DefaultMailboxMax)
	if err != nil || again != r {
		t.Fatalf("retry: %+v, %v; want %+v", again, err, r)
	}
	changed := stewardReport("r-1")
	changed.Text = "Something else."
	if _, err := s.ReceiveExternal(changed, DefaultMailboxMax); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("conflict: %v", err)
	}
	m, err := s.External(SenderSteward, "r-1")
	if err != nil || m.Sequence != r.Sequence || m.ReceivedAt.IsZero() || m.Acknowledged {
		t.Fatalf("External: %+v, %v", m, err)
	}
	for name, bad := range map[string]func(*ExternalMessage){
		"unknown sender":    func(m *ExternalMessage) { m.Sender = SenderOtherDirector },
		"guildmaster":       func(m *ExternalMessage) { m.Sender = SenderGuildmaster },
		"empty sender":      func(m *ExternalMessage) { m.Sender = "" },
		"from mismatch":     func(m *ExternalMessage) { m.From = "web" },
		"not a report":      func(m *ExternalMessage) { m.Type, m.Kind = "note", "" },
		"no kind":           func(m *ExternalMessage) { m.Kind = "" },
		"subject two lines": func(m *ExternalMessage) { m.Subject = "a\nb" },
		"long subject":      func(m *ExternalMessage) { m.Subject = strings.Repeat("s", MaxSubjectBytes+1) },
		"blank text":        func(m *ExternalMessage) { m.Text = " " },
		"bad to":            func(m *ExternalMessage) { m.To = "a b" },
		"sequence":          func(m *ExternalMessage) { m.Sequence = 3 },
	} {
		m := stewardReport("bad-1")
		bad(&m)
		if _, err := s.ReceiveExternal(m, DefaultMailboxMax); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.External(SenderGuildmaster, "r-1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("External from guildmaster: %v", err)
	}
}

// TestReceiveExternalAdmission: the closed and mailbox_max checks apply only to a new message: an identical retry
// returns its original receipt before either, and a refusal stores nothing.
func TestReceiveExternalAdmission(t *testing.T) {
	s, _ := newStore(t)
	first, err := s.ReceiveExternal(stewardReport("r-1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveExternal(stewardReport("r-2"), 1); !errors.Is(err, ErrMailboxFull) {
		t.Errorf("at the cap: %v", err)
	}
	if r, err := s.ReceiveExternal(stewardReport("r-1"), 1); err != nil || r != first {
		t.Errorf("a retry at the cap: %+v, %v", r, err)
	}
	if _, err := s.External(SenderSteward, "r-2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused message was stored: %v", err)
	}
	if _, err := s.CloseInbox(SenderSteward, "too many", 2); !errors.Is(err, ErrInvalid) {
		t.Errorf("a close that is not the next: %v", err)
	}
	if _, err := s.CloseInbox(SenderSteward, "too many", 1); err != nil {
		t.Fatal(err)
	}
	st, err := s.InboxStateOf(SenderSteward)
	if err != nil || st.State != InboxClosed || st.Decision != "inbox-steward-1" || st.Reason != "too many" {
		t.Fatalf("state %+v, %v", st, err)
	}
	if _, err := s.ReceiveExternal(stewardReport("r-3"), DefaultMailboxMax); !errors.Is(err, ErrInboxClosed) {
		t.Errorf("closed: %v", err)
	}
	if r, err := s.ReceiveExternal(stewardReport("r-1"), DefaultMailboxMax); err != nil || r != first {
		t.Errorf("a retry to a closed inbox: %+v, %v", r, err)
	}
	if _, err := s.ReopenInbox(SenderSteward, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveExternal(stewardReport("r-3"), DefaultMailboxMax); err != nil {
		t.Errorf("after reopen: %v", err)
	}
}

// TestExternalInboxAckAndUnread: the external inbox lists by sender in receive order and is counted in the
// Director's unread by type and sender; an acknowledgement removes it once and is retried safely.
func TestExternalInboxAckAndUnread(t *testing.T) {
	s, _ := messagingStore(t)
	mustSend(t, s, toDirector("local-1"))
	for _, id := range []string{"r-1", "r-2", "r-3"} {
		if _, err := s.ReceiveExternal(stewardReport(id), DefaultMailboxMax); err != nil {
			t.Fatal(err)
		}
	}
	ms, more, err := s.ExternalInbox(SenderSteward, 0, 2)
	if err != nil || !more || len(ms) != 2 || ms[0].ID != "r-1" || ms[1].ID != "r-2" {
		t.Fatalf("page 1: %+v, %v, %v", ms, more, err)
	}
	if ms, more, err := s.ExternalInbox("", ms[1].Sequence, 2); err != nil || more || len(ms) != 1 || ms[0].ID != "r-3" {
		t.Fatalf("page 2: %+v, %v, %v", ms, more, err)
	}
	unread, err := s.DirectorUnread()
	if err != nil || unread[UnreadFrom(MessageStewardReport, SenderSteward)] != 3 {
		t.Fatalf("unread %v, %v", unread, err)
	}
	r, err := s.AckExternal(SenderSteward, "r-2")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.AckExternal(SenderSteward, "r-2"); err != nil || again != r {
		t.Errorf("ack retry: %+v, %v", again, err)
	}
	if ms, _, _ := s.ExternalInbox(SenderSteward, 0, 10); len(ms) != 2 {
		t.Errorf("after ack: %+v", ms)
	}
	if m, _ := s.External(SenderSteward, "r-2"); !m.Acknowledged {
		t.Errorf("acknowledged message %+v", m)
	}
	if unread, _ := s.DirectorUnread(); unread[UnreadFrom(MessageStewardReport, SenderSteward)] != 2 {
		t.Errorf("unread after ack %v", unread)
	}
	if _, err := s.AckExternal(SenderSteward, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ack of no message: %v", err)
	}
}

// TestExternalWake: an external message's wake is keyed by its sender and id, apart from a local message's of the
// same id.
func TestExternalWake(t *testing.T) {
	s, _ := messagingStore(t)
	mustSend(t, s, toDirector("same-id"))
	if _, err := s.ReceiveExternal(stewardReport("same-id"), DefaultMailboxMax); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWake(Wake{MessageID: "same-id", ExternalSender: SenderSteward, SenderType: "steward", Decision: WakeQueued, Reason: "no session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WakeFor("same-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the local message has a wake: %v", err)
	}
	w, err := s.ExternalWakeFor(SenderSteward, "same-id")
	if err != nil || w.ExternalSender != SenderSteward || w.Reason != "no session" {
		t.Fatalf("external wake %+v, %v", w, err)
	}
	if _, err := s.RecordWake(Wake{MessageID: "missing", ExternalSender: SenderSteward, SenderType: "steward", Decision: WakeWoke}); !errors.Is(err, ErrNotFound) {
		t.Errorf("wake of no message: %v", err)
	}
}

// TestOutbox: an entry is recorded pending once, then its attempts in order: a failure keeps it pending, a receipt
// delivers it and a refusal refuses it, each final.
func TestOutbox(t *testing.T) {
	s, _ := newStore(t)
	env := []byte(`{"schema":"guildhall.director-message/1"}`)
	r, err := s.RecordOutbox("r-1", "laptop/tools", env)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.RecordOutbox("r-1", "laptop/tools", env); err != nil || again != r {
		t.Errorf("re-record: %+v, %v", again, err)
	}
	if _, err := s.RecordOutbox("r-1", "vds/tools", env); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("another destination under the id: %v", err)
	}
	if _, err := s.RecordOutbox("r-2", "laptop/tools", []byte("not json")); !errors.Is(err, ErrInvalid) {
		t.Errorf("an envelope that is not JSON: %v", err)
	}
	if _, err := s.OutboxAttempt("r-1", 1, nil, "", "ssh: connection refused"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutboxAttempt("r-1", 3, []byte(`{}`), "", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("an attempt out of order: %v", err)
	}
	if _, err := s.OutboxAttempt("r-1", 2, []byte(`{"sequence":4}`), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutboxAttempt("r-1", 3, nil, "closed: busy", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("an attempt after delivery: %v", err)
	}
	if _, err := s.RecordOutbox("r-2", "laptop/tools", env); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutboxAttempt("r-2", 1, nil, "closed: busy", ""); err != nil {
		t.Fatal(err)
	}
	undelivered, err := s.Outbox(false)
	if err != nil || len(undelivered) != 1 || undelivered[0].ID != "r-2" || undelivered[0].State != OutboxRefused || undelivered[0].Reason != "closed: busy" {
		t.Fatalf("undelivered %+v, %v", undelivered, err)
	}
	all, _ := s.Outbox(true)
	if len(all) != 2 || all[0].State != OutboxDelivered || all[0].Attempts != 2 || all[0].LastError != "" || string(all[0].Receipt) != `{"sequence":4}` {
		t.Fatalf("all %+v", all)
	}
}

// TestMarkCopyReach: an unreachable copy keeps the first time it was found so until it is reached, which returns it.
func TestMarkCopyReach(t *testing.T) {
	s, _ := newStore(t)
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		reached bool
		at      time.Time
		want    time.Time
	}{
		{true, t0, time.Time{}},
		{false, t0.Add(time.Minute), time.Time{}},
		{false, t0.Add(time.Hour), t0.Add(time.Minute)},
		{true, t0.Add(2 * time.Hour), t0.Add(time.Minute)},
		{true, t0.Add(3 * time.Hour), time.Time{}},
	} {
		got, err := s.MarkCopyReach("laptop/tools", c.reached, c.at)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("step %d: %v, %v; want %v", i, got, err, c.want)
		}
	}
}
