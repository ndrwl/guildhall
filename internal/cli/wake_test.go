package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// typedPanes is the DirectorPane double for command tests: it records each line typed.
type typedPanes struct{ lines []string }

func (p *typedPanes) TypeLine(_ context.Context, pane store.DirectorPane, line string) error {
	p.lines = append(p.lines, pane.Pane+" "+line)
	return nil
}

// startSession records a running Director session in the store at path, bound to a pane unless pane is zero.
func startSession(t *testing.T, path string, pane store.DirectorPane) {
	t.Helper()
	s, err := store.Open(path, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	r, err := s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
		Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64), Pane: pane})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDirectorState(1, store.DirectorRunning, r.Sequence); err != nil {
		t.Fatal(err)
	}
}

func readWakes(t *testing.T, path string) []store.Wake {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ws, _, err := s.Wakes(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestMessagesSendToTheDirectorWakesIt(t *testing.T) {
	path := messagingStorePath(t)
	startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	panes := &typedPanes{}
	send := func() (int, string, string) {
		app, stdout, stderr := newTestApp(nil, nil)
		app.Stdin = strings.NewReader(envelope("n-1", "director"))
		app.DirectorPane = panes
		code := app.Run([]string{"messages", "send", "--store", path, "director", "-"})
		return code, stdout.String(), stderr.String()
	}
	code, out, stderr := send()
	if code != 0 || stderr != "" {
		t.Fatalf("send: exit %d, stderr %s", code, stderr)
	}
	if r := decodeJSON[store.Receipt](t, out); r.MutationID != "message:n-1" {
		t.Fatalf("receipt = %+v", r)
	}
	if want := []string{"w1:p2 guildhall: 1 unread (note 1)"}; !slices.Equal(panes.lines, want) {
		t.Fatalf("typed = %q, want %q", panes.lines, want)
	}
	// A retried send prints the original receipt and wakes nothing new.
	if code, again, _ := send(); code != 0 || again != out {
		t.Fatalf("retry: exit %d, %s; want %s", code, again, out)
	}
	if len(panes.lines) != 1 {
		t.Fatalf("a retry typed %q", panes.lines[1:])
	}
	ws := readWakes(t, path)
	if len(ws) != 1 || ws[0].MessageID != "n-1" || ws[0].SenderType != messaging.SenderLocal || ws[0].Decision != store.WakeWoke {
		t.Fatalf("wakes = %+v", ws)
	}
}

func TestMessagesSendQueuesTheWakeWithoutASession(t *testing.T) {
	path := messagingStorePath(t)
	panes := &typedPanes{}
	app, _, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(envelope("n-1", "director"))
	app.DirectorPane = panes
	if code := app.Run([]string{"messages", "send", "--store", path, "director", "-"}); code != 0 {
		t.Fatalf("send: exit %d, stderr %s", code, stderr)
	}
	if ws := readWakes(t, path); len(ws) != 1 || ws[0].Decision != store.WakeQueued || ws[0].Reason != "no session" || len(panes.lines) != 0 {
		t.Fatalf("wakes = %+v, typed %q", ws, panes.lines)
	}
}

func TestMessagesSendToAnArtisanWakesNoDirector(t *testing.T) {
	path := messagingStorePath(t)
	startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	panes := &typedPanes{}
	app, _, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(envelope("n-1", "artisan"))
	app.DirectorPane = panes
	if code := app.Run([]string{"messages", "send", "--store", path, "artisan:app/t-1/build/1", "-"}); code != 0 {
		t.Fatalf("send: exit %d, stderr %s", code, stderr)
	}
	if ws := readWakes(t, path); len(ws) != 0 || len(panes.lines) != 0 {
		t.Fatalf("wakes = %+v, typed %q", ws, panes.lines)
	}
}
