package adaptertest

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// SessionRuntime is what the Director session supervisor needs from a runtime adapter.
type SessionRuntime interface {
	SkillsDir() string
	SessionFiles(copyDir string) map[string][]byte
	Session(spec adapter.SessionSpec) (adapter.Command, error)
}

// SessionSubject is one runtime's Director session under test.
type SessionSubject struct {
	Runtime SessionRuntime
	// Script is the role prompt that makes the runtime play steps in order, each `guildhall <args>`, `/exit` or
	// `exit <n>` (fake.RunScript).
	Script func(steps ...string) string
}

// recorderLogEnv names the file the recording guildhall appends its calls to.
const recorderLogEnv = "GUILDHALL_ADAPTERTEST_RECORDER"

// recorded is one call of the recording guildhall.
type recorded struct {
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Copy string   `json:"copy"`
}

// RunIfRecorder makes the test binary act as the recording guildhall the session suite puts on PATH, and exit,
// when it was started as `guildhall` with the recorder's log named. The recorder appends its arguments, working
// directory and adapter.CopyEnv to the log and exits 1 when its first argument is "fail". Call it first in
// TestMain of a package that runs RunSessionContract.
func RunIfRecorder() {
	log := os.Getenv(recorderLogEnv)
	if log == "" || strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != "guildhall" {
		return
	}
	dir, _ := os.Getwd()
	b, _ := json.Marshal(recorded{Args: os.Args[1:], Dir: dir, Copy: os.Getenv(adapter.CopyEnv)})
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		err = errors.Join(err, f.Close())
	}
	if err != nil || len(os.Args) > 1 && os.Args[1] == "fail" {
		os.Exit(1)
	}
	os.Exit(0)
}

// RunSessionContract runs the Director session contract against the runtimes newSubject returns: the command
// runs in the session directory with the copy named in its environment, plays the role prompt's script against
// guildhall on PATH, goes on past a failing command, ends with the scripted status, at a typed /exit or at the
// end of input, and an invalid spec gets no command. It makes no model calls.
func RunSessionContract(t *testing.T, newSubject func(t *testing.T) SessionSubject) {
	t.Run("plays the script in the session directory with the copy", func(t *testing.T) {
		s, spec, log := sessionSetup(t, newSubject, "guildhall director ready", "guildhall fail now", "guildhall messages list director", "/exit")
		if code := runSession(t, s, spec, strings.NewReader("")); code != 0 {
			t.Fatalf("exit status %d, want 0", code)
		}
		calls := readRecorded(t, log)
		want := [][]string{{"director", "ready"}, {"fail", "now"}, {"messages", "list", "director"}}
		if len(calls) != len(want) {
			t.Fatalf("calls = %+v, want %q", calls, want)
		}
		for i, c := range calls {
			if !slices.Equal(c.Args, want[i]) || !samePath(c.Dir, spec.Dir) || c.Copy != spec.Copy {
				t.Errorf("call %d = %+v, want %q in %s with copy %s", i, c, want[i], spec.Dir, spec.Copy)
			}
		}
	})
	t.Run("ends with the scripted status", func(t *testing.T) {
		s, spec, log := sessionSetup(t, newSubject, "exit 3", "guildhall never")
		if code := runSession(t, s, spec, strings.NewReader("")); code != 3 {
			t.Fatalf("exit status %d, want 3", code)
		}
		if calls := readRecorded(t, log); len(calls) != 0 {
			t.Errorf("ran %+v after exit", calls)
		}
	})
	t.Run("ends at a typed /exit", func(t *testing.T) {
		s, spec, log := sessionSetup(t, newSubject, "guildhall director ready")
		// An OS pipe reaches the session as its own standard input, held open after /exit as a terminal is.
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = r.Close(), w.Close() }()
		go func() {
			_, _ = io.WriteString(w, "hello\n/exit\n")
		}()
		if code := runSession(t, s, spec, r); code != 0 {
			t.Fatalf("exit status %d, want 0", code)
		}
		if calls := readRecorded(t, log); len(calls) != 1 {
			t.Errorf("calls = %+v, want one", calls)
		}
	})
	t.Run("ends at the end of input", func(t *testing.T) {
		s, spec, _ := sessionSetup(t, newSubject)
		if code := runSession(t, s, spec, strings.NewReader("")); code != 0 {
			t.Fatalf("exit status %d, want 0", code)
		}
	})
	t.Run("refuses an invalid spec", func(t *testing.T) {
		s, spec, _ := sessionSetup(t, newSubject)
		bad := map[string]func(x *adapter.SessionSpec){
			"relative dir":     func(x *adapter.SessionSpec) { x.Dir = "session-1" },
			"relative copy":    func(x *adapter.SessionSpec) { x.Copy = "copy" },
			"relative role":    func(x *adapter.SessionSpec) { x.RolePrompt = "role.md" },
			"blank message":    func(x *adapter.SessionSpec) { x.FirstMessage = " " },
			"two-line message": func(x *adapter.SessionSpec) { x.FirstMessage = "a\nb" },
			"env":              func(x *adapter.SessionSpec) { x.Env = []string{"NOEQUALS"} },
		}
		for name, mutate := range bad {
			b := spec
			mutate(&b)
			if cmd, err := s.Runtime.Session(b); err == nil || len(cmd.Argv) > 0 {
				t.Errorf("%s: command %+v, %v; want a refusal and no command", name, cmd, err)
			}
		}
	})
}

// sessionSetup lays out a session the way the supervisor does: the role prompt playing steps, the runtime's
// files, and the recording guildhall first on PATH.
func sessionSetup(t *testing.T, newSubject func(t *testing.T) SessionSubject, steps ...string) (SessionSubject, adapter.SessionSpec, string) {
	t.Helper()
	s := newSubject(t)
	root := t.TempDir()
	spec := adapter.SessionSpec{Dir: filepath.Join(root, "copy", "director", "session-1"), Copy: filepath.Join(root, "copy"),
		FirstMessage: "You are starting up: read startup.md in this directory and follow it."}
	spec.RolePrompt = filepath.Join(spec.Dir, "role.md")
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.RolePrompt, []byte("# Director\n\n"+s.Script(steps...)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.Dir, "startup.md"), []byte("You are starting up.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, data := range s.Runtime.SessionFiles(spec.Copy) {
		path := filepath.Join(spec.Dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(spec.Dir, filepath.FromSlash(s.Runtime.SkillsDir())), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "guildhall"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.Link(exe, filepath.Join(bin, name)); err != nil {
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "calls.jsonl")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(recorderLogEnv, log)
	return s, spec, log
}

// runSession starts the session's command as the supervisor does and returns its exit status.
func runSession(t *testing.T, s SessionSubject, spec adapter.SessionSpec, stdin io.Reader) int {
	t.Helper()
	cmd, err := s.Runtime.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cmd.Env, func(kv string) bool { return kv == adapter.CopyEnv+"="+spec.Copy }) {
		t.Errorf("session environment %q does not set %s", cmd.Env, adapter.CopyEnv)
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env, c.Stdin = cmd.Dir, append(os.Environ(), cmd.Env...), stdin
	var out strings.Builder
	c.Stdout, c.Stderr = &out, &out
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err = <-done:
	case <-time.After(testenv.Ceiling(time.Minute)):
		_ = c.Process.Kill()
		t.Fatalf("session did not end:\n%s", out.String())
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("session: %v\n%s", err, out.String())
	}
	t.Logf("session output:\n%s", out.String())
	return c.ProcessState.ExitCode()
}

func readRecorded(t *testing.T, log string) []recorded {
	t.Helper()
	f, err := os.Open(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var calls []recorded
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r recorded
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, r)
	}
	return calls
}

// samePath compares two directories after resolving symbolic links, as a child's working directory may be
// reported through one (macOS's /var).
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}
