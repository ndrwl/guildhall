package session_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/session"
)

// hookEnv makes the test binary play the runtime's end-of-turn hook: "<dir>|<number>" signals that session.
const hookEnv = "GUILDHALL_SESSION_TEST_HOOK"

func TestMain(m *testing.M) {
	if v := os.Getenv(hookEnv); v != "" {
		dir, n, _ := strings.Cut(v, "|")
		number, _ := strconv.Atoi(n)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := session.SignalTurnEnded(ctx, dir, number)
		cancel()
		switch {
		case errors.Is(err, session.ErrNoSupervisor):
			os.Exit(3)
		case errors.Is(err, session.ErrMismatch):
			os.Exit(4)
		case err != nil:
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// shortDir is a session directory short enough for an AF_UNIX path on every platform.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// hook runs the test binary as the end-of-turn hook of session number in dir and returns its exit status.
func hook(t *testing.T, dir string, number int) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(exe)
	c.Env = append(os.Environ(), hookEnv+"="+dir+"|"+strconv.Itoa(number))
	out, err := c.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() == 1 {
			t.Logf("hook: %s", out)
		}
		return exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func received(e *session.Endpoint) bool {
	select {
	case <-e.TurnEnded():
		return true
	default:
		return false
	}
}

func listen(t *testing.T, dir string, number int) *session.Endpoint {
	t.Helper()
	e, err := session.Listen(dir, number)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestEndpointPath(t *testing.T) {
	dir := shortDir(t)
	if p, err := session.EndpointPath(dir); err != nil || p != filepath.Join(dir, session.EndpointFile) {
		t.Fatalf("EndpointPath = %q, %v", p, err)
	}
	if _, err := session.EndpointPath("director/session-1"); !errors.Is(err, session.ErrPath) {
		t.Errorf("relative directory: err = %v, want ErrPath", err)
	}
	long := filepath.Join(dir, strings.Repeat("d", artisan.MaxEndpointPath))
	if _, err := session.EndpointPath(long); !errors.Is(err, session.ErrPath) {
		t.Errorf("long directory: err = %v, want ErrPath", err)
	}
	if _, err := session.Listen(long, 1); !errors.Is(err, session.ErrPath) {
		t.Errorf("Listen in a long directory: err = %v, want ErrPath", err)
	}
}

// TestHookSignalsTheSupervisor crosses a real process boundary: a child process plays the hook.
func TestHookSignalsTheSupervisor(t *testing.T) {
	dir := shortDir(t)
	e := listen(t, dir, 3)
	if e.Path() != filepath.Join(dir, session.EndpointFile) {
		t.Fatalf("Path = %q", e.Path())
	}
	if received(e) {
		t.Fatal("a turn ended before any hook ran")
	}
	if code := hook(t, dir, 3); code != 0 {
		t.Fatalf("hook exited %d, want 0", code)
	}
	if !received(e) {
		t.Fatal("the answered hook's turn end was not received")
	}
	if code := hook(t, dir, 4); code != 4 {
		t.Fatalf("hook for another session exited %d, want ErrMismatch's 4", code)
	}
	if received(e) {
		t.Fatal("a refused signal was received")
	}
	for range 2 {
		if code := hook(t, dir, 3); code != 0 {
			t.Fatalf("hook exited %d", code)
		}
	}
	if !received(e) || received(e) {
		t.Fatal("two unread turn ends did not coalesce into one")
	}
}

func TestSignalWithNoSupervisor(t *testing.T) {
	dir := shortDir(t)
	if code := hook(t, dir, 1); code != 3 {
		t.Fatalf("hook with no endpoint exited %d, want ErrNoSupervisor's 3", code)
	}
	e, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := os.Lstat(e.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file after Close: %v", err)
	}
	if code := hook(t, dir, 1); code != 3 {
		t.Fatalf("hook after Close exited %d, want 3", code)
	}
}

func TestListenOverLeftovers(t *testing.T) {
	dir := shortDir(t)
	p := filepath.Join(dir, session.EndpointFile)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	e := listen(t, dir, 1)
	if code := hook(t, dir, 1); code != 0 || !received(e) {
		t.Fatalf("over a stale socket: hook exited %d", code)
	}
	if _, err := session.Listen(dir, 2); err == nil {
		t.Fatal("Listen over a live endpoint succeeded")
	}
	if code := hook(t, dir, 1); code != 0 {
		t.Fatalf("the live endpoint stopped answering after a refused Listen: exit %d", code)
	}

	other := shortDir(t)
	file := filepath.Join(other, session.EndpointFile)
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Listen(other, 1); err == nil {
		t.Fatal("Listen over a regular file succeeded")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "x" {
		t.Fatalf("the regular file was not kept: %q, %v", b, err)
	}
}

func TestMalformedRequestIsRefused(t *testing.T) {
	dir := shortDir(t)
	e := listen(t, dir, 1)
	for _, line := range []string{
		"not json",
		`{"op":"turn-ended","session":1}`,
		`{"schema":"guildhall.session-control/2","op":"turn-ended","session":1}`,
		`{"schema":"guildhall.session-control/1","op":"turn-ended","session":1,"extra":true}`,
		`{"schema":"guildhall.session-control/1","op":"stop","session":1}`,
	} {
		c, err := net.Dial("unix", e.Path())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(time.Minute))
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		reply, err := bufio.NewReader(c).ReadString('\n')
		_ = c.Close()
		if err != nil || !strings.Contains(reply, `"error"`) {
			t.Errorf("request %s: reply %q, %v; want an error reply", line, reply, err)
		}
	}
	if received(e) {
		t.Fatal("a malformed request was received as a turn end")
	}
}
