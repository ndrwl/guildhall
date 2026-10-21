package claude_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
)

// TestMain lets the test binary act as the stub `claude` and as
// `guildhall claude-artisan`, so the suite runs the shipped wrapper over a
// Claude Code that makes no model calls.
func TestMain(m *testing.M) {
	claudetest.RunIfStub()
	if len(os.Args) > 1 && os.Args[1] == claude.Subcommand {
		os.Exit(claude.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func subject(t *testing.T) adaptertest.Subject {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	claudetest.Install(t)
	return adaptertest.Subject{
		Runtime:      claude.Adapter{Executable: exe},
		Capabilities: adapter.Capabilities{},
		Prompt:       claudetest.Prompt,
	}
}

func TestClaudeRuntimeConformance(t *testing.T) {
	adaptertest.RunConformance(t, subject)
}

// TestClaudeRuntimeConformanceOnInterpreterAttempts runs the suite on attempts
// rendered by the interpreter, whose prompt and result live in an attempt
// directory outside the working directory.
func TestClaudeRuntimeConformanceOnInterpreterAttempts(t *testing.T) {
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		s := subject(t)
		s.Attempt = func(t *testing.T, prompt string) adapter.Spec {
			return interptest.AdapterSpec(t, interp.FakeAdapter, prompt)
		}
		return s
	})
}

// TestClaudeRuntimeConformanceThroughTheLauncher runs the suite with the
// wrapper inside the Artisan launcher, the owned-worker stop path: a stop must
// end at rung 1, the cancel marker, which the wrapper honours by stopping
// Claude.
func TestClaudeRuntimeConformanceThroughTheLauncher(t *testing.T) {
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		s := subject(t)
		s.Start = launcherStart
		return s
	})
}

// launched is a runtime the launcher runs; it has exited once Run returns.
type launched struct {
	l    *artisan.Launcher
	dir  string
	done chan struct{}
}

func (p *launched) Exited() <-chan struct{} { return p.done }

func (p *launched) Stop() error {
	if _, err := p.l.Stop(); err != nil {
		return err
	}
	out, err := artisan.ReadOutcome(p.dir)
	if err == nil && out.State == artisan.Cancelled && out.Rung > 1 {
		return fmt.Errorf("the wrapper did not honour the cancel marker: it stopped at rung %d", out.Rung)
	}
	return err
}

func launcherStart(t *testing.T, spec adapter.Spec, cmd adapter.Command) (adaptertest.Process, error) {
	u, err := artisan.NewUnit("claude-" + rand.Text()[:8])
	if err != nil {
		return nil, err
	}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{
		Dir:   filepath.Dir(spec.ResultFile),
		Unit:  u,
		Files: launch.Files{ResultFile: spec.ResultFile, Identity: spec.Identity},
		Key:   artisan.AttemptKey{Repository: "app", TaskID: spec.TaskID, Step: "build", Attempt: 1},
	})
	if err != nil {
		return nil, errors.Join(err, u.Close())
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	p := &launched{l: l, dir: filepath.Dir(spec.ResultFile), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		if _, err := l.Run(c, nil); err != nil && !errors.Is(err, artisan.ErrDecided) {
			t.Errorf("launcher Run = %v", err)
		}
		if err := u.Close(); err != nil {
			t.Errorf("unit Close = %v", err)
		}
	}()
	return p, nil
}

func testSpec(t *testing.T) adapter.Spec {
	t.Helper()
	work, attempt := t.TempDir(), t.TempDir()
	spec := adapter.Spec{
		Identity:   adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"},
		WorkDir:    work,
		PromptFile: filepath.Join(attempt, "prompt.md"),
		ResultFile: filepath.Join(attempt, "result.json"),
		Env:        []string{"A=1"},
	}
	if err := os.WriteFile(spec.PromptFile, []byte("Do the task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec
}

func args(spec adapter.Spec) claude.Args {
	return claude.Args{Identity: spec.Identity, PromptFile: spec.PromptFile, ResultFile: spec.ResultFile}
}

func TestCommand(t *testing.T) {
	spec := testSpec(t)
	cmd, err := claude.Adapter{Executable: "/bin/guildhall"}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", claude.Subcommand, "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.WorkDir || !slices.Equal(cmd.Env, spec.Env) {
		t.Errorf("Command = %+v, want argv %q in %s with %q", cmd, want, spec.WorkDir, spec.Env)
	}
	cmd, err = claude.Adapter{Executable: "/bin/guildhall", Claude: "/opt/claude"}.Command(spec)
	if err != nil || !slices.Equal(cmd.Argv[len(cmd.Argv)-2:], []string{"--claude", "/opt/claude"}) {
		t.Errorf("Command with Claude = %+v, %v; want it to end --claude /opt/claude", cmd, err)
	}
	parsed, err := claude.ParseArgs(cmd.Argv[2:])
	if err != nil || parsed.Identity != spec.Identity || parsed.PromptFile != spec.PromptFile || parsed.ResultFile != spec.ResultFile || parsed.Claude != "/opt/claude" {
		t.Errorf("ParseArgs(Command) = %+v, %v", parsed, err)
	}
	if cmd, err := (claude.Adapter{}).Command(spec); err == nil || len(cmd.Argv) != 0 {
		t.Errorf("Command without Executable = %+v, %v; want a refusal", cmd, err)
	}
}

func TestParseArgsRefuses(t *testing.T) {
	spec := testSpec(t)
	good := []string{"--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile}
	for name, argv := range map[string][]string{
		"missing result":   good[:6],
		"relative prompt":  {"--task", "task-1", "--attempt", "attempt-1", "--prompt", "prompt.md", "--result", spec.ResultFile},
		"same files":       {"--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.ResultFile, "--result", spec.ResultFile},
		"bad identity":     {"--task", "", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile},
		"extra argument":   append(slices.Clone(good), "more"),
		"unknown flag":     append(slices.Clone(good), "--model", "x"),
		"relative program": append(slices.Clone(good), "--claude", "bin/claude"),
	} {
		if _, err := claude.ParseArgs(argv); err == nil {
			t.Errorf("%s: ParseArgs(%q) accepted", name, argv)
		}
		var stderr bytes.Buffer
		if code := claude.Main(argv, &bytes.Buffer{}, &stderr); code != 2 {
			t.Errorf("%s: Main exit %d, want 2; stderr %q", name, code, stderr.String())
		}
	}
}

func TestClaudeArgs(t *testing.T) {
	spec := testSpec(t)
	a := args(spec)
	got := claude.ClaudeArgs(a)
	want := []string{"--print", claude.Instruction(a), "--permission-mode", claude.PermissionMode, "--add-dir", filepath.Dir(spec.PromptFile)}
	if !slices.Equal(got, want) {
		t.Errorf("ClaudeArgs = %q, want %q", got, want)
	}
	in := claude.Instruction(a)
	for _, s := range []string{
		claudetest.PromptLine + spec.PromptFile + "\n",
		claudetest.DraftLine + claude.DraftFile(spec.ResultFile) + "\n",
		`"schema":"guildhall.artisan-result"`, `"version":1`, `"task_id":"task-1"`, `"attempt_id":"attempt-1"`,
		`"succeeded"`, `"failed"`, "outputs",
	} {
		if !strings.Contains(in, s) {
			t.Errorf("Instruction lacks %q:\n%s", s, in)
		}
	}
	if strings.Contains(in, spec.ResultFile+"\n") {
		t.Errorf("Instruction names the result file itself, which only the wrapper writes:\n%s", in)
	}
	other := a
	other.ResultFile = filepath.Join(t.TempDir(), "result.json")
	if got := claude.ClaudeArgs(other); len(got) < 2 || !slices.Equal(got[len(got)-2:], []string{filepath.Dir(spec.PromptFile), filepath.Dir(other.ResultFile)}) {
		t.Errorf("ClaudeArgs with separate result directory = %q; want both directories added", got)
	}
}

// TestRunRefusesWithoutStartingClaude: a cancelled attempt or an existing
// result starts nothing, so a missing Claude is never reached.
func TestRunRefusesWithoutStartingClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Run("cancelled", func(t *testing.T) {
		spec := testSpec(t)
		if err := adapter.MarkCancelled(spec.ResultFile, spec.Identity); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, adapter.ErrAttemptCancelled) {
			t.Errorf("Run = %v, want ErrAttemptCancelled", err)
		}
	})
	t.Run("existing result", func(t *testing.T) {
		spec := testSpec(t)
		if err := os.WriteFile(spec.ResultFile, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, adapter.ErrResultExists) {
			t.Errorf("Run = %v, want ErrResultExists", err)
		}
		if b, _ := os.ReadFile(spec.ResultFile); string(b) != "kept" {
			t.Errorf("result replaced: %q", b)
		}
	})
	t.Run("missing claude", func(t *testing.T) {
		spec := testSpec(t)
		err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{})
		if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), claude.Program) {
			t.Errorf("Run = %v, want exec.ErrNotFound naming %s", err, claude.Program)
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("result written: %v", err)
		}
	})
}

// TestRunDraftHandling: a stale draft is never published, and a draft whose
// result already appeared is not published over it.
func TestRunDraftHandling(t *testing.T) {
	claudetest.Install(t)
	t.Run("stale draft removed", func(t *testing.T) {
		spec := testSpec(t)
		if err := os.WriteFile(claude.DraftFile(spec.ResultFile), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.NoResult)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, claude.ErrNoDraft) {
			t.Errorf("Run = %v, want ErrNoDraft", err)
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale draft published: %v", err)
		}
	})
	t.Run("published once", func(t *testing.T) {
		spec := testSpec(t)
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
		if err != nil || r.Status != adapter.StatusSucceeded {
			t.Errorf("ReadResult = %+v, %v", r, err)
		}
	})
}

// TestRunStopsOnContext: ending ctx stops Claude and publishes nothing.
func TestRunStopsOnContext(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Hang)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := claude.Run(ctx, args(spec), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want the context's end", err)
	}
	if d := time.Since(start); d < 400*time.Millisecond || d > claude.StopGrace+5*time.Second {
		t.Errorf("Run returned after %s; want Claude running until the context ended, then stopped", d)
	}
	if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stopped attempt has a result: %v", err)
	}
}

// LiveEnv opts into TestLiveClaude, which runs a real Claude Code session and
// so makes a model call; CI never sets it.
const LiveEnv = "GUILDHALL_CLAUDE_LIVE"

// TestLiveClaude runs one real attempt through the wrapper with the `claude`
// on PATH, signed in as the developer: run it by hand with
// GUILDHALL_CLAUDE_LIVE=1 go test ./internal/adapter/claude -run TestLiveClaude -v -timeout 15m
func TestLiveClaude(t *testing.T) {
	if os.Getenv(LiveEnv) != "1" {
		t.Skipf("set %s=1 to run a real Claude Code session; it makes a model call", LiveEnv)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t)
	prompt := "This is a Guildhall adapter check. Create the file hello.txt in the working directory containing the single line `hello`, " +
		"then report success, listing hello.txt as your one output.\n"
	if err := os.WriteFile(spec.PromptFile, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := claude.Adapter{Executable: exe}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := adapter.StartProcess(cmd, os.Stderr, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(); <-p.Exited() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Exited: p.Exited()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result: %+v", r)
	got, err := os.ReadFile(filepath.Join(spec.WorkDir, "hello.txt"))
	if r.Status != adapter.StatusSucceeded || err != nil || strings.TrimSpace(string(got)) != "hello" {
		t.Errorf("result %+v; hello.txt %q, %v", r, got, err)
	}
}
