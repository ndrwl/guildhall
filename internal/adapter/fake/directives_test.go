package fake

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// deadline bounds every wait for a fake process.
const deadline = 10 * time.Second

func TestP0DirectivesAreValidated(t *testing.T) {
	inv := t.TempDir()
	for _, prompt := range []string{
		"guildhall-fake: grandchildren=2\nguildhall-fake: grandchild-cmd=native\nguildhall-fake: lifetime=1ms\n",
		"guildhall-fake: grandchild-cmd=fake\nguildhall-fake: lifetime=1ms\n",
		"guildhall-fake: ignore-cancel=true\nguildhall-fake: ignore-interrupt=false\n",
		"guildhall-fake: interactive=true\nguildhall-fake: result=never\nguildhall-fake: lifetime=1ms\n",
		"guildhall-fake: result=after-cancel\nguildhall-fake: lifetime=1h\n",
		"guildhall-fake: result=delay\nguildhall-fake: inventory=" + inv + "\n",
	} {
		args := setup(t, prompt)
		if _, err := run(t, args); err != nil {
			t.Errorf("prompt %q refused: %v", prompt, err)
		}
	}
	for _, prompt := range []string{
		"guildhall-fake: grandchildren=9\n",
		"guildhall-fake: grandchildren=-1\n",
		"guildhall-fake: grandchildren=two\n",
		"guildhall-fake: grandchild-cmd=bash\n",
		"guildhall-fake: ignore-cancel=maybe\n",
		"guildhall-fake: ignore-interrupt=yes\n",
		"guildhall-fake: interactive=1\n",
		"guildhall-fake: result=sometimes\n",
		"guildhall-fake: lifetime=0s\n",
		"guildhall-fake: lifetime=2h\n",
		"guildhall-fake: inventory=relative/dir\n",
	} {
		args := setup(t, prompt)
		if _, err := run(t, args); err == nil {
			t.Errorf("prompt %q accepted", prompt)
		}
		if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("prompt %q wrote a result", prompt)
		}
	}
}

// running is a fake Run in the background.
type running struct {
	done chan struct{}
	err  error
}

// runReal runs the fake in this process with real sleeping, in the
// background; cleanup marks the attempt cancelled and waits for it.
func runReal(t *testing.T, args Args) *running {
	t.Helper()
	r := &running{done: make(chan struct{})}
	go func() { r.err = Run(context.Background(), args, io.Discard, Sleep); close(r.done) }()
	t.Cleanup(func() {
		_ = adapter.MarkCancelled(args.ResultFile, args.Identity)
		select {
		case <-r.done:
		case <-time.After(deadline):
			t.Error("fake did not return after the case")
		}
	})
	return r
}

func returned(t *testing.T, r *running) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(deadline):
		t.Fatalf("fake did not return within %v", deadline)
		return nil
	}
}

func TestRunHonoursTheCancelSidecar(t *testing.T) {
	args := setup(t, "guildhall-fake: delay=10m\n")
	done := runReal(t, args)
	time.Sleep(100 * time.Millisecond)
	if err := adapter.MarkCancelled(args.ResultFile, args.Identity); err != nil {
		t.Fatal(err)
	}
	if err := returned(t, done); err != nil {
		t.Errorf("Run after the marker = %v, want nil", err)
	}
	if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cancelled fake wrote a result: %v", err)
	}
}

func TestRunIgnoreCancelKeepsWorking(t *testing.T) {
	args := setup(t, "guildhall-fake: delay=300ms\nguildhall-fake: ignore-cancel=true\n")
	if err := adapter.MarkCancelled(args.ResultFile, args.Identity); err != nil {
		t.Fatal(err)
	}
	begun := time.Now()
	err := returned(t, runReal(t, args))
	if d := time.Since(begun); d < 300*time.Millisecond {
		t.Errorf("an ignoring fake returned after %v, before its delay", d)
	}
	// The adapter refuses to publish a result once the attempt is cancelled.
	if !errors.Is(err, adapter.ErrAttemptCancelled) {
		t.Errorf("Run = %v, want the adapter's refusal ErrAttemptCancelled", err)
	}
}

func TestRunResultAfterCancelPublishesALateResult(t *testing.T) {
	args := setup(t, "guildhall-fake: result=after-cancel\n")
	done := runReal(t, args)
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("result published before the marker: %v", err)
	}
	if err := adapter.MarkCancelled(args.ResultFile, args.Identity); err != nil {
		t.Fatal(err)
	}
	if err := returned(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}
	b, err := os.ReadFile(args.ResultFile)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := adapter.ParseResult(b, args.Identity); err != nil || r.Status != adapter.StatusSucceeded {
		t.Errorf("late result %+v, %v; want a valid succeeded result", r, err)
	}
}

func TestRunResultNeverEndsAtTheLifetime(t *testing.T) {
	args := setup(t, "guildhall-fake: result=never\nguildhall-fake: lifetime=300ms\n")
	begun := time.Now()
	if err := returned(t, runReal(t, args)); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if d := time.Since(begun); d < 300*time.Millisecond {
		t.Errorf("returned after %v, before its lifetime", d)
	}
	if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("result=never wrote a result: %v", err)
	}
}

func TestRunRecordsItsIdentity(t *testing.T) {
	inv := t.TempDir()
	args := setup(t, "guildhall-fake: inventory="+inv+"\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := artisantest.Inventory(inv)
	if err != nil || len(entries) != 1 || entries[0].Role != "worker" || entries[0].Identity != self {
		t.Errorf("inventory %+v, %v; want the worker %v", entries, err, self)
	}
}

// process runs the test binary as `guildhall fake-artisan` for args.
type process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Scanner
	done  chan struct{}
}

func startFake(t *testing.T, args Args) *process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := Adapter{Executable: exe}.Command(adapter.Spec{Identity: args.Identity, WorkDir: filepath.Dir(args.PromptFile), PromptFile: args.PromptFile, ResultFile: args.ResultFile})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	p := &process{cmd: c, done: make(chan struct{})}
	if p.stdin, err = c.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.out = bufio.NewScanner(stdout)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		// Drain what nobody reads, so the fake never blocks on output.
		for p.out.Scan() {
		}
	}()
	go func() { _ = c.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = adapter.MarkCancelled(args.ResultFile, args.Identity)
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(deadline):
			_ = c.Process.Kill()
			t.Error("fake process did not exit after the case")
		}
	})
	return p
}

// recorded waits for n inventory entries of role in inv.
func recorded(t *testing.T, inv, role string, n int) []artisan.Identity {
	t.Helper()
	var ids []artisan.Identity
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		entries, err := artisantest.Inventory(inv)
		if err != nil {
			t.Fatal(err)
		}
		ids = ids[:0]
		for _, e := range entries {
			if e.Role == role {
				ids = append(ids, e.Identity)
			}
		}
		if len(ids) >= n {
			return ids
		}
	}
	t.Fatalf("waited %v for %d %s entries, have %v", deadline, n, role, ids)
	return nil
}

func probeAll(t *testing.T, ids []artisan.Identity, want artisan.Liveness) {
	t.Helper()
	for _, id := range ids {
		lv, err := artisan.Probe(id)
		for end := time.Now().Add(deadline); lv != want && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			lv, err = artisan.Probe(id)
		}
		if lv != want {
			t.Errorf("%v is %v (%v), want %v", id, lv, err, want)
		}
	}
}

func TestGrandchildrenRecordAndHonourTheMarker(t *testing.T) {
	inv := t.TempDir()
	args := setup(t, "guildhall-fake: grandchildren=2\nguildhall-fake: result=never\nguildhall-fake: lifetime=30s\nguildhall-fake: inventory="+inv+"\n")
	p := startFake(t, args)
	worker := recorded(t, inv, "worker", 1)
	if worker[0].PID != p.cmd.Process.Pid {
		t.Errorf("worker recorded %v, started pid %d", worker[0], p.cmd.Process.Pid)
	}
	grandchildren := recorded(t, inv, "grandchild", 2)
	probeAll(t, grandchildren, artisan.Alive)
	if err := adapter.MarkCancelled(args.ResultFile, args.Identity); err != nil {
		t.Fatal(err)
	}
	probeAll(t, append(worker, grandchildren...), artisan.Gone)
}

func TestNativeGrandchildrenOutliveTheMarker(t *testing.T) {
	inv := t.TempDir()
	args := setup(t, "guildhall-fake: grandchildren=1\nguildhall-fake: grandchild-cmd=native\nguildhall-fake: result=never\nguildhall-fake: lifetime=20s\nguildhall-fake: inventory="+inv+"\n")
	p := startFake(t, args)
	native := recorded(t, inv, "grandchild", 1)
	t.Cleanup(func() {
		for _, id := range native {
			_ = artisan.Kill(id)
		}
	})
	if err := adapter.MarkCancelled(args.ResultFile, args.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(deadline):
		t.Fatal("worker did not exit at the marker")
	}
	probeAll(t, native, artisan.Alive)
	for _, id := range native {
		if err := artisan.Kill(id); err != nil {
			t.Fatal(err)
		}
	}
	probeAll(t, native, artisan.Gone)
}

func TestInteractiveEchoesInput(t *testing.T) {
	args := setup(t, "guildhall-fake: interactive=true\nguildhall-fake: result=never\nguildhall-fake: lifetime=20s\n")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, Subcommand, "--task", args.TaskID, "--attempt", args.AttemptID, "--prompt", args.PromptFile, "--result", args.ResultFile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = adapter.MarkCancelled(args.ResultFile, args.Identity)
		<-done
	})
	if _, err := io.WriteString(stdin, "hello pane\n"); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(deadline); !strings.Contains(out.String(), "echo: hello pane"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("output %q does not echo the input", out.String())
		}
	}
}

// syncBuffer is a bytes.Buffer safe for a writer and a reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
