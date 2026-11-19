//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/testenv"
)

const (
	// hangStubEnv makes the test binary, run as scp or ssh, write "pid pgid" to the named file and then hang far longer
	// than any test ceiling, as a transfer or remote command that never finishes.
	hangStubEnv = "GUILDHALL_TEST_HANG_PIDFILE"
	// serveStubEnv, beside hangStubEnv, makes the scp stub answer a fetch, a remote source, from the named directory by
	// the remote path's base name; only puts and ssh commands hang.
	serveStubEnv = "GUILDHALL_TEST_SCP_SERVE"
)

// runStewardHangStub acts as the hanging scp or ssh when hangStubEnv is set; it never returns then.
func runStewardHangStub() {
	pidFile := os.Getenv(hangStubEnv)
	if pidFile == "" {
		return
	}
	args := os.Args[1:]
	if i := slices.Index(args, "--"); i >= 0 && strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "scp" && len(args) == i+3 {
		src, dst := args[i+1], args[i+2]
		if serve := os.Getenv(serveStubEnv); serve != "" && strings.Contains(src, ":") {
			_, remote, _ := strings.Cut(src, ":")
			data, err := os.ReadFile(filepath.Join(serve, filepath.Base(remote)))
			if err != nil {
				fmt.Fprintf(os.Stderr, "scp: %s: No such file or directory\n", remote)
				os.Exit(1)
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "stub scp:", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	pgid, err := syscall.Getpgid(0)
	if err == nil {
		tmp := pidFile + ".tmp"
		if err = os.WriteFile(tmp, fmt.Appendf(nil, "%d %d\n", os.Getpid(), pgid), 0o600); err == nil {
			err = os.Rename(tmp, pidFile)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		os.Exit(2)
	}
	time.Sleep(10 * time.Minute) // timing:allow deliberately hung scp or ssh subprocess; the test interrupts it and checks its group is gone.
	os.Exit(3)
}

// recordingInterrupt is an App.AdvanceContext that records the signals each command subscribes to and hands back a
// context the test cancels as an interrupt would.
type recordingInterrupt struct {
	mu      sync.Mutex
	signals [][]os.Signal
	cancel  context.CancelFunc
}

func (r *recordingInterrupt) notify(parent context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, sig)
	r.cancel = cancel
	return ctx, cancel
}

func (r *recordingInterrupt) interrupt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *recordingInterrupt) subscribed() [][]os.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.signals)
}

// interruptWhenHung runs run in a goroutine, interrupts once the stub's pid file appears and returns run's exit code
// and the stub's process group. It fails the test, killing the group, when run does not return within the ceiling.
func interruptWhenHung(t *testing.T, pidFile string, r *recordingInterrupt, run func() int) (code, pgid int) {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- run() }()
	deadline := time.After(testenv.Ceiling(20 * time.Second))
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for pgid == 0 {
		select {
		case code := <-done:
			t.Fatalf("the command returned %d before its child hung", code)
		case <-deadline:
			t.Fatal("the stub never hung")
		case <-tick.C:
			data, err := os.ReadFile(pidFile)
			if err != nil {
				continue
			}
			var pid int
			if _, err := fmt.Sscan(string(data), &pid, &pgid); err != nil {
				t.Fatalf("pid file %q: %v", data, err)
			}
		}
	}
	r.interrupt()
	select {
	case code = <-done:
		return code, pgid
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatal("the interrupted command did not return")
	}
	return 0, 0
}

// assertGroupGone checks that no process of the stub's group is left.
func assertGroupGone(t *testing.T, pgid int) {
	t.Helper()
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Errorf("the stub's process group %d is still alive after the command returned: %v", pgid, err)
	}
}

func assertInterruptSignals(t *testing.T, r *recordingInterrupt) {
	t.Helper()
	want := []os.Signal{os.Interrupt, syscall.SIGTERM}
	got := r.subscribed()
	if len(got) == 0 {
		t.Fatal("the command did not subscribe to interruption")
	}
	for _, sig := range got {
		if !slices.Equal(sig, want) {
			t.Errorf("subscribed to %v, want %v", sig, want)
		}
	}
}

// TestStewardRolloutInterruptStopsChildren: an interrupt during steward rollout --apply stops the hanging scp's
// whole process group before the command returns, the receipt records the copy failed and interrupted, and the
// private config lock is free at once.
func TestStewardRolloutInterruptStopsChildren(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is needed for the private config checkout: %v", err)
	}
	bin := t.TempDir()
	for _, name := range []string{"scp", "ssh"} {
		copyFile(t, self, filepath.Join(bin, name))
	}
	cfg, private := writeStewardConfig(t, "schema_version = 1\n[managed.standards_revision]\nvalue = \"standards-2\"\n")
	if out, err := exec.Command(git, "init", "-q", private).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	serve := t.TempDir()
	managed, err := config.EncodeManaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(serve, "config.toml"), cliCopy)
	writeFile(t, filepath.Join(serve, "managed.toml"), string(managed))
	pidFile := filepath.Join(t.TempDir(), "stub.pid")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(git))
	t.Setenv(hangStubEnv, pidFile)
	t.Setenv(serveStubEnv, serve)

	plan := filepath.Join(t.TempDir(), "plan")
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}
	r := &recordingInterrupt{}
	app := New()
	var stdout, stderr bytes.Buffer
	app.Stdout, app.Stderr = &stdout, &stderr
	app.AdvanceContext = r.notify
	if code := app.Run(append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry run exit %d; stderr: %s\n%s", code, &stderr, &stdout)
	}
	if !strings.Contains(stdout.String(), "workstation/tools (guild-ws:/home/guild/tools/managed.toml): change") {
		t.Fatalf("dry run planned no change:\n%s", &stdout)
	}

	stdout.Reset()
	stderr.Reset()
	code, pgid := interruptWhenHung(t, pidFile, r, func() int { return app.Run(append(args, "--apply")) })
	if code == 0 {
		t.Errorf("interrupted apply exit 0; stdout: %s", &stdout)
	}
	assertGroupGone(t, pgid)
	assertInterruptSignals(t, r)
	receipts, _ := filepath.Glob(filepath.Join(plan, "receipt-*.json"))
	if len(receipts) != 1 {
		t.Fatalf("receipts %q, want one; stderr: %s", receipts, &stderr)
	}
	data, err := os.ReadFile(receipts[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt steward.Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Copies) != 1 || receipt.Copies[0].Status != steward.StatusFailed || !strings.Contains(receipt.Copies[0].Error, "interrupted") {
		t.Errorf("receipt:\n%s", data)
	}
	release, err := steward.TakePrivateLock(private, steward.LockOwner{PID: os.Getpid(), Command: "test"})
	if err != nil {
		t.Fatalf("the private config lock is still held: %v", err)
	}
	release()
}

// hangOnceStepping is the takeover's Runner: the in-memory fleet until the apply has written its step log, and then
// a real ssh, the hanging stub, so the first step that runs a remote command hangs in a separate process group.
type hangOnceStepping struct {
	fleet   steward.Runner
	ssh     steward.SSH
	planDir string
}

func (r hangOnceStepping) Run(ctx context.Context, alias string, argv []string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(r.planDir, "steps.json")); err == nil {
		return r.ssh.Run(ctx, alias, argv)
	}
	return r.fleet.Run(ctx, alias, argv)
}

// TestStewardTakeoverInterruptStopsChildren: an interrupt during steward takeover --apply stops the step's hanging
// ssh before the command returns, and the step log records the running step failed and interrupted.
func TestStewardTakeoverInterruptStopsChildren(t *testing.T) {
	ts := newTakeoverSetup(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(t.TempDir(), "ssh")
	copyFile(t, self, ssh)
	pidFile := filepath.Join(t.TempDir(), "stub.pid")
	t.Setenv(hangStubEnv, pidFile)
	cfgPath := filepath.Join(ts.steward, "config.toml")
	planDir := filepath.Join(ts.steward, "rollouts", "takeover-alpha")
	code, out, stderr := ts.run(t, false, "", "steward", "takeover", "--config", cfgPath, "--machine", "workstation", "--director", "alpha", "--copy", ts.director, "--plan", planDir, "--dry-run")
	m := regexp.MustCompile(`^takeover plan (sha256:[0-9a-f]{64})\n`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		t.Fatalf("dry run: exit %d, %s%s", code, out, stderr)
	}

	r := &recordingInterrupt{}
	app, stdout, errOut := newTestApp(nil, nil)
	app.Copier = localCopier{}
	app.Runner = hangOnceStepping{fleet: ts.fleet, ssh: steward.SSH{Path: ssh, Timeout: time.Hour}, planDir: planDir}
	app.LookPath = exec.LookPath
	app.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
	app.Stdin = strings.NewReader("")
	app.AdvanceContext = r.notify
	code, pgid := interruptWhenHung(t, pidFile, r, func() int {
		return app.Run([]string{"steward", "takeover", "--config", cfgPath, "--plan", planDir, "--apply", "--approve", m[1]})
	})
	if code == 0 {
		t.Errorf("interrupted apply exit 0; stdout: %s", stdout)
	}
	assertGroupGone(t, pgid)
	assertInterruptSignals(t, r)
	log, err := steward.ReadStepLog(planDir)
	if err != nil {
		t.Fatal(err)
	}
	var failed []steward.StepRecord
	for _, rec := range log.Steps {
		if rec.State == steward.StepFailed {
			failed = append(failed, rec)
		}
	}
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "interrupted") {
		t.Errorf("step log %+v, want the running step failed and interrupted; stderr: %s", log.Steps, errOut)
	}
}

// TestInterruptContextFallsBackToNotify: with no AdvanceContext the helper subscribes to the real signals, and the
// first one cancels the context.
func TestInterruptContextFallsBackToNotify(t *testing.T) {
	ctx, stop := (&App{}).interruptContext(context.Background(), syscall.SIGUSR1)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("the signal did not cancel the context")
	}
}
