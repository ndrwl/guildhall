package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// App.LookPath and App.Output are doctor's process boundary.
// testToolBoundary is the behaviour doctor relies on from them; it runs against
// both the real boundary New wires and the fakeTools double the other tests use,
// so a test passing against the double means the same against the real one.

// probe is what a tool the boundary runs does: print stdout then stderr, then
// exit with exit, or hang until it is stopped.
type probe struct {
	stdout, stderr string
	exit           int
	hang           bool
}

// toolBoundary is one implementation of the boundary under test.
type toolBoundary struct {
	app *App
	// install makes name resolvable on the boundary's PATH, running p, and
	// returns the arguments to pass Output for it.
	install func(t *testing.T, name string, p probe) []string
	ready   func() (bool, error)
}

func testToolBoundary(t *testing.T, newBoundary func(t *testing.T) toolBoundary) {
	t.Run("missing tool is ErrNotFound", func(t *testing.T) {
		b := newBoundary(t)
		if _, err := b.app.LookPath("guildhall-no-such-tool"); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("LookPath error = %v, want exec.ErrNotFound", err)
		}
	})
	t.Run("output combines stdout and stderr", func(t *testing.T) {
		b := newBoundary(t)
		args := b.install(t, "probe-ok", probe{stdout: "tool 1.0\n", stderr: "on stderr\n"})
		path, err := b.app.LookPath("probe-ok")
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		out, err := b.app.Output(context.Background(), path, args...)
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		if got, want := string(out), "tool 1.0\non stderr\n"; got != want {
			t.Errorf("output = %q, want %q", got, want)
		}
	})
	t.Run("nonzero exit is an error with output", func(t *testing.T) {
		b := newBoundary(t)
		args := b.install(t, "probe-fail", probe{stderr: "boom\n", exit: 3})
		path, err := b.app.LookPath("probe-fail")
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		out, err := b.app.Output(context.Background(), path, args...)
		if err == nil {
			t.Fatal("Output error = nil, want the exit status")
		}
		if got, want := string(out), "boom\n"; got != want {
			t.Errorf("output = %q, want %q", got, want)
		}
	})
	t.Run("context cancellation stops a hung tool", func(t *testing.T) {
		b := newBoundary(t)
		args := b.install(t, "probe-hang", probe{hang: true})
		path, err := b.app.LookPath("probe-hang")
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct {
			out    []byte
			err    error
			ctxErr error
		}, 1)
		go func() {
			out, err := b.app.Output(ctx, path, args...)
			done <- struct {
				out    []byte
				err    error
				ctxErr error
			}{out, err, ctx.Err()}
		}()
		startup := time.NewTimer(testenv.Ceiling(30 * time.Second))
		defer startup.Stop()
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for {
			ready, err := b.ready()
			if err != nil {
				t.Fatalf("probe readiness: %v", err)
			}
			if ready {
				break
			}
			select {
			case got := <-done:
				t.Fatalf("Output returned before readiness: output %q, error %v", got.out, got.err)
			case <-startup.C:
				t.Fatalf("probe did not become ready within %v", testenv.Ceiling(30*time.Second))
			case <-poll.C:
			}
		}
		cancel()
		select {
		case got := <-done:
			if got.err == nil {
				t.Error("Output error = nil after cancellation, want an error")
			}
			if string(got.out) != probeReady {
				t.Errorf("output = %q, want hang readiness signal %q", got.out, probeReady)
			}
			if !errors.Is(got.ctxErr, context.Canceled) {
				t.Errorf("context error at Output return = %v, want Canceled", got.ctxErr)
			}
		case <-time.After(testenv.Ceiling(5 * time.Second)):
			t.Fatal("Output did not return promptly after cancellation")
		}
	})
}

func TestRealToolBoundary(t *testing.T) {
	testToolBoundary(t, func(t *testing.T) toolBoundary {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		readyPath := filepath.Join(dir, "ready")
		t.Setenv("PATH", dir)
		t.Setenv(probeEnv, "1")
		return toolBoundary{
			app: New(),
			ready: func() (bool, error) {
				_, err := os.Stat(readyPath)
				if errors.Is(err, os.ErrNotExist) {
					return false, nil
				}
				return err == nil, err
			},
			install: func(t *testing.T, name string, p probe) []string {
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				copyFile(t, self, filepath.Join(dir, name))
				return []string{"-test.run=^TestToolProbeHelper$", "--",
					p.stdout, p.stderr, strconv.Itoa(p.exit), strconv.FormatBool(p.hang), readyPath}
			},
		}
	})
}

func TestFakeToolBoundary(t *testing.T) {
	testToolBoundary(t, func(t *testing.T) toolBoundary {
		f := &fakeTools{paths: map[string]string{}, outputs: map[string]result{}}
		ready := make(chan struct{})
		app := &App{Stdout: io.Discard, Stderr: io.Discard, LookPath: f.lookPath, Output: f.output}
		return toolBoundary{
			app: app,
			ready: func() (bool, error) {
				select {
				case <-ready:
					return true, nil
				default:
					return false, nil
				}
			},
			install: func(t *testing.T, name string, p probe) []string {
				path := "/fake/" + name
				f.paths[name] = path
				r := result{out: p.stdout + p.stderr}
				if p.hang {
					r.ready = ready
				}
				if p.exit != 0 {
					r.err = fmt.Errorf("exit status %d", p.exit)
				}
				f.outputs[path+" --version"] = r
				return []string{"--version"}
			},
		}
	})
}

// probeEnv makes the test binary act as the probe tool when the real boundary runs it.
const probeEnv = "GUILDHALL_TEST_TOOL_PROBE"

const probeReady = "probe: hanging\n"

// TestToolProbeHelper is not a test: it is the probe tool TestRealToolBoundary runs.
func TestToolProbeHelper(t *testing.T) {
	if os.Getenv(probeEnv) != "1" {
		t.Skip("probe tool for TestRealToolBoundary")
	}
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) != at+6 {
		fmt.Fprintf(os.Stderr, "probe: bad arguments %q\n", os.Args)
		os.Exit(2)
	}
	stdout, stderr, exit, hang := os.Args[at+1], os.Args[at+2], os.Args[at+3], os.Args[at+4]
	_, _ = io.WriteString(os.Stdout, stdout)
	_, _ = io.WriteString(os.Stderr, stderr)
	if hang == "true" {
		_, _ = io.WriteString(os.Stdout, probeReady)
		if err := os.WriteFile(os.Args[at+5], nil, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(time.Hour)
	}
	code, _ := strconv.Atoi(exit)
	os.Exit(code)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeTools is the test double for App's process boundary: tools resolve from
// paths, and a command answers from outputs keyed by "path args...".
type fakeTools struct {
	paths   map[string]string
	outputs map[string]result
}

type result struct {
	out   string
	err   error
	ready chan struct{}
}

func (f *fakeTools) lookPath(file string) (string, error) {
	if p, ok := f.paths[file]; ok {
		return p, nil
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

// output answers like the real boundary: a hung command returns only once ctx ends.
func (f *fakeTools) output(ctx context.Context, path string, args ...string) ([]byte, error) {
	r, ok := f.outputs[strings.Join(append([]string{path}, args...), " ")]
	if !ok {
		return nil, errors.New("unexpected command")
	}
	if r.ready != nil {
		close(r.ready)
		<-ctx.Done()
		return []byte(r.out + probeReady), ctx.Err()
	}
	return []byte(r.out), r.err
}
