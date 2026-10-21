// Package claude is the Claude Code Artisan adapter (R1.22): the minimal M3
// runtime that runs one attempt as a non-interactive Claude Code session.
//
// The adapter's command is `guildhall claude-artisan`, a wrapper that runs
// `claude --print` in the attempt's working directory with an instruction
// naming the prompt file and a draft result file, then publishes the draft as
// the attempt's result file exactly once. The wrapper, not the model, keeps the
// result protocol of docs/adapters.md: it refuses to start when the result
// file exists or the attempt is cancelled, never replaces a result, honours
// the cancel marker by stopping Claude and publishing nothing, and publishes
// the draft's bytes unvalidated, so a malformed or misidentified draft is
// rejected by the reader as any other result is.
//
// It declares no optional capabilities (R1.24): no pre-assigned session or
// resume, no background session, hooks, structured output or remote control.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// Subcommand is the guildhall subcommand that runs the Claude wrapper.
const Subcommand = "claude-artisan"

// Program is the Claude Code executable the wrapper runs when none is named:
// looked up on PATH when the attempt starts.
const Program = "claude"

// PermissionMode is the Claude Code permission mode of every attempt: file
// edits are allowed in the working directory and the prompt and result
// directories, and every other tool needing permission is denied, since a
// non-interactive session has no one to ask.
const PermissionMode = "acceptEdits"

// DraftSuffix is appended to the result file's path to name the draft Claude
// writes; the wrapper publishes it as the result.
const DraftSuffix = ".draft"

// StopGrace is how long the wrapper waits for Claude to exit after asking it
// to stop before killing it.
const StopGrace = 10 * time.Second

// ErrNoDraft reports that Claude exited without writing its draft result, so
// the attempt has no result.
var ErrNoDraft = errors.New("claude exited without writing a result")

// Adapter launches the Claude wrapper from the guildhall executable.
type Adapter struct {
	// Executable is the absolute path of the guildhall binary.
	Executable string
	// Claude is the Claude Code program the wrapper runs; empty means Program
	// on PATH.
	Claude string
}

// Capabilities reports the Claude adapter's optional capabilities: none.
func (Adapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

// Command returns the wrapper invocation for spec: an argument list run in
// spec's working directory with spec's environment. It refuses an invalid
// spec or an unset Executable, returning no command.
func (a Adapter) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("claude adapter: guildhall executable is not set")
	}
	argv := []string{a.Executable, Subcommand,
		"--task", spec.TaskID, "--attempt", spec.AttemptID,
		"--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if a.Claude != "" {
		argv = append(argv, "--claude", a.Claude)
	}
	return adapter.Command{Argv: argv, Dir: spec.WorkDir, Env: append([]string(nil), spec.Env...)}, nil
}

// Args are the wrapper's command-line arguments.
type Args struct {
	adapter.Identity
	PromptFile string
	ResultFile string
	// Claude is the Claude Code program; empty means Program on PATH.
	Claude string
}

// ParseArgs parses `--task --attempt --prompt --result [--claude]`; every
// path must be absolute and the prompt and result files must differ.
func ParseArgs(argv []string) (Args, error) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var a Args
	fs.StringVar(&a.TaskID, "task", "", "task id")
	fs.StringVar(&a.AttemptID, "attempt", "", "attempt id")
	fs.StringVar(&a.PromptFile, "prompt", "", "prompt file")
	fs.StringVar(&a.ResultFile, "result", "", "result file")
	fs.StringVar(&a.Claude, "claude", "", "Claude Code program")
	if err := fs.Parse(argv); err != nil {
		return Args{}, err
	}
	if fs.NArg() > 0 {
		return Args{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.PromptFile == "" || a.ResultFile == "" {
		return Args{}, errors.New("--task, --attempt, --prompt and --result are required")
	}
	if a.Claude != "" && !filepath.IsAbs(a.Claude) {
		return Args{}, fmt.Errorf("--claude %q must be an absolute path", a.Claude)
	}
	if err := a.Validate(); err != nil {
		return Args{}, err
	}
	if !filepath.IsAbs(a.PromptFile) || !filepath.IsAbs(a.ResultFile) {
		return Args{}, errors.New("--prompt and --result must be absolute paths")
	}
	if filepath.Clean(a.PromptFile) == filepath.Clean(a.ResultFile) {
		return Args{}, errors.New("--prompt and --result must differ")
	}
	return a, nil
}

// DraftFile is the draft result Claude writes for the result file result.
func DraftFile(result string) string { return result + DraftSuffix }

// Instruction is the prompt Claude Code is given for attempt a: read and
// follow the prompt file, then write the version 1 result for a's identity to
// the draft file, once, as the last action.
func Instruction(a Args) string {
	tmpl, _ := json.Marshal(struct {
		Schema    string   `json:"schema"`
		Version   int      `json:"version"`
		TaskID    string   `json:"task_id"`
		AttemptID string   `json:"attempt_id"`
		Status    string   `json:"status"`
		Summary   string   `json:"summary"`
		Outputs   []string `json:"outputs"`
	}{adapter.ResultSchema, adapter.ResultVersion, a.TaskID, a.AttemptID, string(adapter.StatusSucceeded), "What was done, in one or more sentences.", []string{}})
	return strings.Join([]string{
		fmt.Sprintf("You are a Guildhall Artisan working on task %s, attempt %s, in the current directory.", a.TaskID, a.AttemptID),
		"Read the prompt file below and follow it; the inputs and skills directories it names are beside it.",
		"",
		"Prompt file: " + a.PromptFile,
		"Result file: " + DraftFile(a.ResultFile),
		"",
		"When you have finished, or cannot go on, write your result to the result file above, once, as your last action, and nowhere else.",
		"It is one JSON object, exactly this shape:",
		string(tmpl),
		`status is "succeeded" when the prompt's work is done and "failed" when it is not; summary says what was done, or why it failed.`,
		`outputs lists each file the prompt asked you to produce as {"path": "<relative path with / separators in the current directory>", "description": "<optional>"}, and may be empty.`,
		"Use no other fields, and do not change task_id, attempt_id, schema or version.",
		"",
	}, "\n")
}

// ClaudeArgs are the arguments after the program for attempt a: print mode
// with Instruction as the prompt, PermissionMode, and the prompt and result
// directories added for tool access.
func ClaudeArgs(a Args) []string {
	dirs := []string{filepath.Dir(a.PromptFile)}
	if d := filepath.Dir(a.ResultFile); d != dirs[0] {
		dirs = append(dirs, d)
	}
	return append([]string{"--print", Instruction(a), "--permission-mode", PermissionMode, "--add-dir"}, dirs...)
}

// Main runs `guildhall claude-artisan` with args, the arguments after the
// subcommand, and returns its exit status: 0 when Run succeeds, 1 when it
// fails and 2 for arguments ParseArgs refuses.
func Main(args []string, stdout, stderr io.Writer) int {
	a, err := ParseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: %v\n", Subcommand, err)
		return 2
	}
	if err := Run(context.Background(), a, stdout, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: %v\n", Subcommand, err)
		return 1
	}
	return 0
}

// markerPoll is how often Run looks for the cancel marker.
const markerPoll = 50 * time.Millisecond

// errMarked ends Claude's session when the attempt's cancel marker appears.
var errMarked = errors.New("cancel marker present")

// Run is the wrapper. It refuses, starting nothing, a cancelled attempt
// (adapter.ErrAttemptCancelled) or an existing result file
// (adapter.ErrResultExists); it removes a stale draft, then runs Claude in the
// current directory with ClaudeArgs, no standard input, and its output on
// stdout and stderr. An interrupt, the end of ctx, or the attempt's cancel
// marker asks Claude to stop (an interrupt on Unix, termination on Windows),
// killing it after StopGrace, and publishes nothing; the marker is not an
// error. Once Claude exits, the draft's bytes are published at the result
// file, never replacing one, unless the attempt was cancelled meanwhile; no
// draft is ErrNoDraft. Claude's exit status does not decide completion: a
// failing exit after a draft is published is still returned as an error.
func Run(ctx context.Context, a Args, stdout, stderr io.Writer) error {
	if marked, err := adapter.Cancelled(a.ResultFile, a.Identity); err != nil || marked {
		return errors.Join(err, cancelledErr(marked))
	}
	if _, err := os.Lstat(a.ResultFile); err == nil {
		return fmt.Errorf("%w: %s", adapter.ErrResultExists, a.ResultFile)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	draft := DraftFile(a.ResultFile)
	if err := os.Remove(draft); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	program := a.Claude
	if program == "" {
		program = Program
	}
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("claude adapter: %w", err)
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt)
	defer stopSignals()
	ctx, cancel := watchMarker(ctx, a)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, ClaudeArgs(a)...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return stop(cmd.Process) }
	cmd.WaitDelay = StopGrace
	runErr := cmd.Run()
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errMarked) {
			_, _ = fmt.Fprintf(stderr, "guildhall: %s: cancel marker present; Claude stopped without a result\n", Subcommand)
			return nil
		}
		return fmt.Errorf("claude adapter: stopped without a result: %w", context.Cause(ctx))
	}
	if err := publish(draft, a); err != nil {
		return errors.Join(err, runErr)
	}
	if runErr != nil {
		return fmt.Errorf("claude adapter: Claude exited after writing its result: %w", runErr)
	}
	return nil
}

func cancelledErr(marked bool) error {
	if marked {
		return adapter.ErrAttemptCancelled
	}
	return nil
}

// watchMarker returns ctx ended with cause errMarked once a's cancel marker
// is present.
func watchMarker(ctx context.Context, a Args) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		t := time.NewTicker(markerPoll)
		defer t.Stop()
		for {
			if marked, _ := adapter.Cancelled(a.ResultFile, a.Identity); marked {
				cancel(errMarked)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return ctx, func() { cancel(nil) }
}

// publish links the draft's bytes into place as a's result file: a reader
// sees no file or the whole draft, an existing result is never replaced, and
// a cancelled attempt publishes nothing.
func publish(draft string, a Args) error {
	f, err := os.Open(draft)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoDraft
	}
	if err != nil {
		return err
	}
	// A draft larger than a result may be is published truncated past the
	// limit, so the reader still refuses it as too large.
	data, err := io.ReadAll(io.LimitReader(f, adapter.MaxResultBytes+1))
	_ = f.Close()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.ResultFile), "."+filepath.Base(a.ResultFile)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	if marked, err := adapter.Cancelled(a.ResultFile, a.Identity); err != nil || marked {
		return errors.Join(err, cancelledErr(marked))
	}
	if err := os.Link(tmp.Name(), a.ResultFile); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", adapter.ErrResultExists, a.ResultFile)
		}
		return err
	}
	return nil
}
