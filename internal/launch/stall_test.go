package launch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// stallEvents returns the last_output_at of each worker-stalled event in a's events.jsonl, failing on a failed send.
func stallEvents(t *testing.T, a launch.Attempt) []time.Time {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.Dir(), artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []time.Time
	for line := range strings.Lines(string(b)) {
		var e struct {
			Event      string    `json:"event"`
			Message    string    `json:"message"`
			LastOutput time.Time `json:"last_output_at"`
			Error      string    `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Event != "worker-stalled" {
			continue
		}
		if e.Error != "" || e.Message != interp.StalledMessage(key, e.LastOutput).ID {
			t.Fatalf("worker-stalled event %s", line)
		}
		out = append(out, e.LastOutput)
	}
	return out
}

// speaker starts the speaker worker for a with the stall interval, its output appended to a regular file as
// launch.Process appends it to launcher.log, and returns its directory.
func speaker(t *testing.T, a launch.Attempt, interval time.Duration) (string, <-chan ran) {
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
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "done"), nil, 0o600)
		wait(t, done)
	})
	return dir, done
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
// message, interp.StalledMessage for its last output, however many beats follow; new output followed by another
// crossing sends one more; each send is a worker-stalled event.
func TestLauncherSendsOneStallPerCrossing(t *testing.T) {
	a := newAttempt(t)
	const interval = 400 * time.Millisecond
	dir, done := speaker(t, a, interval)
	first := waitStalls(t, a, 1, interval)
	if err := os.WriteFile(filepath.Join(dir, "speak-1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	both := waitStalls(t, a, 2, interval)
	if both[0].ID != first[0].ID || both[1].ID == first[0].ID {
		t.Fatalf("stall ids %s then %s, %s; want a new id for the new crossing", first[0].ID, both[0].ID, both[1].ID)
	}
	outputs := stallEvents(t, a)
	if len(outputs) != 2 || !outputs[1].After(outputs[0]) {
		t.Fatalf("worker-stalled events at outputs %v, want two, the second later", outputs)
	}
	for i, m := range both {
		want := interp.StalledMessage(key, outputs[i])
		if m.ID != want.ID || m.Type != want.Type || m.Text != want.Text || m.Repository != want.Repository || m.TaskID != want.Task {
			t.Errorf("stall %d = %+v, want %+v", i, m, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, done); r.err != nil || r.r.AttemptOutcome != store.AttemptFailed {
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
	if got := stallEvents(t, a); len(got) != 0 {
		t.Fatalf("worker-stalled events = %v, want none", got)
	}
}
