package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// messagingStorePath returns a store with task app/t-1 and a running build attempt 1.
func messagingStorePath(t *testing.T) string {
	t.Helper()
	path := writeStore(t)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.StartAttempt("m-attempt", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	return path
}

func envelope(id, recipient string) string {
	return fmt.Sprintf(`{
  "schema": "guildhall.message/1",
  "id": %q,
  "recipient": %q,
  "repository": "app",
  "task": "t-1",
  "step": "build",
  "attempt": 1,
  "type": "note",
  "text": "Tests pass locally; pushing the branch next."
}
`, id, recipient)
}

// writeEnvelope writes an envelope the way a Windows editor might: in a directory with a space, with a byte order mark and CRLF endings.
func writeEnvelope(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "message bodies")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "body.json")
	if err := os.WriteFile(path, []byte("\ufeff"+strings.ReplaceAll(body, "\n", "\r\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runMessages(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"messages"}, args...))
	return code, stdout.String(), stderr.String()
}

func decodeJSON[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	return v
}

func TestMessagesSendFromFileAndStdin(t *testing.T) {
	path := messagingStorePath(t)
	body := writeEnvelope(t, envelope("q-1", "director"))

	code, out, stderr := runMessages(t, "", "send", "--store", path, "director", body)
	if code != 0 {
		t.Fatalf("send from file: exit %d, stderr %s", code, stderr)
	}
	r := decodeJSON[store.Receipt](t, out)
	if r.MutationID != "message:q-1" || r.Operation != "send-message" || r.Sequence == 0 {
		t.Fatalf("receipt = %+v", r)
	}

	// An exact retry prints the original receipt.
	if code, again, _ := runMessages(t, "", "send", "--store", path, "director", body); code != 0 || again != out {
		t.Fatalf("retry: exit %d, output %q, want %q", code, again, out)
	}

	code, out, stderr = runMessages(t, envelope("n-1", "artisan"), "send", "--store", path, "artisan:app/t-1/build/1", "-")
	if code != 0 {
		t.Fatalf("send from stdin: exit %d, stderr %s", code, stderr)
	}
	if r := decodeJSON[store.Receipt](t, out); r.MutationID != "message:n-1" {
		t.Fatalf("stdin receipt = %+v", r)
	}
}

func TestMessagesSendRefusals(t *testing.T) {
	path := messagingStorePath(t)
	if code, _, stderr := runMessages(t, envelope("q-1", "director"), "send", "--store", path, "director", "-"); code != 0 {
		t.Fatalf("first send: %s", stderr)
	}
	missing := filepath.Join(t.TempDir(), "missing.store")

	for name, tc := range map[string]struct {
		stdin string
		args  []string
		code  int
		want  string
	}{
		"conflicting reuse":   {strings.Replace(envelope("q-1", "director"), "Tests pass", "Tests fail", 1), []string{"--store", path, "director", "-"}, 1, "already used"},
		"recipient mismatch":  {envelope("q-2", "director"), []string{"--store", path, "artisan:app/t-1/build/1", "-"}, 1, "addressed to"},
		"wrong attempt":       {strings.Replace(envelope("n-2", "artisan"), `"attempt": 1`, `"attempt": 2`, 1), []string{"--store", path, "artisan:app/t-1/build/2", "-"}, 1, "not found"},
		"unknown field":       {strings.Replace(envelope("q-3", "director"), `"type"`, `"approve": true, "type"`, 1), []string{"--store", path, "director", "-"}, 1, "unknown field"},
		"missing body file":   {"", []string{"--store", path, "director", filepath.Join(t.TempDir(), "none.json")}, 1, "body"},
		"missing store":       {envelope("q-4", "director"), []string{"--store", missing, "director", "-"}, 1, "no store at"},
		"no store flag":       {envelope("q-5", "director"), []string{"director", "-"}, 2, "--store"},
		"unknown recipient":   {envelope("q-6", "director"), []string{"--store", path, "steward", "-"}, 2, "recipient"},
		"too few arguments":   {"", []string{"--store", path, "director"}, 2, "usage"},
		"too many arguments":  {"", []string{"--store", path, "director", "-", "extra"}, 2, "usage"},
		"inline JSON as body": {"", []string{"--store", path, "director", `{"id":"q-7"}`}, 1, "body"},
	} {
		code, out, stderr := runMessages(t, tc.stdin, append([]string{"send"}, tc.args...)...)
		if code != tc.code || out != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit %d mentioning %q", name, code, out, stderr, tc.code, tc.want)
		}
	}
	// Sending never creates a store.
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("send created a store: %v", err)
	}
}

func TestMessagesListReadAck(t *testing.T) {
	path := messagingStorePath(t)
	for _, id := range []string{"q-1", "q-2", "q-3"} {
		if code, _, stderr := runMessages(t, envelope(id, "director"), "send", "--store", path, "director", "-"); code != 0 {
			t.Fatalf("send %s: %s", id, stderr)
		}
	}
	type page struct {
		Messages []struct {
			ID           string `json:"id"`
			Schema       string `json:"schema"`
			Sequence     uint64 `json:"sequence"`
			Acknowledged bool   `json:"acknowledged"`
		} `json:"messages"`
		More bool `json:"more"`
	}

	code, out, stderr := runMessages(t, "", "list", "--store", path, "--limit", "2", "director")
	p := decodeJSON[page](t, out)
	if code != 0 || len(p.Messages) != 2 || !p.More || p.Messages[0].ID != "q-1" || p.Messages[0].Schema != "guildhall.message/1" {
		t.Fatalf("list: exit %d, %+v, stderr %s", code, p, stderr)
	}
	after := fmt.Sprint(p.Messages[1].Sequence)
	_, out, _ = runMessages(t, "", "list", "--store", path, "--after", after, "director")
	if p := decodeJSON[page](t, out); len(p.Messages) != 1 || p.Messages[0].ID != "q-3" || p.More {
		t.Fatalf("list after %s = %+v", after, p)
	}

	code, out, stderr = runMessages(t, "", "ack", "--store", path, "director", "q-1")
	if r := decodeJSON[store.Receipt](t, out); code != 0 || r.MutationID != "message-ack:q-1" {
		t.Fatalf("ack: exit %d, %+v, stderr %s", code, r, stderr)
	}
	code, out, _ = runMessages(t, "", "read", "--store", path, "q-1")
	if m := decodeJSON[struct {
		ID           string `json:"id"`
		Text         string `json:"text"`
		Acknowledged bool   `json:"acknowledged"`
	}](t, out); code != 0 || m.ID != "q-1" || !m.Acknowledged || m.Text == "" {
		t.Fatalf("read: exit %d, %+v", code, m)
	}
	_, out, _ = runMessages(t, "", "list", "--store", path, "director")
	if p := decodeJSON[page](t, out); len(p.Messages) != 2 || p.Messages[0].ID != "q-2" {
		t.Fatalf("list after ack = %+v", p)
	}

	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"ack by wrong recipient": {[]string{"ack", "--store", path, "artisan:app/t-1/build/1", "q-2"}, 1, "addressed to"},
		"read missing":           {[]string{"read", "--store", path, "q-9"}, 1, "not found"},
		"limit over maximum":     {[]string{"list", "--store", path, "--limit", "101", "director"}, 1, "limit"},
		"limit not a number":     {[]string{"list", "--store", path, "--limit", "ten", "director"}, 2, "limit"},
		"unknown subcommand":     {[]string{"delete", "--store", path, "q-1"}, 2, "messages"},
		"no subcommand":          {nil, 2, "messages"},
	} {
		code, out, stderr := runMessages(t, "", tc.args...)
		if code != tc.code || out != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit %d mentioning %q", name, code, out, stderr, tc.code, tc.want)
		}
	}
}

func TestMessagesConcurrentSends(t *testing.T) {
	path := messagingStorePath(t)
	const n = 8
	codes := make([]int, n)
	errs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			app, _, stderr := newTestApp(nil, nil)
			app.Stdin = strings.NewReader(envelope(fmt.Sprintf("q-%d", i), "director"))
			codes[i] = app.Run([]string{"messages", "send", "--store", path, "director", "-"})
			errs[i] = stderr.String()
		})
	}
	wg.Wait()
	for i := range n {
		if codes[i] != 0 {
			t.Fatalf("send %d: exit %d, %s", i, codes[i], errs[i])
		}
	}
	_, out, _ := runMessages(t, "", "list", "--store", path, "director")
	if p := decodeJSON[struct {
		Messages []json.RawMessage `json:"messages"`
	}](t, out); len(p.Messages) != n {
		t.Fatalf("messages = %d, want %d", len(p.Messages), n)
	}
}
