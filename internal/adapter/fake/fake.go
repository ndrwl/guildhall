// Package fake is the test-only Artisan adapter (R1.22). It declares no
// optional capabilities and runs `guildhall fake-artisan`, which reads its
// prompt file and writes a version 1 result file, so the core is exercised
// without any agent runtime. Prompt lines of the form
// `guildhall-fake: <key>=<value>` select bounded outcomes and faults for tests;
// docs/adapters.md lists them.
package fake

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// Subcommand is the guildhall subcommand that runs the fake Artisan.
const Subcommand = "fake-artisan"

// MaxDelay bounds the simulated working time a prompt may request.
const MaxDelay = 10 * time.Minute

// MaxPromptBytes bounds the prompt file the fake Artisan reads.
const MaxPromptBytes = 1 << 20

// directivePrefix starts a prompt line that controls the fake Artisan.
const directivePrefix = "guildhall-fake:"

// ErrSimulatedExit is returned after the result is written when the prompt asks
// for a failing exit, to show that exit status does not decide completion.
var ErrSimulatedExit = errors.New("fake artisan: simulated failing exit after writing its result")

// Adapter launches the fake Artisan from the guildhall executable.
type Adapter struct {
	// Executable is the absolute path of the guildhall binary.
	Executable string
}

// Capabilities reports the fake adapter's optional capabilities: none.
func (Adapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

// Command returns the fake Artisan invocation for spec.
func (a Adapter) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("fake adapter: guildhall executable is not set")
	}
	return adapter.Command{
		Argv: []string{a.Executable, Subcommand,
			"--task", spec.TaskID, "--attempt", spec.AttemptID,
			"--prompt", spec.PromptFile, "--result", spec.ResultFile},
		Dir: spec.WorkDir,
		Env: append([]string(nil), spec.Env...),
	}, nil
}

// Args are the fake Artisan's command-line arguments.
type Args struct {
	adapter.Identity
	PromptFile string
	ResultFile string
}

// ParseArgs parses `--task --attempt --prompt --result`, all required.
func ParseArgs(argv []string) (Args, error) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var a Args
	fs.StringVar(&a.TaskID, "task", "", "task id")
	fs.StringVar(&a.AttemptID, "attempt", "", "attempt id")
	fs.StringVar(&a.PromptFile, "prompt", "", "prompt file")
	fs.StringVar(&a.ResultFile, "result", "", "result file")
	if err := fs.Parse(argv); err != nil {
		return Args{}, err
	}
	if fs.NArg() > 0 {
		return Args{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.PromptFile == "" || a.ResultFile == "" {
		return Args{}, errors.New("--task, --attempt, --prompt and --result are required")
	}
	if err := a.Validate(); err != nil {
		return Args{}, err
	}
	return a, nil
}

// directives is the fake behaviour a prompt selects.
type directives struct {
	outcome adapter.Status
	fault   string
	delay   time.Duration
}

var faults = map[string]bool{"none": true, "no-result": true, "malformed": true, "wrong-identity": true, "exit-error": true}

func parseDirectives(prompt []byte) (directives, error) {
	d := directives{outcome: adapter.StatusSucceeded, fault: "none"}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(prompt))
	sc.Buffer(make([]byte, 0, 64*1024), MaxPromptBytes)
	for sc.Scan() {
		line, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), directivePrefix)
		if !ok {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return d, fmt.Errorf("directive %q is not key=value", line)
		}
		if seen[key] {
			return d, fmt.Errorf("directive %q repeated", key)
		}
		seen[key] = true
		switch key {
		case "outcome":
			if value != string(adapter.StatusSucceeded) && value != string(adapter.StatusFailed) {
				return d, fmt.Errorf("outcome %q must be succeeded or failed", value)
			}
			d.outcome = adapter.Status(value)
		case "fault":
			if !faults[value] {
				return d, fmt.Errorf("fault %q must be none, no-result, malformed, wrong-identity or exit-error", value)
			}
			d.fault = value
		case "delay":
			delay, err := time.ParseDuration(value)
			if err != nil || delay < 0 || delay > MaxDelay {
				return d, fmt.Errorf("delay %q must be a duration from 0s to %s", value, MaxDelay)
			}
			d.delay = delay
		default:
			return d, fmt.Errorf("unknown directive %q", key)
		}
	}
	return d, sc.Err()
}

// Sleep waits for d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run is the fake Artisan: it reads the prompt, applies its directives, and
// writes the result file. Progress lines go to out so a Herdr tab shows them.
func Run(ctx context.Context, a Args, out io.Writer, sleep func(context.Context, time.Duration) error) error {
	f, err := os.Open(a.PromptFile)
	if err != nil {
		return err
	}
	prompt, err := io.ReadAll(io.LimitReader(f, MaxPromptBytes+1))
	// The prompt was opened read-only, so a close error loses nothing.
	_ = f.Close()
	if err != nil {
		return err
	}
	if len(prompt) > MaxPromptBytes {
		return fmt.Errorf("prompt is larger than %d bytes", MaxPromptBytes)
	}
	d, err := parseDirectives(prompt)
	if err != nil {
		return fmt.Errorf("prompt %s: %w", a.PromptFile, err)
	}
	sum := sha256.Sum256(prompt)
	// Progress lines are for a watching human; failing to show them does not
	// change the result.
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, "fake artisan: "+format+"\n", args...) }
	say("task %s attempt %s", a.TaskID, a.AttemptID)
	say("read prompt %s (%d bytes)", a.PromptFile, len(prompt))
	if d.delay > 0 {
		say("working for %s", d.delay)
		if err := sleep(ctx, d.delay); err != nil {
			return err
		}
	}
	result := adapter.Result{
		Identity: a.Identity,
		Status:   d.outcome,
		Summary:  fmt.Sprintf("fake artisan read %d bytes of prompt, sha256 %x", len(prompt), sum),
		Outputs:  []adapter.Output{},
	}
	switch d.fault {
	case "no-result":
		say("exiting without a result (simulated fault)")
		return nil
	case "malformed":
		say("writing a malformed result to %s (simulated fault)", a.ResultFile)
		return writeNew(a.ResultFile, []byte(`{"schema": "`+adapter.ResultSchema+`", "version": 1, "task_id": `))
	case "wrong-identity":
		result.AttemptID = "other"
		if a.AttemptID == result.AttemptID {
			result.AttemptID = "another"
		}
	}
	if err := adapter.WriteResult(a.ResultFile, result); err != nil {
		return err
	}
	say("wrote %s result to %s", result.Status, a.ResultFile)
	if d.fault == "exit-error" {
		return ErrSimulatedExit
	}
	return nil
}

// writeNew creates path with data, refusing to replace an existing file.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", adapter.ErrResultExists, path)
		}
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}
