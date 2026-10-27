package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// waiterCopy is a copy directory short enough for its session endpoint's socket path, holding a store with Claude
// Code session 1 running, and session 1's endpoint listening.
func waiterCopy(t *testing.T) (string, *session.Endpoint) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	startRuntimeSession(t, filepath.Join(dir, "director.store"), store.DirectorPane{}, "claude")
	ep, err := session.Listen(sessionDir(dir, 1), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	return dir, ep
}

type waitResult struct {
	code   int
	stderr string
}

// runWaiter runs `director wait-wake` as session 1's hook, in the background.
func runWaiter(copyDir string, args ...string) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		app := &App{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`),
			Getenv: func(k string) string {
				return map[string]string{"GUILDHALL_COPY": copyDir, session.NumberEnv: "1"}[k]
			}}
		code := app.Run(append([]string{"director", "wait-wake"}, args...))
		out <- waitResult{code, stderr.String()}
	}()
	return out
}

func waitRegistered(t *testing.T, ep *session.Endpoint) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ep.Waiter().Registered; time.Sleep(5 * time.Millisecond) { // timing:allow polling the waiter registration; the deadline bounds it.
		if time.Now().After(end) {
			t.Fatal("the waiter never registered")
		}
	}
}

// TestWaitWakeExitsTwoWithTheHandedLine: the hook registers with its session's supervisor and, handed a wake,
// prints the counts line on standard error and exits 2, Claude Code's rewake.
func TestWaitWakeExitsTwoWithTheHandedLine(t *testing.T) {
	copyDir, ep := waiterCopy(t)
	got := runWaiter(copyDir)
	waitRegistered(t, ep)
	if err := session.Wake(context.Background(), sessionDir(copyDir, 1), 1, "guildhall: 2 unread (question 2)"); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.code != 2 || r.stderr != "guildhall: 2 unread (question 2)\n" {
		t.Fatalf("wait-wake = %+v, want exit 2 with the line", r)
	}
}

// TestWaitWakeIsRetiredSilently: a newer waiter of the session retires the older one, which exits 0 printing
// nothing, so it wakes nobody.
func TestWaitWakeIsRetiredSilently(t *testing.T) {
	copyDir, ep := waiterCopy(t)
	older := runWaiter(copyDir)
	waitRegistered(t, ep)
	newer := runWaiter(copyDir)
	if r := <-older; r.code != 0 || r.stderr != "" {
		t.Fatalf("the retired waiter = %+v, want exit 0 and nothing printed", r)
	}
	if err := session.Wake(context.Background(), sessionDir(copyDir, 1), 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-newer; r.code != 2 {
		t.Fatalf("the newer waiter = %+v", r)
	}
}

// TestWaitWakeRefreshesBeforeItsTimeout: a waiter still registered when its refresh comes tells the supervisor,
// which counts it as a wake, and exits 2 with the counts-only line, so the turn it starts arms a new waiter.
func TestWaitWakeRefreshesBeforeItsTimeout(t *testing.T) {
	copyDir, ep := waiterCopy(t)
	r := <-runWaiter(copyDir, "--refresh-after", "300ms")
	if r.code != 2 || r.stderr != "guildhall: 0 unread\n" {
		t.Fatalf("refresh = %+v, want exit 2 with the counts line", r)
	}
	if st := ep.Waiter(); st.Refreshed != 1 {
		t.Fatalf("waiter state = %+v, want the refresh counted", st)
	}
}

// TestWaitWakeOutlivesASupervisorRestart: with no supervisor listening the waiter keeps polling instead of exiting,
// and is woken once a supervisor listens again; a session recorded stopped ends it with exit 0.
func TestWaitWakeOutlivesASupervisorRestart(t *testing.T) {
	old := waitWakeRetry
	waitWakeRetry = 20 * time.Millisecond
	t.Cleanup(func() { waitWakeRetry = old })
	copyDir, ep := waiterCopy(t)
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	got := runWaiter(copyDir)
	select {
	case r := <-got:
		t.Fatalf("the waiter exited with no supervisor: %+v", r)
	case <-time.After(300 * time.Millisecond): // timing:allow proving the waiter keeps polling while nothing listens.
	}
	ep, err := session.Listen(sessionDir(copyDir, 1), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	waitRegistered(t, ep)
	if err := session.Wake(context.Background(), sessionDir(copyDir, 1), 1, "guildhall: 1 unread (note 1)"); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.code != 2 {
		t.Fatalf("after the restart = %+v", r)
	}

	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyDir, "director.store")
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		d, err := s.LatestDirectorSession()
		if err != nil {
			return store.Receipt{}, err
		}
		return s.SetDirectorState(1, store.DirectorStopping, d.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		d, _ := s.LatestDirectorSession()
		return s.SetDirectorState(1, store.DirectorStopped, d.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	if r := <-runWaiter(copyDir); r.code != 0 || r.stderr != "" {
		t.Fatalf("a waiter of a stopped session = %+v, want exit 0", r)
	}
}
