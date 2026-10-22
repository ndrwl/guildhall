package fake

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
)

var testID = adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"}

func TestAdapterDeclaresNoCapabilitiesAndBuildsArgv(t *testing.T) {
	a := Adapter{Executable: "/opt/guildhall"}
	if a.Capabilities().Any() {
		t.Error("fake adapter declares an optional capability")
	}
	dir := t.TempDir()
	spec := adapter.Spec{Identity: testID, WorkDir: dir, PromptFile: filepath.Join(dir, "p.md"), ResultFile: filepath.Join(dir, "r.json"), Env: []string{"A=1"}}
	cmd, err := a.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/guildhall", "fake-artisan", "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != dir || !slices.Equal(cmd.Env, []string{"A=1"}) {
		t.Errorf("command = %+v", cmd)
	}
	if _, err := a.Command(adapter.Spec{}); err == nil {
		t.Error("Command accepted an invalid spec")
	}
	if _, err := (Adapter{}).Command(spec); err == nil {
		t.Error("Command accepted an empty executable")
	}
}

// setup writes a prompt and returns the run arguments for it.
func setup(t *testing.T, prompt string) Args {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(p, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	return Args{Identity: testID, PromptFile: p, ResultFile: filepath.Join(dir, "result.json")}
}

func run(t *testing.T, args Args) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), args, &out, func(context.Context, time.Duration) error { return nil })
	return out.String(), err
}

func TestRunWritesSucceededResultFromPrompt(t *testing.T) {
	args := setup(t, "# Task\nDo the thing.\n")
	out, err := run(t, args)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	r, err := adapter.ReadResult(args.ResultFile, testID)
	if err != nil {
		t.Fatalf("ReadResult = %v", err)
	}
	if r.Status != adapter.StatusSucceeded || !strings.Contains(r.Summary, "21 bytes") || !strings.Contains(r.Summary, "sha256 ") {
		t.Errorf("result = %+v", r)
	}
	if !strings.Contains(out, "task-1") || !strings.Contains(out, args.ResultFile) {
		t.Errorf("output %q does not show the task and result path", out)
	}
}

func TestRunOutcomeFailed(t *testing.T) {
	args := setup(t, "guildhall-fake: outcome=failed\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ReadResult(args.ResultFile, testID)
	if err != nil || r.Status != adapter.StatusFailed {
		t.Errorf("result = %+v, %v", r, err)
	}
}

func TestRunFaults(t *testing.T) {
	t.Run("no-result", func(t *testing.T) {
		args := setup(t, "guildhall-fake: fault=no-result\n")
		if _, err := run(t, args); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("result exists: %v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		args := setup(t, "guildhall-fake: fault=malformed\n")
		if _, err := run(t, args); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.ReadResult(args.ResultFile, testID); !errors.Is(err, adapter.ErrMalformedResult) {
			t.Errorf("ReadResult = %v, want ErrMalformedResult", err)
		}
	})
	t.Run("wrong-identity", func(t *testing.T) {
		args := setup(t, "guildhall-fake: fault=wrong-identity\n")
		if _, err := run(t, args); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.ReadResult(args.ResultFile, testID); !errors.Is(err, adapter.ErrIdentityMismatch) {
			t.Errorf("ReadResult = %v, want ErrIdentityMismatch", err)
		}
	})
	t.Run("exit-error", func(t *testing.T) {
		args := setup(t, "guildhall-fake: fault=exit-error\n")
		if _, err := run(t, args); !errors.Is(err, ErrSimulatedExit) {
			t.Fatalf("Run = %v, want ErrSimulatedExit", err)
		}
		if r, err := adapter.ReadResult(args.ResultFile, testID); err != nil || r.Status != adapter.StatusSucceeded {
			t.Errorf("result after simulated failing exit = %+v, %v", r, err)
		}
	})
}

func TestRunWrongIdentityIsValidAndDifferent(t *testing.T) {
	for _, attempt := range []string{"a", "other", "another", strings.Repeat("a", 122), strings.Repeat("a", 123), strings.Repeat("a", 128)} {
		t.Run(attempt, func(t *testing.T) {
			args := setup(t, "guildhall-fake: fault=wrong-identity\n")
			args.AttemptID = attempt
			if err := args.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := run(t, args); err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.ReadResult(args.ResultFile, args.Identity); !errors.Is(err, adapter.ErrIdentityMismatch) {
				t.Fatalf("ReadResult = %v, want ErrIdentityMismatch", err)
			}
		})
	}
}

func TestRunDelayIsBoundedAndCancellable(t *testing.T) {
	args := setup(t, "guildhall-fake: delay=90s\n")
	var slept time.Duration
	err := Run(context.Background(), args, &bytes.Buffer{}, func(_ context.Context, d time.Duration) error { slept = d; return nil })
	if err != nil || slept != 90*time.Second {
		t.Errorf("Run = %v, slept %s", err, slept)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args = setup(t, "guildhall-fake: delay=5m\n")
	if err := Run(ctx, args, &bytes.Buffer{}, Sleep); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Run = %v", err)
	}
	if _, err := os.Stat(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cancelled run wrote a result: %v", err)
	}
}

func TestRunRefusesBadDirectivesBeforeActing(t *testing.T) {
	for _, prompt := range []string{
		"guildhall-fake: outcome=maybe\n",
		"guildhall-fake: fault=explode\n",
		"guildhall-fake: delay=2h\n",
		"guildhall-fake: delay=-1s\n",
		"guildhall-fake: color=red\n",
		"guildhall-fake: outcome\n",
		"guildhall-fake: outcome=failed\nguildhall-fake: outcome=succeeded\n",
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

func TestRunRefusesMissingPromptAndExistingResult(t *testing.T) {
	args := setup(t, "x")
	args.PromptFile += ".missing"
	if _, err := run(t, args); err == nil {
		t.Error("missing prompt accepted")
	}
	args = setup(t, "x")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, args); !errors.Is(err, adapter.ErrResultExists) {
		t.Errorf("second run = %v, want ErrResultExists", err)
	}
}

func TestParseArgs(t *testing.T) {
	a, err := ParseArgs([]string{"--task", "t", "--attempt", "a", "--prompt", "/p", "--result", "/r"})
	if err != nil || a.TaskID != "t" || a.AttemptID != "a" || a.PromptFile != "/p" || a.ResultFile != "/r" {
		t.Errorf("ParseArgs = %+v, %v", a, err)
	}
	for _, bad := range [][]string{
		{},
		{"--task", "t", "--attempt", "a", "--prompt", "/p"},
		{"--task", "t", "--attempt", "a", "--prompt", "/p", "--result", "/r", "extra"},
		{"--task", "bad/id", "--attempt", "a", "--prompt", "/p", "--result", "/r"},
	} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("ParseArgs(%q) accepted", bad)
		}
	}
}

func TestRunOutputDirective(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	args := setup(t, "Plan the change.\nguildhall-fake: output=notes/spec.md:Sort tasks by id.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ReadResult(args.ResultFile, testID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != adapter.StatusSucceeded || len(r.Outputs) != 1 || r.Outputs[0].Path != "notes/spec.md" {
		t.Errorf("result = %+v, want one output notes/spec.md", r)
	}
	if got, err := os.ReadFile(filepath.Join(work, "notes", "spec.md")); err != nil || string(got) != "Sort tasks by id.\n" {
		t.Errorf("output file = %q, %v", got, err)
	}
	for _, prompt := range []string{
		"guildhall-fake: output=spec.md\n",
		"guildhall-fake: output=:text\n",
		"guildhall-fake: output=../spec.md:text\n",
		"guildhall-fake: output=/tmp/spec.md:text\n",
		`guildhall-fake: output=notes\spec.md:text` + "\n",
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

// TestRunWritesResultDespiteUsageFailure: a usage file that cannot be written does not stop the result.
func TestRunWritesResultDespiteUsageFailure(t *testing.T) {
	args := setup(t, "Do it.\n")
	if err := os.Mkdir(adapter.UsageFile(args.ResultFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, args); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusSucceeded {
		t.Errorf("ReadResult = %+v, %v; want a succeeded result", r, err)
	}
}

// TestRunReportsFixedUsage: a worker whose work ends writes ReportedUsage beside its result, with a result or
// not; one stopped without a result writes none, so its usage is unknown.
func TestRunReportsFixedUsage(t *testing.T) {
	for _, prompt := range []string{"Do it.\n", "guildhall-fake: outcome=failed\n", "guildhall-fake: fault=no-result\n", "guildhall-fake: fault=exit-error\n"} {
		args := setup(t, prompt)
		_, _ = run(t, args)
		if got, err := adapter.ReadUsage(args.ResultFile); err != nil || got != ReportedUsage {
			t.Errorf("%q: usage = %+v, %v; want %+v", prompt, got, err, ReportedUsage)
		}
	}
	args := setup(t, "guildhall-fake: delay=1m\n")
	err := Run(context.Background(), args, &bytes.Buffer{}, func(context.Context, time.Duration) error { return context.Canceled })
	if err == nil {
		t.Fatal("an interrupted delay succeeded")
	}
	if _, err := os.Stat(adapter.UsageFile(args.ResultFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an interrupted worker wrote a usage file: %v", err)
	}
	if err := ReportedUsage.Validate(); err != nil {
		t.Errorf("ReportedUsage is invalid: %v", err)
	}
}
