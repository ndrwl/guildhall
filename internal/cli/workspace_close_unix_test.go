//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// lockedBuffer is a bytes.Buffer safe for the supervisor's goroutines and the test to share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sweptCopy is a supervised copy whose config names a Herdr session and a 1ns close delay, with app/t-9's plan
// attempt finished under an exited launcher and presented in Herdr workspace w2, and the host modelling it, whose
// workspace listing waits until its release is closed.
func sweptCopy(t *testing.T) (string, runDirector, artisan.AttemptKey, *closeHost) {
	t.Helper()
	dir, d := supervisedCopy(t)
	writeFile(t, d.config, strings.Replace(readFileString(t, d.config), "[local]\n",
		"[local]\nherdr_session = \""+sweepSession+"\"\nartisan_workspace_close_after = \"1ns\"\n", 1))
	killed := exitedLauncher(t)
	_, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		digest := "sha256:" + strings.Repeat("a", 64)
		if _, err := s.CreateTask("sweep-1", store.Task{Repository: "app", ID: "t-9", Title: "Swept", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.StartOwnedAttempt("sweep-2", store.Ownership{Repository: "app", TaskID: "t-9", Step: "plan", LauncherIdentity: killed,
			Containment: store.Containment{Kind: store.ContainmentNotStarted}}); err != nil {
			return store.Receipt{}, err
		}
		return s.FinishAttempt("sweep-3", "app", "t-9", "plan", 1, store.AttemptSucceeded)
	})
	if err != nil {
		t.Fatal(err)
	}
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-9", Step: "plan", Attempt: 1}
	f := &sweepFixture{t: t, state: d.state, host: &closeHost{session: sweepSession, entered: make(chan struct{}), release: make(chan struct{})}}
	f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
	return dir, d, k, f.host
}

func marked(state string, k artisan.AttemptKey) bool {
	_, err := os.Stat(filepath.Join(k.Dir(state), paneClosed))
	return err == nil
}

// TestTheSupervisorSweepsWhenTheSessionStarts: a session whose supervisor would not tick for an hour still closes the
// Herdr workspace of an attempt that became due while no supervisor ran, so a restart cleans up.
func TestTheSupervisorSweepsWhenTheSessionStarts(t *testing.T) {
	dir, d, k, host := sweptCopy(t)
	held := make(chan os.Signal, 1)
	signal.Notify(held, syscall.SIGTERM)
	defer signal.Stop(held)
	opened := make(chan string, 1)
	done := superviseWith(t, dir, time.Hour, func(app *App) {
		app.SessionPoll = time.Hour
		app.ArtisanWorkspaces = func(_ context.Context, session string) (WorkspaceCloseHost, error) {
			opened <- session
			return host, nil
		}
	})
	select {
	case <-host.entered:
	case <-time.After(testenv.Ceiling(time.Minute)):
		t.Fatal("no sweep ran when the session started")
	}
	if got := <-opened; got != sweepSession {
		t.Errorf("opened Herdr session %q, want %q", got, sweepSession)
	}
	close(host.release)
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); !marked(d.state, k); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the start sweep did not close the workspace: calls %q", host.calls)
		}
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	ended(t, done)
	if host.closed[0] != "workspace w2" || len(host.closed) != 1 {
		t.Errorf("closed %q, want workspace w2", host.closed)
	}
}

// TestAStuckSweepDelaysNoStop: while a sweep waits on Herdr, director stop --now still ends the runtime at the next
// poll; the supervisor exits once the sweep ends.
func TestAStuckSweepDelaysNoStop(t *testing.T) {
	dir, d, k, host := sweptCopy(t)
	out := &lockedBuffer{}
	done := superviseWith(t, dir, time.Hour, func(app *App) {
		app.Stdout = out
		app.ArtisanWorkspaces = func(context.Context, string) (WorkspaceCloseHost, error) { return host, nil }
	})
	select {
	case <-host.entered:
	case <-time.After(testenv.Ceiling(time.Minute)):
		t.Fatal("no sweep ran when the session started")
	}
	if code, stdout, stderr := d.run(t, "director", "stop", "--now", "--copy", dir); code != 0 {
		t.Fatalf("director stop --now: exit %d, %s %s", code, stdout, stderr)
	}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); !strings.Contains(out.String(), "stopped by director stop"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("a stuck sweep delayed the stop: stdout %q", out.String())
		}
	}
	select {
	case code := <-done:
		t.Fatalf("the supervisor exited %d with its sweep still running", code)
	default:
	}
	close(host.release)
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if !marked(d.state, k) {
		t.Error("the released sweep did not finish")
	}
}
