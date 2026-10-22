package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// Client.run is the client's process boundary: it runs argv with exactly env
// and returns stdout and stderr separately, the exit code, and an error only
// when the program could not be run or the context ended. testRunner is that
// contract; it runs against execRun with the test binary as the program and
// against the fakeRunner double the client tests use, so a client test that
// passes against the double means the same against the real transport.

// program is what the program under test does: write stdout and stderr, then
// exit with exit, or hang until stopped; echo replaces stdout with the
// arguments and selected environment the program received.
type program struct {
	stdout, stderr string
	exit           int
	hang           bool
	echo           []string
	missing        bool
}

// echoed is what an echoing program prints.
type echoed struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

// runnerUnderTest is one implementation of the boundary.
type runnerUnderTest struct {
	run func(ctx context.Context, argv, env []string) ([]byte, []byte, int, error)
	// install returns the argv prefix that runs p; arguments after it are the
	// program's own.
	install func(t *testing.T, name string, p program) []string
	// started reports whether a hanging program has started.
	started func() bool
}

func testRunner(t *testing.T, newRunner func(t *testing.T) runnerUnderTest) {
	t.Run("argv and env reach the program exactly", func(t *testing.T) {
		r := newRunner(t)
		t.Setenv("GUILDHALL_RUNNER_PARENT_ONLY", "leaked")
		argv := append(r.install(t, "echo", program{echo: []string{"GUILDHALL_RUNNER_GIVEN", "GUILDHALL_RUNNER_PARENT_ONLY"}}),
			"tab", "create", "--label", "two words", "", `it's "quoted"`)
		env := append(minimalEnv(), "GUILDHALL_RUNNER_GIVEN=given value")
		stdout, stderr, exit, err := r.run(context.Background(), argv, env)
		if err != nil || exit != 0 {
			t.Fatalf("run = exit %d, %v; stderr %q", exit, err, stderr)
		}
		var got echoed
		if err := json.Unmarshal(stdout, &got); err != nil {
			t.Fatalf("echo output %q: %v", stdout, err)
		}
		if want := []string{"tab", "create", "--label", "two words", "", `it's "quoted"`}; !slices.Equal(got.Args, want) {
			t.Errorf("args = %q, want %q", got.Args, want)
		}
		if got.Env["GUILDHALL_RUNNER_GIVEN"] != "given value" {
			t.Errorf("given env = %q, want %q", got.Env["GUILDHALL_RUNNER_GIVEN"], "given value")
		}
		if v, ok := got.Env["GUILDHALL_RUNNER_PARENT_ONLY"]; ok {
			t.Errorf("parent-only variable reached the program: %q", v)
		}
	})
	t.Run("stdout and stderr stay separate", func(t *testing.T) {
		r := newRunner(t)
		argv := r.install(t, "streams", program{stdout: "on stdout\n", stderr: "on stderr\n"})
		stdout, stderr, exit, err := r.run(context.Background(), argv, minimalEnv())
		if err != nil || exit != 0 {
			t.Fatalf("run = exit %d, %v", exit, err)
		}
		if string(stdout) != "on stdout\n" || string(stderr) != "on stderr\n" {
			t.Errorf("stdout %q stderr %q", stdout, stderr)
		}
	})
	t.Run("nonzero exit is reported with its output, not as an error", func(t *testing.T) {
		r := newRunner(t)
		argv := r.install(t, "fail", program{stdout: "partial\n", stderr: `{"error":{"code":"x","message":"y"}}`, exit: 1})
		stdout, stderr, exit, err := r.run(context.Background(), argv, minimalEnv())
		if err != nil {
			t.Fatalf("run error = %v, want nil with an exit code", err)
		}
		if exit != 1 || string(stdout) != "partial\n" || string(stderr) != `{"error":{"code":"x","message":"y"}}` {
			t.Errorf("exit %d stdout %q stderr %q", exit, stdout, stderr)
		}
	})
	t.Run("a program that cannot start is an error", func(t *testing.T) {
		r := newRunner(t)
		argv := r.install(t, "missing", program{missing: true})
		if _, _, _, err := r.run(context.Background(), argv, minimalEnv()); err == nil {
			t.Error("run of a missing program succeeded")
		}
	})
	t.Run("cancellation stops a hung program and reports the context error", func(t *testing.T) {
		r := newRunner(t)
		argv := r.install(t, "hang", program{hang: true})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type ret struct {
			exit int
			err  error
		}
		done := make(chan ret, 1)
		go func() {
			_, _, exit, err := r.run(ctx, argv, minimalEnv())
			done <- ret{exit, err}
		}()
		deadline := time.Now().Add(testenv.Ceiling(30 * time.Second))
		for !r.started() {
			if time.Now().After(deadline) {
				t.Fatalf("hung program did not start within %v", testenv.Ceiling(30*time.Second))
			}
			select {
			case got := <-done:
				t.Fatalf("run returned before cancellation: exit %d, %v", got.exit, got.err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		select {
		case got := <-done:
			if !errors.Is(got.err, context.Canceled) {
				t.Errorf("run after cancellation = exit %d, %v; want an error matching context.Canceled", got.exit, got.err)
			}
		case <-time.After(testenv.Ceiling(10 * time.Second)):
			t.Fatalf("run did not return within %v of cancellation", testenv.Ceiling(10*time.Second))
		}
	})
}

// minimalEnv is the environment a program needs to start on every platform.
func minimalEnv() []string {
	var env []string
	for _, name := range []string{"PATH", "SYSTEMROOT", "TMPDIR", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

func TestExecRunnerContract(t *testing.T) {
	testRunner(t, func(t *testing.T) runnerUnderTest {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		startedFile := t.TempDir() + string(os.PathSeparator) + "started"
		return runnerUnderTest{
			run: execRun,
			install: func(t *testing.T, name string, p program) []string {
				if p.missing {
					return []string{self + "-missing-" + name}
				}
				spec, err := json.Marshal(map[string]any{"Stdout": p.stdout, "Stderr": p.stderr, "Exit": p.exit, "Hang": p.hang, "Echo": p.echo})
				if err != nil {
					t.Fatal(err)
				}
				// The probe's role and script travel as arguments, since the
				// runner must not leak the parent's environment to it.
				return []string{self, "-test.run=^TestRunnerProbeHelper$", "--", "runner-probe", string(spec), startedFile}
			},
			started: func() bool {
				_, err := os.Stat(startedFile)
				return err == nil
			},
		}
	})
}

func TestFakeRunnerContract(t *testing.T) {
	testRunner(t, func(t *testing.T) runnerUnderTest {
		f := &fakeRunner{t: t, responses: map[string]response{}}
		return runnerUnderTest{
			run: f.run,
			install: func(t *testing.T, name string, p program) []string {
				f.responses["probe "+name] = response{stdout: p.stdout, stderr: p.stderr, exit: p.exit, hang: p.hang, echo: p.echo, startErr: p.missing}
				return []string{"herdr", "probe", name}
			},
			started: f.hanging,
		}
	})
}

// TestRunnerProbeHelper is not a test: it is the program TestExecRunnerContract runs.
func TestRunnerProbeHelper(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) < at+4 || os.Args[at+1] != "runner-probe" {
		t.Skip("program for TestExecRunnerContract")
	}
	var p struct {
		Stdout, Stderr string
		Exit           int
		Hang           bool
		Echo           []string
	}
	if err := json.Unmarshal([]byte(os.Args[at+2]), &p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if p.Echo != nil {
		got := echoed{Args: os.Args[at+4:], Env: map[string]string{}}
		for _, name := range p.Echo {
			if v, ok := os.LookupEnv(name); ok {
				got.Env[name] = v
			}
		}
		b, _ := json.Marshal(got)
		_, _ = os.Stdout.Write(b)
		os.Exit(0)
	}
	_, _ = os.Stdout.WriteString(p.Stdout)
	_, _ = os.Stderr.WriteString(p.Stderr)
	if p.Hang {
		if err := os.WriteFile(os.Args[at+3], nil, 0o600); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Hour)
	}
	os.Exit(p.Exit)
}

// echoJSON is what the fake prints for an echoing response.
func echoJSON(argv, env, names []string) string {
	got := echoed{Args: argv, Env: map[string]string{}}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if slices.Contains(names, name) {
			got.Env[name] = value
		}
	}
	b, _ := json.Marshal(got)
	return string(b)
}
