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
	t.Run("context deadline stops a hung tool", func(t *testing.T) {
		b := newBoundary(t)
		args := b.install(t, "probe-hang", probe{hang: true})
		path, err := b.app.LookPath("probe-hang")
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := b.app.Output(ctx, path, args...)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("Output error = nil after the deadline, want an error")
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Output did not return after its context deadline")
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
		t.Setenv("PATH", dir)
		t.Setenv(probeEnv, "1")
		return toolBoundary{
			app: New(),
			install: func(t *testing.T, name string, p probe) []string {
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				copyFile(t, self, filepath.Join(dir, name))
				return []string{"-test.run=^TestToolProbeHelper$", "--",
					p.stdout, p.stderr, strconv.Itoa(p.exit), strconv.FormatBool(p.hang)}
			},
		}
	})
}

func TestFakeToolBoundary(t *testing.T) {
	testToolBoundary(t, func(t *testing.T) toolBoundary {
		f := &fakeTools{paths: map[string]string{}, outputs: map[string]result{}}
		app := &App{Stdout: io.Discard, Stderr: io.Discard, LookPath: f.lookPath, Output: f.output}
		return toolBoundary{
			app: app,
			install: func(t *testing.T, name string, p probe) []string {
				path := "/fake/" + name
				f.paths[name] = path
				r := result{out: p.stdout + p.stderr, hang: p.hang}
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

// TestToolProbeHelper is not a test: it is the probe tool TestRealToolBoundary runs.
func TestToolProbeHelper(t *testing.T) {
	if os.Getenv(probeEnv) != "1" {
		t.Skip("probe tool for TestRealToolBoundary")
	}
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) != at+5 {
		fmt.Fprintf(os.Stderr, "probe: bad arguments %q\n", os.Args)
		os.Exit(2)
	}
	stdout, stderr, exit, hang := os.Args[at+1], os.Args[at+2], os.Args[at+3], os.Args[at+4]
	_, _ = io.WriteString(os.Stdout, stdout)
	_, _ = io.WriteString(os.Stderr, stderr)
	if hang == "true" {
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
	out  string
	err  error
	hang bool
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
	if r.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(r.out), r.err
}
