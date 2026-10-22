package launch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// speakerEnv makes the test binary a worker that writes one line to standard output when the file speak-<n> appears
// in the directory it names, n from 1, and exits when done appears there.
const speakerEnv = "GUILDHALL_LAUNCH_TEST_SPEAKER"

func mainIfSpeaker() {
	dir := os.Getenv(speakerEnv)
	if dir == "" {
		return
	}
	n := 1
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
			os.Exit(0)
		}
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("speak-%d", n))); err == nil {
			_, _ = fmt.Fprintf(os.Stdout, "spoke %d\n", n)
			n++
		}
	}
	os.Exit(1)
}

// storeOutbox sends through the store file it names, opened for each send, as the launcher's own outbox does.
type storeOutbox string

func (p storeOutbox) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	s, err := store.Open(string(p), store.Options{RequireStore: true})
	if err != nil {
		return store.Receipt{}, err
	}
	defer func() { _ = s.Close() }()
	return (&messaging.StoreMailbox{Store: s}).Send(to, e)
}

// stalls returns the worker-stalled messages in the Director's inbox.
func stalls(t *testing.T, a launch.Attempt) []store.Message {
	t.Helper()
	var out []store.Message
	withStore(t, a, func(s *store.Store) {
		msgs, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Type == interp.WorkerStalled {
				out = append(out, m)
			}
		}
	})
	return out
}

// stalledFor checks m is interp.StalledMessage for the last output a's heartbeat records.
func stalledFor(t *testing.T, a launch.Attempt, m store.Message) {
	t.Helper()
	hb, err := artisan.ReadHeartbeat(a.Dir())
	if err != nil {
		t.Fatal(err)
	}
	want := interp.StalledMessage(key, hb.LastOutputAt)
	if m.ID != want.ID || m.Type != want.Type || m.Text != want.Text || m.Repository != want.Repository || m.TaskID != want.Task {
		t.Fatalf("stall %+v, want the engine's %+v", m, want)
	}
}

// speaker starts the speaker worker for a with the stall interval, its output appended to a regular file as
// launch.Process appends it to launcher.log, and returns its directory and finish, which ends the worker and
// returns Run's result; the case's cleanup finishes it too.
func speaker(t *testing.T, a launch.Attempt, interval time.Duration) (string, func() ran) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.OpenFile(filepath.Join(t.TempDir(), "launcher.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := adapter.Command{Argv: []string{exe, "-test.run=^$"}, Env: []string{speakerEnv + "=" + dir}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan ran, 1)
	go func() {
		r, err := launch.Run(ctx, launch.Config{Attempt: a, Command: cmd, Stdout: out, Stderr: out, Ladder: fastLadder, ResultGrace: time.Second,
			HeartbeatInterval: 50 * time.Millisecond, StallInterval: interval, Outbox: storeOutbox(a.Store)})
		done <- ran{r, err}
	}()
	finish := sync.OnceValue(func() ran {
		if err := os.WriteFile(filepath.Join(dir, "done"), nil, 0o600); err != nil {
			t.Error(err)
		}
		return wait(t, done)
	})
	t.Cleanup(func() { finish() })
	return dir, finish
}

// waitStalls waits until a's inbox holds n worker-stalled messages, then checks it still holds n after a few
// more beats past the interval.
func waitStalls(t *testing.T, a launch.Attempt, n int, interval time.Duration) []store.Message {
	t.Helper()
	for end := time.Now().Add(deadline); len(stalls(t, a)) < n; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("worker-stalled messages = %d, want %d", len(stalls(t, a)), n)
		}
	}
	time.Sleep(interval + 250*time.Millisecond)
	got := stalls(t, a)
	if len(got) != n {
		t.Fatalf("worker-stalled messages = %d after more beats, want %d", len(got), n)
	}
	return got
}

// TestLauncherSendsOneStallPerCrossing: a worker silent past the stall interval gets exactly one worker-stalled
// message, interp.StalledMessage for the last output its heartbeat records, however many beats follow; new output
// followed by another crossing sends one more, for the new last output.
func TestLauncherSendsOneStallPerCrossing(t *testing.T) {
	a := newAttempt(t)
	const interval = 400 * time.Millisecond
	dir, finish := speaker(t, a, interval)
	first := waitStalls(t, a, 1, interval)
	stalledFor(t, a, first[0])
	if err := os.WriteFile(filepath.Join(dir, "speak-1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	both := waitStalls(t, a, 2, interval)
	if both[0].ID != first[0].ID || both[1].ID == first[0].ID {
		t.Fatalf("stall ids %s then %s, %s; want a new id for the new crossing", first[0].ID, both[0].ID, both[1].ID)
	}
	stalledFor(t, a, both[1])
	if r := finish(); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
		t.Fatalf("Run = %+v, %v", r.r, r.err)
	}
	if got := stalls(t, a); len(got) != 2 {
		t.Fatalf("worker-stalled messages = %d after the attempt ended, want 2", len(got))
	}
}

// TestLauncherWithoutStallIntervalSendsNone: a zero stall interval, `--stall-interval 0s`, observes nothing.
func TestLauncherWithoutStallIntervalSendsNone(t *testing.T) {
	a := newAttempt(t)
	speaker(t, a, 0)
	time.Sleep(time.Second)
	if got := stalls(t, a); len(got) != 0 {
		t.Fatalf("worker-stalled messages = %d, want 0", len(got))
	}
}
