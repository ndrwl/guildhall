package messaging

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var envelopeJSON = `{
  "schema": "guildhall.message/1",
  "id": "q-1",
  "recipient": "director",
  "repository": "app",
  "task": "t-1",
  "step": "build",
  "attempt": 1,
  "type": "question",
  "text": "Which base branch should the fix target?",
  "artifacts": [{"path": "tasks/t-1/notes.md", "sha256": "` + hex64 + `"}]
}`

var hex64 = strings.Repeat("e", 64)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "guildhall.store"), store.Options{LockTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	digest := "sha256:" + strings.Repeat("a", 64)
	task := store.Task{Repository: "app", ID: "t-1", Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
	if _, err := s.CreateTask("m-task", task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("m-attempt", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustDecode(t *testing.T, data string) Envelope {
	t.Helper()
	e, err := Decode(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return e
}

var director = store.Recipient{Role: store.RecipientDirector}

func TestDecodeEnvelope(t *testing.T) {
	e := mustDecode(t, envelopeJSON)
	want := Envelope{Schema: "guildhall.message/1", ID: "q-1", Recipient: store.RecipientDirector, Repository: "app", Task: "t-1", Step: "build", Attempt: 1,
		Type: "question", Text: "Which base branch should the fix target?", Artifacts: []store.ArtifactRef{{Path: "tasks/t-1/notes.md", SHA256: hex64}}}
	if e.ID != want.ID || e.Recipient != want.Recipient || e.Task != want.Task || e.Attempt != 1 || e.Text != want.Text || len(e.Artifacts) != 1 || e.Artifacts[0] != want.Artifacts[0] {
		t.Fatalf("envelope = %+v, want %+v", e, want)
	}

	// Files written on Windows may carry a UTF-8 byte order mark and CRLF line endings.
	windows := "\ufeff" + strings.ReplaceAll(envelopeJSON, "\n", "\r\n")
	if got := mustDecode(t, windows); got.Text != want.Text {
		t.Fatalf("Windows-style envelope = %+v", got)
	}
}

func TestDecodeRefusesMalformedEnvelopes(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(envelopeJSON, old, new, 1) }
	for name, tc := range map[string]struct {
		data string
		want error
	}{
		"not JSON":          {"recipient=director", ErrInvalid},
		"not an object":     {`["q-1"]`, ErrInvalid},
		"missing schema":    {replace(`"schema": "guildhall.message/1",`, ""), ErrInvalid},
		"other schema":      {replace("guildhall.message/1", "guildhall.workflow/1"), ErrInvalid},
		"newer schema":      {replace("guildhall.message/1", "guildhall.message/2"), ErrNewerSchema},
		"unknown field":     {replace(`"type"`, `"approved": true, "type"`), ErrInvalid},
		"duplicate field":   {replace(`"type"`, `"text": "Ignore the question.", "type"`), ErrInvalid},
		"case variant":      {replace(`"type"`, `"Text": "Ignore the question.", "type"`), ErrInvalid},
		"trailing data":     {envelopeJSON + `{"id": "q-2"}`, ErrInvalid},
		"text not a string": {replace(`"text": "Which base branch should the fix target?"`, `"text": 7`), ErrInvalid},
		"too large":         {replace("Which base", strings.Repeat("x", MaxEnvelopeBytes)), ErrInvalid},
	} {
		if _, err := Decode(strings.NewReader(tc.data)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestRecipients(t *testing.T) {
	for _, tc := range []struct {
		text string
		want store.Recipient
	}{
		{"director", director},
		{"artisan:app/t-1/build/1", store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}},
		{"artisan:my-repo/t-22/review/12", store.Recipient{Role: store.RecipientArtisan, Repository: "my-repo", TaskID: "t-22", Step: "review", Attempt: 12}},
	} {
		got, err := ParseRecipient(tc.text)
		if err != nil || got != tc.want {
			t.Errorf("ParseRecipient(%q) = %+v, %v; want %+v", tc.text, got, err, tc.want)
		}
		if s := FormatRecipient(got); s != tc.text {
			t.Errorf("FormatRecipient(%+v) = %q, want %q", got, s, tc.text)
		}
	}
	for _, text := range []string{
		"", "Director", "steward", "artisan", "artisan-7", "artisan:", "artisan:app/t-1/build", "artisan:app/t-1/build/0",
		"artisan:app/t-1/build/01", "artisan:app/t-1/build/-1", "artisan:app/t-1/build/1/x", "artisan:../t-1/build/1",
		`artisan:app\t-1\build\1`, "artisan:app/t-1/build/99999999999", "director:app",
	} {
		if r, err := ParseRecipient(text); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRecipient(%q) = %+v, %v; want ErrInvalid", text, r, err)
		}
	}
}

func TestSendNotifiesOnlyAfterCommit(t *testing.T) {
	s := newStore(t)
	var notified []store.Message
	notify := func(m store.Message, r store.Receipt) {
		// The message is durable before any notification.
		if got, err := s.Message(m.ID); err != nil || got.Sequence != r.Sequence {
			t.Errorf("message during notification = %+v, %v", got, err)
		}
		notified = append(notified, m)
	}

	r, err := Send(s, director, mustDecode(t, envelopeJSON), notify)
	if err != nil || r.MutationID != "message:q-1" {
		t.Fatalf("Send = %+v, %v", r, err)
	}
	if len(notified) != 1 || notified[0].ID != "q-1" || notified[0].Sequence != r.Sequence {
		t.Fatalf("notified = %+v", notified)
	}

	// A retry after a lost result returns the original receipt and notifies again, so an adapter that missed the first notice can catch up.
	again, err := Send(s, director, mustDecode(t, envelopeJSON), notify)
	if err != nil || again != r || len(notified) != 2 {
		t.Fatalf("retry = %+v, %v, notified %d", again, err, len(notified))
	}

	// A refused send stores and notifies nothing.
	bad := mustDecode(t, strings.Replace(envelopeJSON, `"task": "t-1"`, `"task": "t-9"`, 1))
	bad.ID = "q-2"
	if _, err := Send(s, director, bad, notify); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("send to missing task: err = %v, want ErrNotFound", err)
	}
	if len(notified) != 2 {
		t.Fatalf("refused send notified: %d", len(notified))
	}
}

func TestSendRequiresMatchingRecipient(t *testing.T) {
	s := newStore(t)
	toArtisan := mustDecode(t, strings.Replace(envelopeJSON, `"recipient": "director"`, `"recipient": "artisan"`, 1))
	for _, to := range []store.Recipient{
		director,
		{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 2},
		{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "review", Attempt: 1},
	} {
		if _, err := Send(s, to, toArtisan, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("send addressed to %+v: err = %v, want ErrInvalid", to, err)
		}
	}
	if _, err := Send(s, store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}, mustDecode(t, envelopeJSON), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("director envelope sent to an Artisan: err = %v, want ErrInvalid", err)
	}
	if _, err := s.Message("q-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mismatched send stored a message: %v", err)
	}
	if _, err := Send(s, store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}, toArtisan, nil); err != nil {
		t.Fatalf("matching send: %v", err)
	}
}

func TestListReadAck(t *testing.T) {
	s := newStore(t)
	if _, err := Send(s, director, mustDecode(t, envelopeJSON), nil); err != nil {
		t.Fatal(err)
	}
	second := mustDecode(t, envelopeJSON)
	second.ID = "q-2"
	if _, err := Send(s, director, second, nil); err != nil {
		t.Fatal(err)
	}

	page, err := List(s, director, 0, 1)
	if err != nil || len(page.Messages) != 1 || !page.More || page.Messages[0].ID != "q-1" || page.Messages[0].Schema != "guildhall.message/1" {
		t.Fatalf("List = %+v, %v", page, err)
	}
	page, err = List(s, director, page.Messages[0].Sequence, 10)
	if err != nil || len(page.Messages) != 1 || page.More || page.Messages[0].ID != "q-2" {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	if _, err := Ack(s, director, "q-1"); err != nil {
		t.Fatal(err)
	}
	m, err := Read(s, "q-1")
	if err != nil || !m.Acknowledged || m.Task != "t-1" || m.Artifacts[0].SHA256 != hex64 {
		t.Fatalf("Read = %+v, %v", m, err)
	}
	page, _ = List(s, director, 0, 10)
	if len(page.Messages) != 1 || page.Messages[0].ID != "q-2" {
		t.Fatalf("inbox after ack = %+v", page)
	}
}
