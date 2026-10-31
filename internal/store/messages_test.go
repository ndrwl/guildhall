package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// messagingStore returns a store holding task app/t-1 with a running build attempt 1.
func messagingStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, path := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if _, err := s.StartAttempt("m-attempt", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	return s, path
}

var artisanInbox = Recipient{Role: RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}

func toArtisan(id string) Message {
	return Message{ID: id, Recipient: RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1, Type: "note", Text: "Rebase onto main first."}
}

func toDirector(id string) Message {
	return Message{ID: id, Recipient: RecipientDirector, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1, Type: "question", Text: "Which branch?",
		Artifacts: []ArtifactRef{{Path: "tasks/t-1/notes.md", SHA256: strings.Repeat("e", 64)}}}
}

func mustSend(t *testing.T, s *Store, m Message) Receipt {
	t.Helper()
	r, err := s.SendMessage(m)
	if err != nil {
		t.Fatalf("SendMessage(%s): %v", m.ID, err)
	}
	return r
}

func inboxIDs(t *testing.T, s *Store, to Recipient, after uint64, limit int) ([]string, bool) {
	t.Helper()
	msgs, more, err := s.Inbox(to, after, limit)
	if err != nil {
		t.Fatalf("Inbox(%+v): %v", to, err)
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids, more
}

func TestSendMessageCommitsMessageAndReceipt(t *testing.T) {
	s, path := messagingStore(t)
	r := mustSend(t, s, toDirector("q-1"))
	if r.MutationID != "message:q-1" || r.Operation != "send-message" || r.Repository != "app" || r.TaskID != "t-1" || r.Attempt != 1 || r.Sequence != 3 {
		t.Fatalf("receipt = %+v", r)
	}
	_ = s.Close()

	s = openTest(t, path)
	got, err := s.Message("q-1")
	if err != nil {
		t.Fatalf("Message after restart: %v", err)
	}
	want := toDirector("q-1")
	want.Sequence = r.Sequence
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("message = %+v, want %+v", got, want)
	}
	if stored, err := s.Receipt("message:q-1"); err != nil || stored != r {
		t.Fatalf("receipt after restart = %+v, %v", stored, err)
	}
	if ids, _ := inboxIDs(t, s, Recipient{Role: RecipientDirector}, 0, 10); fmt.Sprint(ids) != "[q-1]" {
		t.Fatalf("director inbox after restart = %v", ids)
	}
}

func TestSendMessageValidates(t *testing.T) {
	s, _ := messagingStore(t)
	for name, mod := range map[string]func(*Message){
		"blank id":            func(m *Message) { m.ID = "" },
		"id with slash":       func(m *Message) { m.ID = "../q" },
		"id with colon":       func(m *Message) { m.ID = "a:b" },
		"long id":             func(m *Message) { m.ID = strings.Repeat("a", 101) },
		"unknown recipient":   func(m *Message) { m.Recipient = "steward" },
		"bad repository":      func(m *Message) { m.Repository = "App" },
		"bad task":            func(m *Message) { m.TaskID = "" },
		"bad type":            func(m *Message) { m.Type = "Question!" },
		"blank text":          func(m *Message) { m.Text = " \n" },
		"long text":           func(m *Message) { m.Text = strings.Repeat("x", MaxMessageTextBytes+1) },
		"invalid utf-8 text":  func(m *Message) { m.Text = "\xff" },
		"step without number": func(m *Message) { m.Attempt = 0 },
		"number without step": func(m *Message) { m.Step = "" },
		"traversal artifact":  func(m *Message) { m.Artifacts[0].Path = "../secret" },
		"absolute artifact":   func(m *Message) { m.Artifacts[0].Path = "/etc/passwd" },
		"backslash artifact":  func(m *Message) { m.Artifacts[0].Path = `tasks\t-1` },
		"bad artifact digest": func(m *Message) { m.Artifacts[0].SHA256 = strings.Repeat("E", 64) },
		"too many artifacts": func(m *Message) {
			m.Artifacts = make([]ArtifactRef, MaxMessageArtifacts+1)
			for i := range m.Artifacts {
				m.Artifacts[i] = ArtifactRef{Path: fmt.Sprintf("a/%d", i), SHA256: strings.Repeat("e", 64)}
			}
		},
		"preset sequence":     func(m *Message) { m.Sequence = 7 },
		"preset acknowledged": func(m *Message) { m.Acknowledged = true },
	} {
		m := toDirector("q-1")
		mod(&m)
		if _, err := s.SendMessage(m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if n := countReceipts(t, s); n != 2 {
		t.Fatalf("refused sends wrote receipts: %d, want 2", n)
	}
}

func TestSendMessageChecksTaskAndAttempt(t *testing.T) {
	s, _ := messagingStore(t)
	for name, tc := range map[string]struct {
		mod  func(*Message)
		want error
	}{
		"missing repository":     {func(m *Message) { m.Repository = "other" }, ErrNotFound},
		"missing task":           {func(m *Message) { m.TaskID = "t-9" }, ErrNotFound},
		"missing step":           {func(m *Message) { m.Step = "review" }, ErrNotFound},
		"missing attempt number": {func(m *Message) { m.Attempt = 2 }, ErrNotFound},
		"artisan without attempt": {func(m *Message) {
			m.Recipient, m.Step, m.Attempt = RecipientArtisan, "", 0
		}, ErrInvalid},
	} {
		for _, base := range []Message{toArtisan("n-1"), toDirector("q-1")} {
			if base.Recipient == RecipientDirector && name == "artisan without attempt" {
				continue
			}
			m := base
			tc.mod(&m)
			if _, err := s.SendMessage(m); !errors.Is(err, tc.want) {
				t.Errorf("%s to %s: err = %v, want %v", name, base.Recipient, err, tc.want)
			}
		}
	}

	// A Director message may concern only the task, with no attempt.
	taskOnly := toDirector("q-task")
	taskOnly.Step, taskOnly.Attempt = "", 0
	mustSend(t, s, taskOnly)

	// An Artisan inbox exists only while its attempt runs; the Director may still hear about a finished attempt.
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(toArtisan("n-late")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("send to finished attempt: err = %v, want ErrInvalid", err)
	}
	mustSend(t, s, toDirector("q-late"))
}

func TestSendMessageReplayAndConflict(t *testing.T) {
	s, path := messagingStore(t)
	first := mustSend(t, s, toArtisan("n-1"))
	if _, err := s.AckMessage(artisanInbox, "n-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// An exact retry after restart, acknowledgement and the attempt finishing returns the original receipt and resends nothing.
	s = openTest(t, path)
	again, err := s.SendMessage(toArtisan("n-1"))
	if err != nil || again != first {
		t.Fatalf("retry = %+v, %v; want %+v", again, err, first)
	}
	if m, _ := s.Message("n-1"); !m.Acknowledged {
		t.Fatalf("retry reset acknowledgement: %+v", m)
	}

	changed := toArtisan("n-1")
	changed.Text = "Something else."
	if _, err := s.SendMessage(changed); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("reused id with new text: err = %v, want ErrMutationConflict", err)
	}
	// A message id is also refused when its mutation id was used by another operation.
	if _, err := s.CreateTask("message:q-x", sampleTask("app", "t-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(toDirector("q-x")); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("id colliding with another mutation: err = %v, want ErrMutationConflict", err)
	}
	if m, _ := s.Message("n-1"); m.Text != toArtisan("n-1").Text {
		t.Fatalf("conflict changed message: %+v", m)
	}
}

func TestSendMessageIsAtomic(t *testing.T) {
	s, path := messagingStore(t)
	injected := errors.New("injected failure after message write")
	testHookBeforeCommit = func() error { return injected }
	t.Cleanup(func() { testHookBeforeCommit = nil })

	if _, err := s.SendMessage(toDirector("q-1")); !errors.Is(err, injected) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	testHookBeforeCommit = nil
	_ = s.Close()

	s = openTest(t, path)
	if _, err := s.Message("q-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message survived a rolled-back send: %v", err)
	}
	if _, err := s.Receipt("message:q-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("receipt survived a rolled-back send: %v", err)
	}
	if ids, _ := inboxIDs(t, s, Recipient{Role: RecipientDirector}, 0, 10); len(ids) != 0 {
		t.Fatalf("inbox entry survived a rolled-back send: %v", ids)
	}
	if r := mustSend(t, s, toDirector("q-1")); r.Sequence != 3 {
		t.Fatalf("rolled-back send consumed a sequence number: %+v", r)
	}
}

func TestConcurrentSends(t *testing.T) {
	s, _ := messagingStore(t)
	const n = 16
	errs := make([]error, 2*n)
	receipts := make([]Receipt, 2*n)
	var wg sync.WaitGroup
	for i := range n {
		// Distinct messages all land; the same message sent concurrently lands once.
		wg.Go(func() { _, errs[i] = s.SendMessage(toDirector(fmt.Sprintf("q-%02d", i))) })
		wg.Go(func() { receipts[n+i], errs[n+i] = s.SendMessage(toArtisan("n-same")) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if i >= n && receipts[i] != receipts[n] {
			t.Fatalf("same message receipt %d = %+v, want %+v", i, receipts[i], receipts[n])
		}
	}
	ids, more := inboxIDs(t, s, Recipient{Role: RecipientDirector}, 0, MaxInboxLimit)
	if len(ids) != n || more {
		t.Fatalf("director inbox = %d messages (more %v), want %d", len(ids), more, n)
	}
	if ids, _ := inboxIDs(t, s, artisanInbox, 0, MaxInboxLimit); fmt.Sprint(ids) != "[n-same]" {
		t.Fatalf("artisan inbox = %v", ids)
	}
}

func TestInboxIsBoundedOrderedAndSeparated(t *testing.T) {
	s, _ := messagingStore(t)
	mustCreate(t, s, "m-task-2", sampleTask("lib", "t-1"))
	if _, err := s.StartAttempt("m-attempt-2", "app", "t-1", "review"); err != nil {
		t.Fatal(err)
	}
	other := toDirector("q-lib")
	other.Repository, other.Step, other.Attempt = "lib", "", 0

	mustSend(t, s, toDirector("q-1"))
	mustSend(t, s, toArtisan("n-1"))
	mustSend(t, s, other)
	mustSend(t, s, toDirector("q-2"))
	review := toArtisan("n-review")
	review.Step = "review"
	mustSend(t, s, review)

	director := Recipient{Role: RecipientDirector}
	// The Director's inbox spans repositories in send order and pages by sequence.
	ids, more := inboxIDs(t, s, director, 0, 2)
	if fmt.Sprint(ids) != "[q-1 q-lib]" || !more {
		t.Fatalf("first page = %v (more %v)", ids, more)
	}
	msgs, _, _ := s.Inbox(director, 0, 2)
	ids, more = inboxIDs(t, s, director, msgs[1].Sequence, 2)
	if fmt.Sprint(ids) != "[q-2]" || more {
		t.Fatalf("second page = %v (more %v)", ids, more)
	}
	// Each Artisan sees only its own attempt's messages.
	if ids, _ := inboxIDs(t, s, artisanInbox, 0, 10); fmt.Sprint(ids) != "[n-1]" {
		t.Fatalf("build inbox = %v", ids)
	}
	reviewer := artisanInbox
	reviewer.Step = "review"
	if ids, _ := inboxIDs(t, s, reviewer, 0, 10); fmt.Sprint(ids) != "[n-review]" {
		t.Fatalf("review inbox = %v", ids)
	}

	for name, tc := range map[string]struct {
		to    Recipient
		limit int
	}{
		"zero limit":              {director, 0},
		"limit over maximum":      {director, MaxInboxLimit + 1},
		"director with task":      {Recipient{Role: RecipientDirector, Repository: "app", TaskID: "t-1"}, 10},
		"artisan without attempt": {Recipient{Role: RecipientArtisan, Repository: "app", TaskID: "t-1"}, 10},
		"unknown role":            {Recipient{Role: "guildmaster"}, 10},
		"steward with task":       {Recipient{Role: RecipientSteward, Repository: "app", TaskID: "t-1"}, 10},
	} {
		if _, _, err := s.Inbox(tc.to, 0, tc.limit); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestAckMessageConsumesInboxDurably(t *testing.T) {
	s, path := messagingStore(t)
	mustSend(t, s, toArtisan("n-1"))
	mustSend(t, s, toArtisan("n-2"))
	sent := mustSend(t, s, toDirector("q-1"))

	// Acknowledgement is addressed: the wrong recipient cannot consume a message.
	for _, wrong := range []Recipient{
		{Role: RecipientDirector},
		{Role: RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 2},
	} {
		if _, err := s.AckMessage(wrong, "n-1"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ack by %+v: err = %v, want ErrInvalid", wrong, err)
		}
	}
	if _, err := s.AckMessage(artisanInbox, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ack of missing message: err = %v, want ErrNotFound", err)
	}

	ack, err := s.AckMessage(artisanInbox, "n-1")
	if err != nil || ack.MutationID != "message-ack:n-1" || ack.Operation != "ack-message" || ack.Attempt != 1 {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	_ = s.Close()

	// After a restart the acknowledged message is gone from the inbox, the unread one is not, and a retried ack returns the original receipt.
	s = openTest(t, path)
	if ids, _ := inboxIDs(t, s, artisanInbox, 0, 10); fmt.Sprint(ids) != "[n-2]" {
		t.Fatalf("inbox after ack and restart = %v", ids)
	}
	if again, err := s.AckMessage(artisanInbox, "n-1"); err != nil || again != ack {
		t.Fatalf("retried ack = %+v, %v; want %+v", again, err, ack)
	}
	if m, err := s.Message("n-1"); err != nil || !m.Acknowledged {
		t.Fatalf("acknowledged message = %+v, %v", m, err)
	}

	// Acknowledgement is not a task result: the attempt keeps running and the task stays open.
	attempts, _ := s.Attempts("app", "t-1")
	task, _ := s.Task("app", "t-1")
	if len(attempts) != 1 || attempts[0].Outcome != "" || task.State != TaskOpen {
		t.Fatalf("ack changed lifecycle: attempts %+v, task %+v", attempts, task)
	}

	// An Artisan's message can still be consumed after its attempt finished.
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckMessage(artisanInbox, "n-2"); err != nil {
		t.Fatalf("ack after attempt finished: %v", err)
	}
	if _, err := s.AckMessage(Recipient{Role: RecipientDirector}, "q-1"); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Receipt("message:q-1"); r != sent {
		t.Fatalf("ack changed the send receipt: %+v", r)
	}
}

func TestMessageLookup(t *testing.T) {
	s, _ := messagingStore(t)
	if _, err := s.Message("q-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message: err = %v, want ErrNotFound", err)
	}
	// A mutation id that looks like a message's but belongs to another operation is not a message.
	mustCreate(t, s, "message:q-2", sampleTask("app", "t-2"))
	if _, err := s.Message("q-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-message receipt: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Message("../q"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid id: err = %v, want ErrInvalid", err)
	}
}

func TestDoctorCountsMessages(t *testing.T) {
	s, _ := messagingStore(t)
	mustSend(t, s, toDirector("q-1"))
	mustSend(t, s, toArtisan("n-1"))
	if _, err := s.AckMessage(artisanInbox, "n-1"); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	if len(rep.Repositories) != 1 || rep.Repositories[0].Messages != 2 || rep.Repositories[0].UnreadMessages != 1 {
		t.Fatalf("repositories = %+v", rep.Repositories)
	}
}
