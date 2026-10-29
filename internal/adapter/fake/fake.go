// Package fake is the Artisan adapter for tests and no-model demos (R1.22). It declares no
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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/atomicfile"
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

// CheckSettings honours every model and effort: the fake runs no model, and records the settings it was given
// (SettingsFile) so tests can see what reached the runtime.
func (Adapter) CheckSettings(adapter.Settings) error { return nil }

// SettingsFile is the record, beside the result file, of the settings a fake worker was started with: the JSON
// of its adapter.Settings, written before the worker reads its prompt.
const SettingsFile = "fake-settings.json"

// RecordedSettings returns the settings the fake worker for the result file result recorded (SettingsFile).
func RecordedSettings(result string) (adapter.Settings, error) {
	data, err := atomicfile.Read(filepath.Join(filepath.Dir(result), SettingsFile))
	if err != nil {
		return adapter.Settings{}, err
	}
	var s adapter.Settings
	err = json.Unmarshal(data, &s)
	return s, err
}

// Command returns the fake Artisan invocation for spec, passing spec's
// settings as --model and --effort, each only when set. It refuses a spec
// referencing a profile that grants allowed tools, since the fake grants none.
func (a Adapter) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("fake adapter: guildhall executable is not set")
	}
	rules, err := adapter.ProfileTools(spec.Copy, spec.Profile)
	if err != nil {
		return adapter.Command{}, err
	}
	if len(rules) > 0 {
		return adapter.Command{}, errors.New("fake adapter: it grants no allowed tools")
	}
	argv := []string{a.Executable, Subcommand,
		"--task", spec.TaskID, "--attempt", spec.AttemptID,
		"--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	if spec.Effort != "" {
		argv = append(argv, "--effort", spec.Effort)
	}
	return adapter.Command{Argv: argv, Dir: spec.WorkDir, Env: append([]string(nil), spec.Env...)}, nil
}

// Roles a fake Artisan process plays.
const (
	RoleWorker     = "worker"
	RoleGrandchild = "grandchild"
)

// Main runs `guildhall fake-artisan` with args, the arguments after the
// subcommand, and returns its exit status: 0 when Run succeeds, 1 when it
// fails and 2 for arguments ParseArgs refuses. stdin is the interactive
// directive's input; Run handles interrupts itself.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a, err := ParseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: fake-artisan: %v\n", err)
		return 2
	}
	a.Stdin = stdin
	if err := Run(context.Background(), a, stdout, Sleep); err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: fake-artisan: %v\n", err)
		return 1
	}
	return 0
}

// Args are the fake Artisan's command-line arguments.
type Args struct {
	adapter.Identity
	PromptFile     string
	ResultFile     string
	ControlAddress string
	ControlToken   string
	// Role is "worker", the Artisan the adapter starts, or "grandchild", a
	// process the worker starts for the grandchildren directive.
	Role string
	// Stdin is what the interactive directive echoes; nil reads nothing.
	Stdin io.Reader
	// Settings are the --model and --effort the worker records (SettingsFile).
	adapter.Settings
}

// ParseArgs parses `--task --attempt --prompt --result`, all required, and the optional `--model` and `--effort`.
func ParseArgs(argv []string) (Args, error) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var a Args
	fs.StringVar(&a.TaskID, "task", "", "task id")
	fs.StringVar(&a.AttemptID, "attempt", "", "attempt id")
	fs.StringVar(&a.PromptFile, "prompt", "", "prompt file")
	fs.StringVar(&a.ResultFile, "result", "", "result file")
	fs.StringVar(&a.ControlAddress, "worker-control", "", "attempt owner loopback address")
	fs.StringVar(&a.ControlToken, "worker-token", "", "attempt ownership token")
	fs.StringVar(&a.Role, "role", RoleWorker, "worker or grandchild")
	fs.StringVar(&a.Model, "model", "", "model to record")
	fs.StringVar(&a.Effort, "effort", "", "effort to record")
	if err := fs.Parse(argv); err != nil {
		return Args{}, err
	}
	if fs.NArg() > 0 {
		return Args{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if (a.ControlAddress == "") != (a.ControlToken == "") {
		return Args{}, errors.New("--worker-control and --worker-token go together")
	}
	if a.Role != RoleWorker && a.Role != RoleGrandchild {
		return Args{}, fmt.Errorf("--role %q must be %s or %s", a.Role, RoleWorker, RoleGrandchild)
	}
	if a.PromptFile == "" || a.ResultFile == "" {
		return Args{}, errors.New("--task, --attempt, --prompt and --result are required")
	}
	if err := a.Validate(); err != nil {
		return Args{}, err
	}
	return a, nil
}

// MaxGrandchildren bounds the grandchildren directive.
const MaxGrandchildren = 8

// MaxLifetime bounds the lifetime directive; DefaultLifetime is the lifetime
// of a prompt that sets none.
const (
	MaxLifetime     = time.Hour
	DefaultLifetime = 15 * time.Minute
)

// When the result is written: after the delay, only once the cancel marker
// is present (a late result), or never.
const (
	resultDelay       = "delay"
	resultAfterCancel = "after-cancel"
	resultNever       = "never"
)

// directives is the fake behaviour a prompt selects.
type directives struct {
	outcome adapter.Status
	fault   string
	delay   time.Duration

	grandchildren   int
	native          bool
	ignoreCancel    bool
	ignoreInterrupt bool
	interactive     bool
	result          string
	lifetime        time.Duration
	inventory       string
	// output is one file to write into the working directory and list as the result's output.
	output, text string
}

var faults = map[string]bool{"none": true, "no-result": true, "malformed": true, "wrong-identity": true, "exit-error": true}

func parseDirectives(prompt []byte) (directives, error) {
	d := directives{outcome: adapter.StatusSucceeded, fault: "none", result: resultDelay, lifetime: DefaultLifetime}
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
		case "grandchildren":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > MaxGrandchildren {
				return d, fmt.Errorf("grandchildren %q must be a number from 0 to %d", value, MaxGrandchildren)
			}
			d.grandchildren = n
		case "grandchild-cmd":
			if value != "fake" && value != "native" {
				return d, fmt.Errorf("grandchild-cmd %q must be fake or native", value)
			}
			d.native = value == "native"
		case "ignore-cancel", "ignore-interrupt", "interactive":
			if value != "true" && value != "false" {
				return d, fmt.Errorf("%s %q must be true or false", key, value)
			}
			on := value == "true"
			switch key {
			case "ignore-cancel":
				d.ignoreCancel = on
			case "ignore-interrupt":
				d.ignoreInterrupt = on
			default:
				d.interactive = on
			}
		case "result":
			if value != resultDelay && value != resultAfterCancel && value != resultNever {
				return d, fmt.Errorf("result %q must be delay, after-cancel or never", value)
			}
			d.result = value
		case "lifetime":
			l, err := time.ParseDuration(value)
			if err != nil || l <= 0 || l > MaxLifetime {
				return d, fmt.Errorf("lifetime %q must be a positive duration up to %s", value, MaxLifetime)
			}
			d.lifetime = l
		case "inventory":
			if !filepath.IsAbs(value) {
				return d, fmt.Errorf("inventory %q must be an absolute directory", value)
			}
			d.inventory = value
		case "output":
			path, text, ok := strings.Cut(value, ":")
			if !ok || !filepath.IsLocal(path) || filepath.IsAbs(path) || strings.Contains(path, `\`) || path != filepath.ToSlash(filepath.Clean(path)) {
				return d, fmt.Errorf("output %q must be <relative slash-separated path>:<text>", value)
			}
			d.output, d.text = path, text
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

// ReportedUsage is the usage the fake reports for every attempt whose work ends, result or not: fixed numbers, so
// tests of what Guildhall records are deterministic. Its tokens are marked measured and its cost estimated, as the
// Claude adapter reports them.
var ReportedUsage = adapter.Usage{Tokens: adapter.Measured, InputTokens: 100, OutputTokens: 20, CacheCreationInputTokens: 300, CacheReadInputTokens: 4000, Cost: adapter.Estimated, CostUSD: 0.01}

// Run is the fake Artisan: a worker records its settings (SettingsFile), then
// it reads the prompt, applies its directives, and writes the result file. Progress lines go to out so a Herdr tab shows them.
// Once its work ends, before any result, it writes ReportedUsage as the
// attempt's usage file (adapter.WriteUsage); work ended by an interrupt or the
// cancel marker reports none.
//
// Run handles interrupts itself: one ends the work without a result unless
// the prompt says ignore-interrupt. Unless it says ignore-cancel, the
// attempt's cancel marker (adapter.Cancelled) ends the work without a result;
// with result=after-cancel the marker instead makes it publish a late result.
// With grandchildren it starts that many processes, each the fake in role
// grandchild or a native command, and with inventory every fake process
// records its identity there. The lifetime bounds every process of the tree.
func Run(ctx context.Context, a Args, out io.Writer, sleep func(context.Context, time.Duration) error) error {
	if a.ControlAddress != "" {
		if err := adapter.JoinOwnedWorker(ctx, a.ControlAddress, a.ControlToken, a.Identity, a.ResultFile); err != nil {
			return fmt.Errorf("worker ownership handshake: %w", err)
		}
	}
	if a.Role != RoleGrandchild {
		data, err := json.Marshal(a.Settings)
		if err != nil {
			return err
		}
		if err := atomicfile.Write(filepath.Join(filepath.Dir(a.ResultFile), SettingsFile), data); err != nil {
			return fmt.Errorf("settings record: %w", err)
		}
	}
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
	if a.Role == RoleGrandchild {
		return grandchild(ctx, a, d, sleep)
	}
	// Progress lines are for a watching human; failing to show them does not
	// change the result.
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, "fake artisan: "+format+"\n", args...) }
	say("task %s attempt %s", a.TaskID, a.AttemptID)
	say("read prompt %s (%d bytes)", a.PromptFile, len(prompt))
	play, err := playScenario(a, d, prompt)
	if err != nil {
		say("%v", err)
		return errors.Join(err, writeScenarioFailure(a, err))
	}
	if play != nil {
		say("scenario: %s", play.summary)
	}
	ctx, stop := handleInterrupt(ctx, d)
	defer stop()
	if err := startTree(a, d); err != nil {
		return err
	}
	if d.interactive && a.Stdin != nil {
		go echo(a.Stdin, out)
	}
	ctx, cancel := watchMarker(ctx, a, !d.ignoreCancel || d.result == resultAfterCancel)
	defer cancel()
	work := d.lifetime
	if d.result == resultDelay {
		work = min(d.delay, d.lifetime)
	}
	if work > 0 {
		say("working for %s", work)
		if err := sleep(ctx, work); err != nil {
			if !errors.Is(context.Cause(ctx), errMarked) {
				return err
			}
			if d.result == resultAfterCancel {
				say("cancel marker present; publishing a late result")
				result, err := newResult(a, d, prompt, play)
				if err != nil {
					return err
				}
				return publishLate(a, result)
			}
			say("cancel marker present; stopping without a result")
			return nil
		}
	}
	if err := adapter.WriteUsage(a.ResultFile, ReportedUsage); err != nil {
		say("usage not reported: %v", err)
	} else {
		say("reported usage in %s", adapter.UsageFile(a.ResultFile))
	}
	if d.result != resultDelay || d.delay > d.lifetime {
		say("lifetime %s over without a result", d.lifetime)
		return nil
	}
	result, err := newResult(a, d, prompt, play)
	if errors.As(err, new(errScenario)) {
		say("%v", err)
		return errors.Join(err, writeScenarioFailure(a, err))
	} else if err != nil {
		return err
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

// newResult is the result the prompt asks for, or the scenario's when play is not nil.
func newResult(a Args, d directives, prompt []byte, play *scenarioOutcome) (adapter.Result, error) {
	result := adapter.Result{
		Identity: a.Identity,
		Status:   d.outcome,
		Summary:  fmt.Sprintf("fake artisan read %d bytes of prompt, sha256 %x", len(prompt), sha256.Sum256(prompt)),
		Outputs:  []adapter.Output{},
	}
	if play != nil {
		outputs, err := play.write(a.ResultFile)
		if err != nil {
			return adapter.Result{}, errScenario{err}
		}
		result.Status, result.Summary, result.Outputs = play.status, play.summary, outputs
		return result, nil
	}
	if d.output != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.FromSlash(d.output)), 0o755); err != nil {
			return adapter.Result{}, err
		}
		if err := atomicfile.Write(filepath.FromSlash(d.output), []byte(d.text+"\n")); err != nil {
			return adapter.Result{}, err
		}
		result.Outputs = []adapter.Output{{Path: d.output}}
	}
	return result, nil
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
