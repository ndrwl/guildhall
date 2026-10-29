// Package messagingtest holds the behavioural contract every messaging.Mailbox
// must pass and an in-memory Fake that passes it, for testing consumers in isolation.
package messagingtest

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// Harness gives the contract suite a fresh Mailbox and the records it checks
// messages against. Each call to the suite's factory returns a new, empty one.
type Harness interface {
	Mailbox() messaging.Mailbox
	// CreateTask records an open task.
	CreateTask(t *testing.T, repository, task string)
	// StartAttempt starts the next attempt of step and returns its number.
	StartAttempt(t *testing.T, repository, task, step string) int
	// FinishAttempt finishes a running attempt.
	FinishAttempt(t *testing.T, repository, task, step string, number int)
	// Restart drops everything that is not durable, as a process restart would;
	// Mailbox afterwards returns a mailbox over the same durable state.
	Restart(t *testing.T)
}

// Director is the Director's recipient.
var Director = store.Recipient{Role: store.RecipientDirector}

// BuildArtisan is the Artisan of attempt 1 of step "build" of app/t-1, which every case starts running.
var BuildArtisan = store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}

// Envelope returns a valid envelope about app/t-1 build attempt 1, addressed to the given role.
func Envelope(id string, role store.RecipientRole) messaging.Envelope {
	return messaging.Envelope{
		Schema: messaging.SchemaPrefix + "1", ID: id, Recipient: role, Repository: "app", Task: "t-1", Step: "build", Attempt: 1,
		Type: "note", Text: "Tests pass locally; pushing the branch next.",
		Artifacts: []store.ArtifactRef{{Path: "tasks/t-1/notes.md", SHA256: strings.Repeat("e", 64)}},
	}
}

// RunMailboxContract runs the Mailbox contract against harnesses from newHarness.
func RunMailboxContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	setup := func(t *testing.T) Harness {
		t.Helper()
		h := newHarness(t)
		h.CreateTask(t, "app", "t-1")
		if n := h.StartAttempt(t, "app", "t-1", "build"); n != 1 {
			t.Fatalf("first build attempt = %d, want 1", n)
		}
		return h
	}
	for _, c := range []struct {
		name string
		run  func(t *testing.T, h Harness)
	}{
		{"SendThenRead", contractSendThenRead},
		{"SendRecordsTheCallersSender", contractSender},
		{"SendRequiresMatchingRecipient", contractMatchingRecipient},
		{"SendChecksTaskAndAttempt", contractTaskAndAttempt},
		{"SendRefusesInvalidContent", contractInvalidContent},
		{"RetryAndConflict", contractRetryAndConflict},
		{"ArtifactOwnership", contractArtifactOwnership},
		{"EmptyArtifactRetry", contractEmptyArtifactRetry},
		{"ConcurrentSends", contractConcurrentSends},
		{"ListIsBoundedOrderedAndSeparated", contractList},
		{"AckConsumesAddressedMessages", contractAck},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, setup(t)) })
	}
}

func mustSend(t *testing.T, b messaging.Mailbox, to store.Recipient, e messaging.Envelope) store.Receipt {
	t.Helper()
	r, err := b.Send(to, store.SenderGuildmaster, e)
	if err != nil {
		t.Fatalf("Send(%s): %v", e.ID, err)
	}
	return r
}

func ids(t *testing.T, b messaging.Mailbox, to store.Recipient, after uint64, limit int) (string, bool) {
	t.Helper()
	page, err := b.List(to, after, limit)
	if err != nil {
		t.Fatalf("List(%s): %v", messaging.FormatRecipient(to), err)
	}
	out := make([]string, len(page.Messages))
	for i, m := range page.Messages {
		out[i] = m.ID
	}
	return strings.Join(out, " "), page.More
}

func contractSendThenRead(t *testing.T, h Harness) {
	e := Envelope("q-1", store.RecipientDirector)
	r := mustSend(t, h.Mailbox(), Director, e)
	if r.MutationID != "message:q-1" || r.Operation != "send-message" || r.Repository != "app" || r.TaskID != "t-1" || r.Attempt != 1 || r.Sequence == 0 {
		t.Fatalf("receipt = %+v", r)
	}
	h.Restart(t)
	got, err := h.Mailbox().Read("q-1")
	if err != nil {
		t.Fatalf("Read after restart: %v", err)
	}
	if fmt.Sprint(got.Envelope) != fmt.Sprint(e) || got.Sequence != r.Sequence || got.Acknowledged {
		t.Fatalf("Read = %+v, want %+v at sequence %d", got, e, r.Sequence)
	}
	// Reading does not acknowledge.
	if s, _ := ids(t, h.Mailbox(), Director, 0, 10); s != "q-1" {
		t.Fatalf("inbox after read = %q", s)
	}
	if _, err := h.Mailbox().Read("q-9"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Read missing: err = %v, want store.ErrNotFound", err)
	}
	if _, err := h.Mailbox().Read("../q"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Read invalid id: err = %v, want store.ErrInvalid", err)
	}
}

// contractSender: Send records the sender its caller gives, never one from the envelope; a resend from another
// sender returns the original receipt and keeps the first sender; a message with no sender, as one recorded before
// senders were, reads without one; and a sender outside the vocabulary is refused (d33).
func contractSender(t *testing.T, h Harness) {
	b := h.Mailbox()
	first, err := b.Send(Director, store.SenderGuildmaster, Envelope("q-1", store.RecipientDirector))
	if err != nil {
		t.Fatal(err)
	}
	mustSend(t, b, BuildArtisan, Envelope("n-1", store.RecipientArtisan))
	h.Restart(t)
	b = h.Mailbox()
	again, err := b.Send(Director, store.SenderArtisan, Envelope("q-1", store.RecipientDirector))
	if err != nil || again != first {
		t.Fatalf("resend from another sender = %+v, %v; want %+v", again, err, first)
	}
	if m, err := b.Read("q-1"); err != nil || m.Sender != store.SenderGuildmaster {
		t.Fatalf("Read after resend = %+v, %v; want sender %q", m, err, store.SenderGuildmaster)
	}
	page, err := b.List(Director, 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Sender != store.SenderGuildmaster {
		t.Fatalf("List = %+v, %v", page, err)
	}
	old, err := b.Send(Director, "", Envelope("q-none", store.RecipientDirector))
	if err != nil {
		t.Fatal(err)
	}
	// A message sent before senders were recorded and replayed after, as the engine's outbox replays, is the same request.
	if replay, err := b.Send(Director, store.SenderGuildhall, Envelope("q-none", store.RecipientDirector)); err != nil || replay != old {
		t.Fatalf("replay of a message with no sender = %+v, %v; want %+v", replay, err, old)
	}
	if m, err := b.Read("q-none"); err != nil || m.Sender != "" || store.MessageSender(store.Message{Sender: m.Sender}) != store.SenderUnknown {
		t.Fatalf("message with no sender = %+v, %v", m, err)
	}
	for _, sender := range []string{"Guildmaster", "director:copy-2", "x\x1b[2J"} {
		if _, err := b.Send(Director, sender, Envelope("q-bad", store.RecipientDirector)); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("sender %q: err = %v, want store.ErrInvalid", sender, err)
		}
	}
}

func contractMatchingRecipient(t *testing.T, h Harness) {
	b := h.Mailbox()
	toArtisan := Envelope("n-1", store.RecipientArtisan)
	for _, to := range []store.Recipient{
		Director,
		{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 2},
		{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "review", Attempt: 1},
	} {
		if _, err := b.Send(to, store.SenderGuildmaster, toArtisan); !errors.Is(err, messaging.ErrInvalid) {
			t.Errorf("artisan envelope sent to %s: err = %v, want messaging.ErrInvalid", messaging.FormatRecipient(to), err)
		}
	}
	if _, err := b.Send(BuildArtisan, store.SenderGuildmaster, Envelope("q-1", store.RecipientDirector)); !errors.Is(err, messaging.ErrInvalid) {
		t.Errorf("director envelope sent to an Artisan: err = %v, want messaging.ErrInvalid", err)
	}
	wrongSchema := Envelope("q-2", store.RecipientDirector)
	wrongSchema.Schema = messaging.SchemaPrefix + "2"
	if _, err := b.Send(Director, store.SenderGuildmaster, wrongSchema); !errors.Is(err, messaging.ErrInvalid) {
		t.Errorf("unsupported schema: err = %v, want messaging.ErrInvalid", err)
	}
	for _, id := range []string{"n-1", "q-1", "q-2"} {
		if _, err := b.Read(id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused send stored %s: %v", id, err)
		}
	}
	mustSend(t, b, BuildArtisan, toArtisan)
}

func contractTaskAndAttempt(t *testing.T, h Harness) {
	b := h.Mailbox()
	for name, mod := range map[string]func(*messaging.Envelope){
		"missing repository": func(e *messaging.Envelope) { e.Repository = "other" },
		"missing task":       func(e *messaging.Envelope) { e.Task = "t-9" },
		"missing step":       func(e *messaging.Envelope) { e.Step = "review" },
		"missing attempt":    func(e *messaging.Envelope) { e.Attempt = 2 },
	} {
		for _, role := range []store.RecipientRole{store.RecipientDirector, store.RecipientArtisan} {
			e := Envelope("m-"+string(role), role)
			mod(&e)
			to := Director
			if role == store.RecipientArtisan {
				to = store.Recipient{Role: role, Repository: e.Repository, TaskID: e.Task, Step: e.Step, Attempt: e.Attempt}
			}
			if _, err := b.Send(to, store.SenderGuildmaster, e); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s to %s: err = %v, want store.ErrNotFound", name, role, err)
			}
		}
	}

	// A Director message may concern only a task.
	taskOnly := Envelope("q-task", store.RecipientDirector)
	taskOnly.Step, taskOnly.Attempt = "", 0
	mustSend(t, b, Director, taskOnly)

	// An Artisan inbox takes messages only while its attempt runs; the Director may still hear about a finished attempt.
	h.FinishAttempt(t, "app", "t-1", "build", 1)
	if _, err := b.Send(BuildArtisan, store.SenderGuildmaster, Envelope("n-late", store.RecipientArtisan)); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("send to finished attempt: err = %v, want store.ErrInvalid", err)
	}
	mustSend(t, b, Director, Envelope("q-late", store.RecipientDirector))
}

func contractInvalidContent(t *testing.T, h Harness) {
	b := h.Mailbox()
	for name, mod := range map[string]func(*messaging.Envelope){
		"blank id":            func(e *messaging.Envelope) { e.ID = "" },
		"traversal id":        func(e *messaging.Envelope) { e.ID = "../q" },
		"bad type":            func(e *messaging.Envelope) { e.Type = "Approve!" },
		"blank text":          func(e *messaging.Envelope) { e.Text = "\t" },
		"long text":           func(e *messaging.Envelope) { e.Text = strings.Repeat("x", store.MaxMessageTextBytes+1) },
		"invalid utf-8":       func(e *messaging.Envelope) { e.Text = "\xff" },
		"step without number": func(e *messaging.Envelope) { e.Attempt = 0 },
		"traversal artifact":  func(e *messaging.Envelope) { e.Artifacts[0].Path = "../secret" },
		"windows artifact":    func(e *messaging.Envelope) { e.Artifacts[0].Path = `C:\secret` },
		"bad digest":          func(e *messaging.Envelope) { e.Artifacts[0].SHA256 = "abc" },
	} {
		e := Envelope("q-1", store.RecipientDirector)
		e.Artifacts = append([]store.ArtifactRef(nil), e.Artifacts...)
		mod(&e)
		if _, err := b.Send(Director, store.SenderGuildmaster, e); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want store.ErrInvalid", name, err)
		}
	}
	if s, _ := ids(t, b, Director, 0, 10); s != "" {
		t.Fatalf("refused sends reached the inbox: %q", s)
	}
}

func contractRetryAndConflict(t *testing.T, h Harness) {
	first := mustSend(t, h.Mailbox(), BuildArtisan, Envelope("n-1", store.RecipientArtisan))
	if _, err := h.Mailbox().Ack(BuildArtisan, "n-1"); err != nil {
		t.Fatal(err)
	}
	h.FinishAttempt(t, "app", "t-1", "build", 1)
	h.Restart(t)

	// An exact retry after restart, acknowledgement and the attempt finishing returns the original receipt and resends nothing.
	b := h.Mailbox()
	again, err := b.Send(BuildArtisan, store.SenderGuildmaster, Envelope("n-1", store.RecipientArtisan))
	if err != nil || again != first {
		t.Fatalf("retry = %+v, %v; want %+v", again, err, first)
	}
	if m, _ := b.Read("n-1"); !m.Acknowledged {
		t.Fatalf("retry reset acknowledgement: %+v", m)
	}
	changed := Envelope("n-1", store.RecipientArtisan)
	changed.Text = "Something else."
	if _, err := b.Send(BuildArtisan, store.SenderGuildmaster, changed); !errors.Is(err, store.ErrMutationConflict) {
		t.Fatalf("reused id: err = %v, want store.ErrMutationConflict", err)
	}
	if m, _ := b.Read("n-1"); m.Text != Envelope("n-1", "").Text {
		t.Fatalf("conflict changed the message: %+v", m)
	}
}

func contractArtifactOwnership(t *testing.T, h Harness) {
	b := h.Mailbox()
	e := Envelope("q-owned", store.RecipientDirector)
	want := e.Artifacts[0]
	mustSend(t, b, Director, e)
	e.Artifacts[0] = store.ArtifactRef{Path: "changed.md", SHA256: strings.Repeat("a", 64)}
	got, err := b.Read(e.ID)
	if err != nil || len(got.Artifacts) != 1 || got.Artifacts[0] != want {
		t.Fatalf("Send retained caller artifacts: %+v, %v", got, err)
	}
	got.Artifacts[0] = e.Artifacts[0]
	page, err := b.List(Director, 0, 10)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Artifacts) != 1 || page.Messages[0].Artifacts[0] != want {
		t.Fatalf("Read exposed stored artifacts: %+v, %v", page, err)
	}
	page.Messages[0].Artifacts[0] = e.Artifacts[0]
	got, err = b.Read(e.ID)
	if err != nil || len(got.Artifacts) != 1 || got.Artifacts[0] != want {
		t.Fatalf("List exposed stored artifacts: %+v, %v", got, err)
	}
}

func contractEmptyArtifactRetry(t *testing.T, h Harness) {
	for i, artifacts := range [][]store.ArtifactRef{nil, {}} {
		e := Envelope(fmt.Sprintf("q-empty-%d", i), store.RecipientDirector)
		e.Artifacts = artifacts
		first := mustSend(t, h.Mailbox(), Director, e)
		if _, err := h.Mailbox().Ack(Director, e.ID); err != nil {
			t.Fatal(err)
		}
		h.Restart(t)
		if artifacts == nil {
			e.Artifacts = []store.ArtifactRef{}
		} else {
			e.Artifacts = nil
		}
		if again := mustSend(t, h.Mailbox(), Director, e); again != first {
			t.Fatalf("empty artifacts retry = %+v, want %+v", again, first)
		}
	}
}

func contractConcurrentSends(t *testing.T, h Harness) {
	b := h.Mailbox()
	const n = 16
	errs := make([]error, 2*n)
	receipts := make([]store.Receipt, 2*n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = b.Send(Director, store.SenderGuildmaster, Envelope(fmt.Sprintf("q-%02d", i), store.RecipientDirector))
		})
		wg.Go(func() {
			receipts[n+i], errs[n+i] = b.Send(BuildArtisan, store.SenderGuildmaster, Envelope("n-same", store.RecipientArtisan))
		})
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
	page, err := b.List(Director, 0, store.MaxInboxLimit)
	if err != nil || len(page.Messages) != n || page.More {
		t.Fatalf("director inbox = %d messages (more %v), %v; want %d", len(page.Messages), page.More, err, n)
	}
	if s, _ := ids(t, b, BuildArtisan, 0, 10); s != "n-same" {
		t.Fatalf("artisan inbox = %q", s)
	}
}

func contractList(t *testing.T, h Harness) {
	h.CreateTask(t, "lib", "t-1")
	h.StartAttempt(t, "app", "t-1", "review")
	b := h.Mailbox()
	other := Envelope("q-lib", store.RecipientDirector)
	other.Repository, other.Step, other.Attempt = "lib", "", 0
	review := Envelope("n-review", store.RecipientArtisan)
	review.Step = "review"
	reviewer := BuildArtisan
	reviewer.Step = "review"

	mustSend(t, b, Director, Envelope("q-1", store.RecipientDirector))
	mustSend(t, b, BuildArtisan, Envelope("n-1", store.RecipientArtisan))
	mustSend(t, b, Director, other)
	mustSend(t, b, Director, Envelope("q-2", store.RecipientDirector))
	mustSend(t, b, reviewer, review)

	// The Director's inbox spans repositories in send order and pages by sequence.
	page, err := b.List(Director, 0, 2)
	if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != "q-1" || page.Messages[1].ID != "q-lib" || !page.More {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if s, more := ids(t, b, Director, page.Messages[1].Sequence, 2); s != "q-2" || more {
		t.Fatalf("second page = %q (more %v)", s, more)
	}
	// Each Artisan sees only its own attempt's messages.
	if s, _ := ids(t, b, BuildArtisan, 0, 10); s != "n-1" {
		t.Fatalf("build inbox = %q", s)
	}
	if s, _ := ids(t, b, reviewer, 0, 10); s != "n-review" {
		t.Fatalf("review inbox = %q", s)
	}
	for name, tc := range map[string]struct {
		to    store.Recipient
		limit int
	}{
		"zero limit":              {Director, 0},
		"limit over maximum":      {Director, store.MaxInboxLimit + 1},
		"director with task":      {store.Recipient{Role: store.RecipientDirector, Repository: "app", TaskID: "t-1"}, 10},
		"artisan without attempt": {store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1"}, 10},
		"unknown role":            {store.Recipient{Role: "steward"}, 10},
	} {
		if _, err := b.List(tc.to, 0, tc.limit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want store.ErrInvalid", name, err)
		}
	}
}

func contractAck(t *testing.T, h Harness) {
	b := h.Mailbox()
	mustSend(t, b, BuildArtisan, Envelope("n-1", store.RecipientArtisan))
	mustSend(t, b, BuildArtisan, Envelope("n-2", store.RecipientArtisan))
	mustSend(t, b, Director, Envelope("q-1", store.RecipientDirector))

	for _, wrong := range []store.Recipient{Director, {Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 2}} {
		if _, err := b.Ack(wrong, "n-1"); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("ack by %s: err = %v, want store.ErrInvalid", messaging.FormatRecipient(wrong), err)
		}
	}
	if _, err := b.Ack(BuildArtisan, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ack of missing message: err = %v, want store.ErrNotFound", err)
	}
	ack, err := b.Ack(BuildArtisan, "n-1")
	if err != nil || ack.MutationID != "message-ack:n-1" || ack.Operation != "ack-message" || ack.Attempt != 1 {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	h.Restart(t)

	// After a restart the acknowledged message is gone from the inbox, the unread ones are not, and a retried ack returns the original receipt.
	b = h.Mailbox()
	if s, _ := ids(t, b, BuildArtisan, 0, 10); s != "n-2" {
		t.Fatalf("inbox after ack and restart = %q", s)
	}
	if again, err := b.Ack(BuildArtisan, "n-1"); err != nil || again != ack {
		t.Fatalf("retried ack = %+v, %v; want %+v", again, err, ack)
	}
	if m, err := b.Read("n-1"); err != nil || !m.Acknowledged {
		t.Fatalf("acknowledged message = %+v, %v", m, err)
	}
	// A message addressed to a finished attempt can still be consumed.
	h.FinishAttempt(t, "app", "t-1", "build", 1)
	if _, err := b.Ack(BuildArtisan, "n-2"); err != nil {
		t.Fatalf("ack after the attempt finished: %v", err)
	}
	if s, _ := ids(t, b, Director, 0, 10); s != "q-1" {
		t.Fatalf("director inbox = %q", s)
	}
}
