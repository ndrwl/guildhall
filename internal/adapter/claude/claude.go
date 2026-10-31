// Package claude is the Claude Code Artisan adapter (R1.22): the minimal M3
// runtime that runs one attempt as a non-interactive Claude Code session.
//
// The adapter's command is `guildhall claude-artisan`, a wrapper that runs
// `claude --print` in the attempt's working directory with an instruction
// naming a copy of the prompt file and a draft result file, both in the model
// directory beside the result file (artisan.ModelDir), the only directory
// Claude may write besides the working directory, then publishes the draft as
// the attempt's result file exactly once. The wrapper, not the model, keeps the
// result protocol of docs/adapters.md: it refuses to start when the result
// file exists or the attempt is cancelled, never replaces a result, honours
// the cancel marker by stopping Claude and publishing nothing, and publishes
// the draft's bytes unvalidated, so a malformed or misidentified draft is
// rejected by the reader as any other result is. It also writes the attempt's
// usage file (adapter.WriteUsage) from Claude Code's JSON output.
//
// It declares no optional capabilities (R1.24): no pre-assigned session or
// resume, no background session, hooks, structured output or remote control.
package claude

import (
	"bytes"
	"cmp"
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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// Subcommand is the guildhall subcommand that runs the Claude wrapper.
const Subcommand = "claude-artisan"

// Program is the Claude Code executable the wrapper runs when none is named:
// looked up on PATH when the attempt starts.
const Program = "claude"

// PermissionMode grants edits in the working and model directories. Other tools
// need permission; inherited settings, hooks and skill-scoped grants may supply it.
// Non-interactive sessions cannot ask, and applicable deny rules still win.
const PermissionMode = "acceptEdits"

// OutputFormat requests incremental events, ending with the session's result
// message carrying usage and cost (ParseUsage).
const OutputFormat = "stream-json"

// ArtisanSettingsFile is the Claude Code settings file the wrapper writes beside the result file for an attempt
// with tool grants and skill policy, even with empty grants, outside the model directory.
const ArtisanSettingsFile = "claude-settings.json"

// Efforts are the effort levels Claude Code's --effort takes, as Claude Code 2.1.309 lists them.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// maxOutputBytes bounds each stream event and the final result retained for usage.
const maxOutputBytes = 4 << 20

// StopGrace is how long the wrapper waits for Claude to exit after asking it
// to stop before killing it.
const StopGrace = 10 * time.Second

// ExitGrace is how long the wrapper lets Claude exit on its own once its draft is a valid result and its final
// result event has arrived, before it stops Claude and publishes the draft. A variable only so tests can shorten it.
var ExitGrace = StopGrace

// QuietLimit is how long the wrapper lets Claude run with an unchanged valid draft and no output at all, no result
// event included, before it stops Claude and publishes the draft. A variable only so tests can shorten it.
var QuietLimit = 3 * time.Minute

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
	// SkillPolicy is the already resolved Director policy; nil uses director-v1.
	SkillPolicy *config.ClaudeSkillPolicy
}

// Capabilities reports the Claude adapter's optional capabilities: none.
func (Adapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

// CheckSettings honours a model Claude Code's --model can take as given, one word that does not start with "-",
// and an effort among Efforts; it refuses anything else with an error wrapping adapter.ErrUnsupportedSetting.
// It does not check that the model exists: Claude Code resolves aliases and names itself, and a session on a model
// it does not know ends with no result.
func (Adapter) CheckSettings(s adapter.Settings) error {
	if s.Model != "" && (strings.HasPrefix(s.Model, "-") || strings.IndexFunc(s.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return fmt.Errorf("%w: claude adapter: model %q is not one word Claude Code's --model takes", adapter.ErrUnsupportedSetting, s.Model)
	}
	if s.Effort != "" && !slices.Contains(Efforts, s.Effort) {
		return fmt.Errorf("%w: claude adapter: effort %q is not one Claude Code's --effort takes: %s", adapter.ErrUnsupportedSetting, s.Effort, strings.Join(Efforts, ", "))
	}
	return nil
}

// Command returns the wrapper invocation for spec: an argument list run in
// spec's working directory with spec's environment, passing spec's settings
// as --model and --effort, each only when set. It refuses an invalid spec,
// settings CheckSettings refuses or an unset Executable, returning no command.
func (a Adapter) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if err := a.CheckSettings(spec.Settings); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("claude adapter: guildhall executable is not set")
	}
	argv := []string{a.Executable, Subcommand,
		"--task", spec.TaskID, "--attempt", spec.AttemptID,
		"--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if spec.Copy != "" {
		argv = append(argv, "--copy", spec.Copy)
	}
	if spec.Profile != "" {
		argv = append(argv, "--profile", spec.Profile)
	}
	if spec.Repository != "" {
		argv = append(argv, "--repo", spec.Repository)
	}
	argv = append(argv, settingsArgs(spec.Settings)...)
	if a.Claude != "" {
		argv = append(argv, "--claude", a.Claude)
	}
	return adapter.Command{Argv: argv, Dir: spec.WorkDir, Env: append([]string(nil), spec.Env...)}, nil
}

// settingsArgs are s as --model and --effort, each only when set: the flags of both the wrapper and Claude Code.
func settingsArgs(s adapter.Settings) []string {
	var args []string
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	return args
}

// Args are the wrapper's command-line arguments.
type Args struct {
	adapter.Identity
	PromptFile string
	ResultFile string
	// Claude is the Claude Code program; empty means Program on PATH.
	Claude  string
	Copy    string
	Profile string
	// Repository is the task's repository id, whose repository grant the effective grant adds.
	Repository string
	// Settings are passed to Claude Code as --model and --effort, each only when set.
	adapter.Settings
	allowedTools []string
}

// ParseArgs parses the wrapper arguments; the prompt and result paths must be absolute
// and distinct, the prompt must be outside the model directory, and the settings must be
// ones CheckSettings honours.
func ParseArgs(argv []string) (Args, error) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var a Args
	fs.StringVar(&a.TaskID, "task", "", "task id")
	fs.StringVar(&a.AttemptID, "attempt", "", "attempt id")
	fs.StringVar(&a.PromptFile, "prompt", "", "prompt file")
	fs.StringVar(&a.ResultFile, "result", "", "result file")
	fs.StringVar(&a.Claude, "claude", "", "Claude Code program")
	fs.StringVar(&a.Copy, "copy", "", "Director copy")
	fs.StringVar(&a.Profile, "profile", "", "profile declared by the copy")
	fs.StringVar(&a.Repository, "repo", "", "repository id listed by the copy")
	fs.StringVar(&a.Model, "model", "", "Claude Code model")
	fs.StringVar(&a.Effort, "effort", "", "Claude Code effort level")
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
	if err := (Adapter{}).CheckSettings(a.Settings); err != nil {
		return Args{}, err
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
	if rel, err := filepath.Rel(ModelDir(a.ResultFile), a.PromptFile); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Args{}, fmt.Errorf("--prompt must not be in the model directory %s", ModelDir(a.ResultFile))
	}
	return a, nil
}

// ModelDir is the directory Claude may write besides the working directory
// for the result file result: artisan.ModelDir beside it.
func ModelDir(result string) string {
	return filepath.Join(filepath.Dir(result), artisan.ModelDir)
}

// ArtisanSettingsPath is the settings file the wrapper writes for the result file result: ArtisanSettingsFile
// beside it.
func ArtisanSettingsPath(result string) string {
	return filepath.Join(filepath.Dir(result), ArtisanSettingsFile)
}

// ArtisanSettings is the Claude Code settings file granting rules: a permissions object whose allow list is
// rules, in order, and nothing else, as JSON with a trailing newline.
func ArtisanSettings(rules []string) []byte {
	if rules == nil {
		rules = []string{}
	}
	type permissions struct {
		Allow []string `json:"allow"`
	}
	data, _ := json.Marshal(struct {
		Permissions permissions `json:"permissions"`
	}{permissions{Allow: rules}})
	return append(data, '\n')
}

// DraftFile is the draft result Claude writes in the model directory for the
// result file result.
func DraftFile(result string) string {
	return filepath.Join(ModelDir(result), artisan.ResultFile)
}

// PromptCopy is the copy of the prompt file Claude reads in the model
// directory for the result file result.
func PromptCopy(result string) string {
	return filepath.Join(ModelDir(result), render.PromptFile)
}

// Instruction is the prompt Claude Code is given for attempt a: read and
// follow the prompt copy, then write the version 1 result for a's identity to
// the draft file, once, as the last action.
func Instruction(a Args) string {
	tmpl, _ := json.Marshal(struct {
		Schema    string `json:"schema"`
		Version   int    `json:"version"`
		TaskID    string `json:"task_id"`
		AttemptID string `json:"attempt_id"`
	}{adapter.ResultSchema, adapter.ResultVersion, a.TaskID, a.AttemptID})
	return strings.Join([]string{
		fmt.Sprintf("You are a Guildhall Artisan working on task %s, attempt %s, in the current directory.", a.TaskID, a.AttemptID),
		"Read the prompt file below and follow it; the inputs and skills directories it names are beside it.",
		"",
		"Prompt file: " + PromptCopy(a.ResultFile),
		"Result file: " + DraftFile(a.ResultFile),
		"",
		"When you have finished, or cannot go on, write your result to the result file above, once, as your last action, and nowhere else.",
		"Use the prompt's Result template, including its required outputs entries. It is one JSON object with these fixed identity fields:",
		string(tmpl),
		`status is "succeeded" when the prompt's work is done and "failed" when it is not; summary says what was done, or why it failed.`,
		`outputs lists each required file as {"path": "<relative path with / separators in the current directory>", "description": "<optional>"}. For succeeded file-producing stages it must not be empty; write the required files in the work directory before writing the result. A verdict in summary does not replace a review outcome file.`,
		`If a custom prompt has no Result template, add status, summary and outputs to the fixed identity fields above, following the same required-file rules.`,
		`For a build or other stage whose output is a code head, commit all tracked work and use outputs: []; Guildhall records your final Git HEAD directly, with no head file needed. Uncommitted tracked changes are refused; untracked notes alone are allowed.`,
		"Use no other fields, and do not change task_id, attempt_id, schema or version.",
		"",
	}, "\n")
}

// ClaudeArgs are the arguments after the program for attempt a: print mode
// with Instruction as the prompt, OutputFormat, PermissionMode, a's model and
// effort as --model and --effort, each only when set, the settings file at
// ArtisanSettingsPath on every attempt, and only the model directory
// added for tool access.
func ClaudeArgs(a Args) []string {
	args := []string{"--print", Instruction(a), "--output-format", OutputFormat, "--verbose", "--permission-mode", PermissionMode}
	args = append(args, settingsArgs(a.Settings)...)
	args = append(args, "--settings", ArtisanSettingsPath(a.ResultFile))
	return append(args, "--add-dir", ModelDir(a.ResultFile))
}

// ParseUsage reads the usage Claude Code reports in its print-mode JSON output, the session's result message:
// its usage object's input, output, cache creation and cache read tokens are measured, and its total_cost_usd,
// which Claude Code prices from those tokens, is an estimate. A message without a usage object has unknown
// tokens, and one without total_cost_usd an unknown cost. Output that is not one JSON object is an error.
func ParseUsage(output []byte) (adapter.Usage, error) {
	msg, err := parseResultMessage(output)
	if err != nil {
		return adapter.Usage{}, err
	}
	u := adapter.UnknownUsage
	if msg.Usage != nil {
		u.Tokens = adapter.Measured
		u.InputTokens, u.OutputTokens = msg.Usage.InputTokens, msg.Usage.OutputTokens
		u.CacheCreationInputTokens, u.CacheReadInputTokens = msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens
	}
	if msg.TotalCostUSD != nil {
		u.Cost, u.CostUSD = adapter.Estimated, *msg.TotalCostUSD
	}
	if err := u.Validate(); err != nil {
		return adapter.Usage{}, fmt.Errorf("claude result message: %w", err)
	}
	return u, nil
}

// Denials are the tool uses Claude Code's print mode denied, from its result message's permission_denials, each
// named as its tool with the shell command or file it asked for, such as `Bash(go test ./...)`, or the tool alone.
// Output that is not a result message has none.
func Denials(output []byte) []string {
	msg, err := parseResultMessage(output)
	if err != nil {
		return nil
	}
	var denials []string
	for _, d := range msg.PermissionDenials {
		subject := cmp.Or(d.ToolInput.Command, d.ToolInput.FilePath, d.ToolInput.NotebookPath)
		if subject == "" {
			denials = append(denials, d.ToolName)
		} else {
			denials = append(denials, d.ToolName+"("+subject+")")
		}
	}
	return denials
}

// resultMessage is the part of Claude Code's print-mode JSON result message the wrapper reads.
type resultMessage struct {
	Result            *string `json:"result"`
	PermissionDenials []struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command      string `json:"command"`
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		} `json:"tool_input"`
	} `json:"permission_denials"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// parseResultMessage decodes output as exactly one JSON object.
func parseResultMessage(output []byte) (resultMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(output))
	var msg resultMessage
	if err := dec.Decode(&msg); err != nil {
		return resultMessage{}, fmt.Errorf("claude output is not a JSON result message: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return resultMessage{}, errors.New("claude output has more than one JSON value")
	}
	return msg, nil
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

// Run is the wrapper. It refuses, starting nothing, settings CheckSettings
// refuses, a cancelled attempt (adapter.ErrAttemptCancelled) or an existing result file
// (adapter.ErrResultExists); it replaces the model directory with one holding
// only the prompt copy and the inputs and skills directories beside the prompt
// file, so a stale draft is never published, writes ArtisanSettings at
// ArtisanSettingsPath with the resolved skill policy on every attempt, then runs Claude in the
// current directory with ClaudeArgs, no standard input, readable assistant/tool
// lines on stdout as events arrive, and its error output on stderr. An interrupt, the end of ctx, or the attempt's cancel
// marker asks Claude to stop (an interrupt on Unix, termination on Windows),
// killing it after StopGrace, and publishes nothing; the marker is not an
// error. A Claude still running after a valid draft is stopped the same way
// (watchResult), but its draft is then published as for an exit. Once Claude
// exits, the draft's bytes are published at the result
// file, never replacing one, unless the attempt was cancelled meanwhile; no
// draft is ErrNoDraft. Before publishing, Run writes the usage Claude's JSON
// final result event reports (ParseUsage) as the attempt's usage file, unknown
// when no valid final event is available, and passes the session's result text to stdout; a
// stopped session writes no usage file. A failure to write the usage file
// does not stop the draft being published; it is returned as an error after.
// Claude's exit status does not decide completion: a failing exit after a
// draft is published is still returned as an error.
func Run(ctx context.Context, a Args, stdout, stderr io.Writer) error {
	if err := (Adapter{}).CheckSettings(a.Settings); err != nil {
		return err
	}
	var err error
	a.allowedTools, err = ProfileTools(a.Copy, a.Profile, a.Repository)
	if err != nil {
		return fmt.Errorf("claude adapter: profile: %w", err)
	}
	if a.allowedTools, err = withBoundTaskGrant(a, a.allowedTools, stderr); err != nil {
		return fmt.Errorf("claude adapter: task grant: %w", err)
	}
	if marked, err := adapter.Cancelled(a.ResultFile, a.Identity); err != nil || marked {
		return errors.Join(err, cancelledErr(marked))
	}
	if _, err := os.Lstat(a.ResultFile); err == nil {
		return fmt.Errorf("%w: %s", adapter.ErrResultExists, a.ResultFile)
	} else if !errors.Is(err, fs.ErrNotExist) {
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
	policy, err := attemptPolicy(a.Copy, a.Profile)
	if err != nil {
		return fmt.Errorf("claude adapter: policy: %w", err)
	}
	if err := checkProgramPolicy(ctx, path, policy); err != nil {
		return err
	}
	if err := prepareModelDir(a); err != nil {
		return fmt.Errorf("claude adapter: model directory: %w", err)
	}
	if err := os.WriteFile(ArtisanSettingsPath(a.ResultFile), ArtisanPolicySettings(a.allowedTools, policy), 0o600); err != nil {
		return fmt.Errorf("claude adapter: settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(a.ResultFile), SkillPolicyFile), policy.Receipt(), 0o600); err != nil {
		return fmt.Errorf("claude adapter: policy receipt: %w", err)
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt)
	defer stopSignals()
	ctx, cancel := watchMarker(ctx, a)
	defer cancel()
	ctx, stopReady := context.WithCancelCause(ctx)
	defer stopReady(nil)
	cmd := exec.CommandContext(ctx, path, ClaudeArgs(a)...)
	output := &limitedBuffer{limit: maxOutputBytes}
	events, closeEvents := openEvents(a.ResultFile)
	defer closeEvents()
	stream := &readableStream{out: stdout, events: events, result: output}
	act := newActivity()
	errOut := &lockedWriter{w: stderr}
	cmd.Stdout, cmd.Stderr = act.writer(stream), act.writer(errOut)
	cmd.Cancel = func() error { return stop(cmd.Process) }
	cmd.WaitDelay = StopGrace
	endWatch := watchResult(ctx, stopReady, a, stream, act, errOut)
	runErr := cmd.Run()
	endWatch()
	stream.finish()
	events.Write(workbench.End(exitStatus(runErr)))
	readyStop := errors.Is(context.Cause(ctx), errResultReady)
	if ctx.Err() != nil && !readyStop {
		if errors.Is(context.Cause(ctx), errMarked) {
			_, _ = fmt.Fprintf(stderr, "guildhall: %s: cancel marker present; Claude stopped without a result\n", Subcommand)
			return nil
		}
		return fmt.Errorf("claude adapter: stopped without a result: %w", context.Cause(ctx))
	}
	if readyStop {
		// The wrapper stopped Claude itself, after its result: the stop's exit status is not Claude's.
		runErr = nil
	}
	var usageErr error
	if err := report(a, output, stdout, stderr); err != nil {
		usageErr = fmt.Errorf("claude adapter: usage: %w", err)
	}
	if err := publish(DraftFile(a.ResultFile), a); err != nil {
		return errors.Join(err, runErr, usageErr)
	}
	if runErr != nil {
		return errors.Join(fmt.Errorf("claude adapter: Claude exited after writing its result: %w", runErr), usageErr)
	}
	return usageErr
}

// WorkbenchPath is the Workbench event file the wrapper appends for the attempt whose result file is result:
// workbench.File beside it, outside the model directory.
func WorkbenchPath(result string) string {
	return filepath.Join(filepath.Dir(result), workbench.File)
}

// openEvents opens the attempt's Workbench event file for append, and returns its writer and a closer. A file
// that cannot be opened writes no events, and is never an error: the event file is presentation only.
func openEvents(result string) (*workbench.Writer, func()) {
	f, err := os.OpenFile(WorkbenchPath(result), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, func() {}
	}
	return workbench.NewWriter(f), func() { _ = f.Close() }
}

// exitStatus is the exit status err reports for Claude's process: 0 for none, the process's status, or -1 when
// it has none, as for a process killed by a signal or one that never ran.
func exitStatus(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	return -1
}

// withBoundTaskGrant adds to rules, last, the task grant a's binding records (BoundTaskGrant), unless it is revoked,
// naming on stderr what it added or dropped. An attempt with no profile, such as a hand-started tab, takes none.
func withBoundTaskGrant(a Args, rules []string, stderr io.Writer) ([]string, error) {
	if a.Profile == "" {
		return rules, nil
	}
	// A copy without a store can hold no binding, so no grant: fewer rules, never more, but said rather than silent.
	if a.Copy != "" {
		if _, err := os.Stat(filepath.Join(a.Copy, copyStoreFile)); errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(stderr, "guildhall: %s: copy %s has no %s; no task grant is read\n", Subcommand, a.Copy, copyStoreFile)
		}
	}
	g, revoked, err := BoundTaskGrant(a.Copy, a.Repository, a.TaskID, a.AttemptID)
	switch {
	case err != nil || g == nil:
		return rules, err
	case revoked:
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: task grant %s is revoked; its rules are not granted\n", Subcommand, g.ID)
		return rules, nil
	}
	_, _ = fmt.Fprintf(stderr, "guildhall: %s: task grant %s adds %s\n", Subcommand, g.ID, strings.Join(g.Rules, ", "))
	var grants []Grant
	for _, rule := range rules {
		grants = append(grants, Grant{Rule: rule})
	}
	return GrantRules(WithTaskGrant(grants, g.Rules)), nil
}

// report writes the usage Claude's output reports as a's usage file, unknown when the output reports none, names
// on stderr each tool use print mode denied, and passes the session's result text to stdout, or the output itself
// when it is not a result message.
func report(a Args, output *limitedBuffer, stdout, stderr io.Writer) error {
	if denied := Denials(output.buf.Bytes()); len(denied) > 0 {
		plural := map[bool]string{true: "s"}[len(denied) > 1]
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: Claude was denied %d tool use%s: %s; acceptEdits grants edits only; inherited settings, hooks and skill-scoped grants may allow more; deny rules remain authoritative (docs/adapters.md#artisan-permissions)\n",
			Subcommand, len(denied), plural, strings.Join(denied, ", "))
	}
	u, err := ParseUsage(output.buf.Bytes())
	if err != nil || output.truncated {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: usage unknown: %v\n", Subcommand, errors.Join(err, output.err()))
		u = adapter.UnknownUsage
	}
	if msg, err := parseResultMessage(output.buf.Bytes()); err == nil && msg.Result != nil {
		_, _ = fmt.Fprintln(stdout, *msg.Result)
	} else {
		_, _ = stdout.Write(output.buf.Bytes())
	}
	return adapter.WriteUsage(a.ResultFile, u)
}

// limitedBuffer keeps the first limit bytes written to it and discards the rest, so a runaway output neither
// grows without bound nor blocks Claude.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room < len(p) {
		b.truncated = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) err() error {
	if b.truncated {
		return fmt.Errorf("output longer than %d bytes", b.limit)
	}
	return nil
}

// prepareModelDir replaces a's model directory with one holding the prompt
// copy and the inputs and skills directories beside the prompt file.
func prepareModelDir(a Args) error {
	dir := ModelDir(a.ResultFile)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(PromptCopy(a.ResultFile), data, 0o600); err != nil {
		return err
	}
	for _, name := range []string{render.InputsDir, render.SkillsDir} {
		src := filepath.Join(filepath.Dir(a.PromptFile), name)
		st, err := os.Stat(src)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !st.IsDir() {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.CopyFS(filepath.Join(dir, name), os.DirFS(src)); err != nil {
			return err
		}
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

// errResultReady ends Claude's session when Claude outlives a valid draft (watchResult); unlike the other causes
// of a stop, the draft is still published.
var errResultReady = errors.New("claude still running after its result")

// activity records when Claude last wrote anything, on its output or its error output.
type activity struct {
	start time.Time
	last  atomic.Int64 // time.Duration since start
}

func newActivity() *activity { return &activity{start: time.Now()} }

// since is how long ago Claude last wrote, or started when it has written nothing.
func (a *activity) since(now time.Time) time.Duration {
	return now.Sub(a.start) - time.Duration(a.last.Load())
}

// writer is w, recording each write as activity.
func (a *activity) writer(w io.Writer) io.Writer { return activityWriter{w: w, a: a} }

type activityWriter struct {
	w io.Writer
	a *activity
}

func (w activityWriter) Write(p []byte) (int, error) {
	w.a.last.Store(int64(time.Since(w.a.start)))
	return w.w.Write(p)
}

// lockedWriter serializes writes to w, which Claude's error output and the wrapper's own lines share.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// watchResult stops a Claude that outlives its result, ending ctx through stopClaude with cause errResultReady, and
// returns a function that ends the watch and waits for it. Every markerPoll it reads a's draft, which counts only
// once it is a valid result for a (readyDraft), so a malformed or misidentified draft never cuts Claude off. Claude
// is stopped when it is still running ExitGrace after both the draft is ready and its final result event has arrived,
// or when the ready draft has not changed and Claude has written nothing for QuietLimit, in case Claude Code writes
// its result event only as it exits. The stop is named on stderr with its trigger.
func watchResult(ctx context.Context, stopClaude context.CancelCauseFunc, a Args, stream *readableStream, act *activity, stderr io.Writer) func() {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(markerPoll)
		defer t.Stop()
		var draft []byte
		var changed, both time.Time // when the ready draft last changed, and when it and the result event were both seen
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-t.C:
			}
			now := time.Now()
			data, ready := readyDraft(a)
			if !ready {
				draft, both = nil, time.Time{}
				continue
			}
			if !bytes.Equal(data, draft) {
				draft, changed, both = data, now, time.Time{}
			}
			if both.IsZero() && stream.sawResult.Load() {
				both = now
			}
			trigger, waited := "", time.Duration(0)
			switch {
			case !both.IsZero() && now.Sub(both) >= ExitGrace:
				trigger, waited = "result-event", ExitGrace
			case now.Sub(changed) >= QuietLimit && act.since(now) >= QuietLimit:
				trigger, waited = "quiet", QuietLimit
			default:
				continue
			}
			_, _ = fmt.Fprintf(stderr, "guildhall: %s: Claude still running %s after its result (trigger: %s); stopping it\n", Subcommand, waited, trigger)
			stopClaude(errResultReady)
			return
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// readyDraft returns a's draft when it is a valid result for a, no larger than a result may be.
func readyDraft(a Args) ([]byte, bool) {
	f, err := os.Open(DraftFile(a.ResultFile))
	if err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, adapter.MaxResultBytes+1))
	_ = f.Close()
	if err != nil || len(data) > adapter.MaxResultBytes {
		return nil, false
	}
	if _, err := adapter.ParseResult(data, a.Identity); err != nil {
		return nil, false
	}
	return data, true
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
