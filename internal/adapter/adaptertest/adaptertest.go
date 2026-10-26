// Package adaptertest is the conformance suite every Artisan runtime adapter
// passes before Guildhall launches it: the launch and result protocol of
// docs/adapters.md, exercised through a real child process. It makes no model
// calls; a runtime proves each behaviour through prompts it understands.
package adaptertest

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// Runtime is what the launcher needs from a runtime adapter: its declared
// optional capabilities and the argument-list command for one attempt.
type Runtime interface {
	Capabilities() adapter.Capabilities
	Command(spec adapter.Spec) (adapter.Command, error)
}

// Behaviour is an Artisan outcome the suite asks a runtime to produce.
type Behaviour string

// The behaviours every runtime must be able to produce on request.
const (
	// Succeed writes a valid succeeded result, then exits 0.
	Succeed Behaviour = "succeed"
	// Fail writes a valid failed result.
	Fail Behaviour = "fail"
	// NoResult exits without writing a result.
	NoResult Behaviour = "no-result"
	// Malformed writes a result file that breaks the contract.
	Malformed Behaviour = "malformed"
	// WrongIdentity writes a valid result for another attempt.
	WrongIdentity Behaviour = "wrong-identity"
	// ExitErrorAfterResult writes a valid succeeded result, then exits nonzero.
	ExitErrorAfterResult Behaviour = "exit-error"
	// Hang keeps working without a result until stopped.
	Hang Behaviour = "hang"
)

// Subject is one runtime under test.
type Subject struct {
	Runtime Runtime
	// Capabilities are exactly the optional capabilities the runtime declares.
	Capabilities adapter.Capabilities
	// Prompt is the prompt text that makes the runtime produce b.
	Prompt func(b Behaviour) string
	// Attempt, when set, lays out one attempt whose prompt carries prompt and
	// returns its spec, the way the launch path feeding this runtime does, such
	// as a prompt rendered by the interpreter and mapped through the reference
	// mapping interp.Spec.AdapterSpec; nil writes prompt as the whole prompt file in a
	// new temporary directory.
	Attempt func(t *testing.T, prompt string) adapter.Spec
	// Start, when set, starts the runtime's command for spec the way a
	// launch path runs it, such as inside the Artisan launcher with its stop
	// ladder; nil starts it as a direct child process (adapter.StartProcess).
	Start func(t *testing.T, spec adapter.Spec, cmd adapter.Command) (Process, error)
}

// Process is a started runtime as the suite drives it.
type Process interface {
	// Exited is closed once the runtime, and whatever supervises it, has
	// ended.
	Exited() <-chan struct{}
	// Stop asks the launch path to stop the runtime; stopping an ended one is
	// not an error.
	Stop() error
}

// RunConformance runs the suite against the runtimes newSubject returns.
func RunConformance(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Run("declares exactly its capabilities", func(t *testing.T) {
		s := newSubject(t)
		if got := s.Runtime.Capabilities(); got != s.Capabilities {
			t.Errorf("Capabilities() = %+v, want %+v", got, s.Capabilities)
		}
	})
	t.Run("refuses an invalid spec without a command", func(t *testing.T) {
		s := newSubject(t)
		good := newSpec(t, s, Succeed)
		bad := map[string]adapter.Spec{"no identity": withID(good, adapter.Identity{}), "relative result": withResult(good, "result.json")}
		for name, spec := range bad {
			cmd, err := s.Runtime.Command(spec)
			if err == nil || len(cmd.Argv) != 0 {
				t.Errorf("%s: Command = %+v, %v; want a refusal and no command", name, cmd, err)
			}
		}
	})
	t.Run("command runs in the working directory with the spec environment", func(t *testing.T) {
		s := newSubject(t)
		spec := newSpec(t, s, Succeed)
		spec.Env = []string{"GUILDHALL_CONFORMANCE=1"}
		cmd, err := s.Runtime.Command(spec)
		if err != nil {
			t.Fatal(err)
		}
		if len(cmd.Argv) == 0 || cmd.Dir != spec.WorkDir || !slices.Contains(cmd.Env, "GUILDHALL_CONFORMANCE=1") {
			t.Errorf("command = %+v", cmd)
		}
	})
	completes := []struct {
		b    Behaviour
		want adapter.Status
	}{{Succeed, adapter.StatusSucceeded}, {Fail, adapter.StatusFailed}, {ExitErrorAfterResult, adapter.StatusSucceeded}}
	for _, tc := range completes {
		t.Run("completes from the result file: "+string(tc.b), func(t *testing.T) {
			s := newSubject(t)
			spec := newSpec(t, s, tc.b)
			r, err := launchAndWait(t, s, spec, time.Minute)
			if err != nil {
				t.Fatalf("WaitResult = %v", err)
			}
			if r.Identity != spec.Identity || r.Status != tc.want {
				t.Errorf("result = %+v, want status %s for %+v", r, tc.want, spec.Identity)
			}
		})
	}
	rejects := []struct {
		b    Behaviour
		want error
	}{{NoResult, adapter.ErrExitedWithoutResult}, {Malformed, adapter.ErrMalformedResult}, {WrongIdentity, adapter.ErrIdentityMismatch}}
	for _, tc := range rejects {
		t.Run("never completes on "+string(tc.b), func(t *testing.T) {
			s := newSubject(t)
			if _, err := launchAndWait(t, s, newSpec(t, s, tc.b), time.Minute); !errors.Is(err, tc.want) {
				t.Errorf("WaitResult = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("a stopped attempt leaves no result", func(t *testing.T) {
		s := newSubject(t)
		spec := newSpec(t, s, Hang)
		p := start(t, s, spec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancellation is input; no runtime startup must fit in a tiny result window.
		if _, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Poll: 10 * time.Millisecond, Exited: p.Exited()}); !errors.Is(err, adapter.ErrNoResultYet) {
			t.Fatalf("WaitResult while working = %v, want ErrNoResultYet", err)
		}
		if err := p.Stop(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.Exited():
		case <-time.After(testenv.Ceiling(30 * time.Second)):
			t.Fatal("runtime did not exit after Stop")
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stopped attempt left a result: %v", err)
		}
	})
	t.Run("an existing result is never replaced", func(t *testing.T) {
		s := newSubject(t)
		spec := newSpec(t, s, Succeed)
		if _, err := launchAndWait(t, s, spec, time.Minute); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(spec.ResultFile)
		if err != nil {
			t.Fatal(err)
		}
		rewritePrompt(t, s, spec, Fail)
		p := start(t, s, spec)
		select {
		case <-p.Exited():
		case <-time.After(testenv.Ceiling(time.Minute)):
			t.Fatal("second run did not exit")
		}
		after, err := os.ReadFile(spec.ResultFile)
		if err != nil || !bytes.Equal(before, after) {
			t.Errorf("result changed by a second run: %s, %v", after, err)
		}
	})
}

func newSpec(t *testing.T, s Subject, b Behaviour) adapter.Spec {
	t.Helper()
	if s.Attempt != nil {
		return s.Attempt(t, s.Prompt(b))
	}
	dir := t.TempDir()
	spec := adapter.Spec{
		Identity:   adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"},
		WorkDir:    dir,
		PromptFile: filepath.Join(dir, "prompt.md"),
		ResultFile: filepath.Join(dir, "result.json"),
	}
	writePrompt(t, spec, s.Prompt(b))
	return spec
}

// rewritePrompt replaces spec's prompt with the one that makes the runtime
// produce b, laid out the way s lays out every attempt.
func rewritePrompt(t *testing.T, s Subject, spec adapter.Spec, b Behaviour) {
	t.Helper()
	prompt := s.Prompt(b)
	if s.Attempt != nil {
		rendered, err := os.ReadFile(s.Attempt(t, prompt).PromptFile)
		if err != nil {
			t.Fatal(err)
		}
		prompt = string(rendered)
	}
	writePrompt(t, spec, prompt)
}

func writePrompt(t *testing.T, spec adapter.Spec, prompt string) {
	t.Helper()
	if err := os.WriteFile(spec.PromptFile, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
}

func withID(s adapter.Spec, id adapter.Identity) adapter.Spec { s.Identity = id; return s }
func withResult(s adapter.Spec, p string) adapter.Spec        { s.ResultFile = p; return s }

func start(t *testing.T, s Subject, spec adapter.Spec) Process {
	t.Helper()
	cmd, err := s.Runtime.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	var p Process
	if s.Start != nil {
		p, err = s.Start(t, spec, cmd)
	} else {
		p, err = adapter.StartProcess(cmd, nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop()
		<-p.Exited()
	})
	return p
}

func launchAndWait(t *testing.T, s Subject, spec adapter.Spec, timeout time.Duration) (adapter.Result, error) {
	t.Helper()
	p := start(t, s, spec)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Poll: 10 * time.Millisecond, Exited: p.Exited()})
	// Let the runtime finish so the next case starts from a quiet host.
	select {
	case <-p.Exited():
	case <-time.After(timeout):
	}
	return r, err
}
