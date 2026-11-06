package codex

import (
	"bytes"
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/grant"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/secrets"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// Sandbox is the Codex sandbox every attempt runs in: nothing a command the model runs can write, and no network.
const Sandbox = "read-only"

// MinVersion is the oldest codex-cli the wrapper runs: the version d108's evidence pins
// (docs/evidence/codex-hooks-2026-11-03.txt, docs/evidence/codex-exec-policy-2026-11-03.txt).
var MinVersion = [3]int{0, 163, 0}

// Switches are the arguments of every `codex exec` the wrapper runs that switch off the Codex tools that would
// reach past the read-only sandbox: the ChatGPT apps MCP server, web search and the user's MCP servers, with network
// access off too. The apps and web-search switches are pinned by docs/evidence/codex-hooks-2026-11-03.txt (H2, H8).
var Switches = []string{
	"--disable", "apps",
	"-c", `web_search="disabled"`,
	"-c", "mcp_servers={}",
	"-c", "sandbox_workspace_write.network_access=false",
}

// ExecFlags are the `codex exec` options the wrapper passes on every run; it refuses an installed Codex whose
// `codex exec --help` does not list each of them.
var ExecFlags = []string{"--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "--cd", "--config", "--disable", "--model"}

// OutputSchemaFlag constrains Codex's final message to a JSON Schema; the wrapper passes it when the installed
// Codex's `codex exec --help` lists it.
const OutputSchemaFlag = "--output-schema"

// Files the wrapper writes beside the result file.
const (
	// OutputSchemaFile is the final message's JSON Schema, given to OutputSchemaFlag.
	OutputSchemaFile = "codex-output-schema.json"
	// FinalMessageFile is Codex's final message as it arrived, kept for a refused answer to be read.
	FinalMessageFile = "codex-final-message.txt"
)

// probeTimeout bounds each of the wrapper's checks of the installed Codex (version, login, help).
const probeTimeout = 30 * time.Second

// StopGrace is how long the wrapper waits for Codex to exit after asking it to stop before killing it.
const StopGrace = 10 * time.Second

// markerPoll is how often Run looks for the cancel marker.
const markerPoll = 50 * time.Millisecond

// maxLineBytes bounds each --json event the wrapper reads; a longer line is skipped.
const maxLineBytes = 4 << 20

// Answer is a step's Codex answer contract. The read-only model writes nothing: it returns the step's answer
// document in its final message, and the wrapper, outside the sandbox, writes it as File in the working directory
// and names it as the result's one output.
type Answer struct {
	// File is the document's path in the working directory, with / separators.
	File string
	// Schema is the document's JSON Schema, in the strict form Codex's OutputSchemaFlag takes.
	Schema json.RawMessage
	// Check refuses a document that breaks the step's contract. promptDir is the directory of the attempt's prompt
	// file, beside which its inputs are.
	Check func(promptDir string, doc []byte) error
	// Outcomes lets a failed final message carry a structured result outcome, already-delivered or nothing-to-do
	// (docs/adapters.md#result-file), as only research, plan and build steps may (workflow.ReportsOutcome); the
	// instruction and the output schema then name the field, and the wrapper carries it into the result.
	Outcomes bool
}

// Answers are the answer contracts by workflow step, the step of the attempt id (artisan.ParseAttemptID). A step
// with none is refused: only a registered step runs on Codex.
type Answers map[string]Answer

// Args are the wrapper's command-line arguments.
type Args struct {
	adapter.Identity
	PromptFile string
	ResultFile string
	// Codex is the Codex CLI program; empty means Program on PATH.
	Codex   string
	Copy    string
	Profile string
	// Repository is the task's repository id, whose repository grant the effective grant adds.
	Repository string
	// Settings are passed to Codex as -m and -c model_reasoning_effort, each only when set.
	adapter.Settings
}

// ParseArgs parses the wrapper arguments: the prompt and result paths must be absolute and distinct, the Codex
// program absolute when named, and the settings ones CheckSettings honours.
func ParseArgs(argv []string) (Args, error) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var a Args
	fs.StringVar(&a.TaskID, "task", "", "task id")
	fs.StringVar(&a.AttemptID, "attempt", "", "attempt id")
	fs.StringVar(&a.PromptFile, "prompt", "", "prompt file")
	fs.StringVar(&a.ResultFile, "result", "", "result file")
	fs.StringVar(&a.Codex, "codex", "", "Codex CLI program")
	fs.StringVar(&a.Copy, "copy", "", "Director copy")
	fs.StringVar(&a.Profile, "profile", "", "profile declared by the copy")
	fs.StringVar(&a.Repository, "repo", "", "repository id listed by the copy")
	fs.StringVar(&a.Model, "model", "", "Codex model")
	fs.StringVar(&a.Effort, "effort", "", "Codex reasoning effort")
	if err := fs.Parse(argv); err != nil {
		return Args{}, err
	}
	if fs.NArg() > 0 {
		return Args{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.PromptFile == "" || a.ResultFile == "" {
		return Args{}, errors.New("--task, --attempt, --prompt and --result are required")
	}
	if a.Codex != "" && !filepath.IsAbs(a.Codex) {
		return Args{}, fmt.Errorf("--codex %q must be an absolute path", a.Codex)
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
	return a, nil
}

// Instruction is the prompt Codex is given for attempt a under answer contract ans: read and follow the prompt
// file, write nothing, and return the result fields and the answer document as one JSON object, the final message.
func Instruction(a Args, ans Answer) string {
	lines := []string{
		fmt.Sprintf("You are a Guildhall Artisan working on task %s, attempt %s, in the current directory.", a.TaskID, a.AttemptID),
		"Read the prompt file below and follow it; the inputs and skills directories it names are beside it.",
		"",
		"Prompt file: " + a.PromptFile,
		"",
		"You run in a read-only sandbox: you cannot create, change or delete any file, so do not try.",
		fmt.Sprintf("Where the prompt asks you to write `%s` or a result file, do not write them: return them in your final message, and Guildhall writes them for you.", ans.File),
		"Your final message is one JSON object and nothing else, with exactly these fields:",
		`- "status": "succeeded" when the prompt's work is done, "failed" when it is not;`,
		`- "summary": one or more sentences saying what was done, or why it failed;`,
		fmt.Sprintf(`- "answer": the content of %s, the JSON object the prompt describes, when status is "succeeded"; null when it is "failed".`, ans.File),
	}
	if ans.Outcomes {
		lines = append(lines, `- "outcome": null, unless status is "failed" because a merged pull request already shipped the change or the task needs no change: then the outcome object the prompt's Result template describes, with kind and, null where unused, by_pr, by_slice and reason.`)
	}
	return strings.Join(append(lines, ""), "\n")
}

// OutputSchema is the JSON Schema of the final message for answer contract ans: status, summary and answer, each
// required, answer being ans.Schema or null, and, when ans.Outcomes, outcome, required too in Codex's strict form,
// being an outcome object or null.
func OutputSchema(ans Answer) []byte {
	answer := json.RawMessage(`{"type": "null"}`)
	if len(ans.Schema) > 0 {
		answer = json.RawMessage(`{"anyOf": [` + string(ans.Schema) + `, {"type": "null"}]}`)
	}
	required := []string{"status", "summary", "answer"}
	properties := map[string]any{
		"status":  map[string]any{"type": "string", "enum": []string{string(adapter.StatusSucceeded), string(adapter.StatusFailed)}},
		"summary": map[string]any{"type": "string"},
		"answer":  answer,
	}
	if ans.Outcomes {
		required = append(required, "outcome")
		properties["outcome"] = map[string]any{"anyOf": []any{
			map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"kind", "by_pr", "by_slice", "reason"},
				"properties": map[string]any{
					"kind":     map[string]any{"type": "string", "enum": []string{string(adapter.OutcomeAlreadyDelivered), string(adapter.OutcomeNothingToDo)}},
					"by_pr":    map[string]any{"type": []string{"string", "null"}},
					"by_slice": map[string]any{"type": []string{"integer", "null"}},
					"reason":   map[string]any{"type": []string{"string", "null"}},
				},
			},
			map[string]any{"type": "null"},
		}}
	}
	data, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           properties,
	})
	return append(data, '\n')
}

// ExecArgs are the arguments after the program for attempt a in the working directory workDir under answer
// contract ans: `exec` with --json, --ephemeral, --skip-git-repo-check, the read-only Sandbox, -C workDir, the
// Switches, a's model as -m and effort as -c model_reasoning_effort, each only when set, --output-schema
// schemaFile when it is not empty, and Instruction as the prompt.
func ExecArgs(a Args, ans Answer, workDir, schemaFile string) []string {
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", Sandbox, "-C", workDir}
	args = append(args, Switches...)
	if a.Model != "" {
		args = append(args, "-m", a.Model)
	}
	if a.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+a.Effort)
	}
	if schemaFile != "" {
		args = append(args, OutputSchemaFlag, schemaFile)
	}
	return append(args, Instruction(a, ans))
}

// Main runs `guildhall codex-artisan` with args, the arguments after the subcommand, under the answer contracts
// answers, and returns its exit status: 0 when Run succeeds, 1 when it fails and 2 for arguments ParseArgs refuses.
func Main(args []string, answers Answers, stdout, stderr io.Writer) int {
	a, err := ParseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: %v\n", Subcommand, err)
		return 2
	}
	if err := Run(context.Background(), a, answers, stdout, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: %v\n", Subcommand, err)
		return 1
	}
	return 0
}

// Errors the wrapper names.
var (
	// ErrNoAnswerContract refuses an attempt whose step registers no Codex answer contract.
	ErrNoAnswerContract = errors.New("codex adapter: the step has no Codex answer contract")
	// ErrVersion refuses a Codex older than MinVersion, or one whose version cannot be read.
	ErrVersion = errors.New("codex adapter: codex version refused")
	// ErrNotLoggedIn refuses a Codex with no login.
	ErrNotLoggedIn = errors.New("codex adapter: codex is not logged in")
	// ErrUserMCPServers refuses a user Codex configuration that declares MCP servers, which run outside the sandbox.
	ErrUserMCPServers = errors.New("codex adapter: the user's Codex configuration declares MCP servers")
	// ErrMissingFlag refuses a Codex whose `codex exec --help` lacks a flag the wrapper passes.
	ErrMissingFlag = errors.New("codex adapter: codex exec lacks a flag the wrapper needs")
	// ErrNoAnswer reports a run that ended without a valid answer, so nothing is published.
	ErrNoAnswer = errors.New("codex adapter: no valid answer")
)

// errMarked ends Codex's run when the attempt's cancel marker appears.
var errMarked = errors.New("cancel marker present")

// Run is the wrapper. It refuses, starting nothing, settings CheckSettings refuses, a profile granting any rule, a
// step with no answer contract in answers (ErrNoAnswerContract), a cancelled attempt (adapter.ErrAttemptCancelled)
// or an existing result file (adapter.ErrResultExists); then a Codex older than MinVersion or of unreadable
// version (ErrVersion), one not logged in (ErrNotLoggedIn), a user configuration declaring MCP servers
// (ErrUserMCPServers) and a `codex exec` lacking one of ExecFlags (ErrMissingFlag), naming the version it runs on
// stderr. It then runs Codex in the current directory with ExecArgs, no standard input, readable progress lines on
// stdout and Workbench events as the --json stream arrives (stream), and Codex's error output on stderr.
//
// An interrupt, the end of ctx or the attempt's cancel marker stops Codex, killing it after StopGrace, and
// publishes nothing; the marker is not an error. Once Codex exits, Run writes the usage the stream reports as the
// attempt's usage file, then, only for a run whose last turn completed and whose final message is a valid answer
// (decide), writes the answer document in the working directory and publishes the result exactly once with
// adapter.WriteResult. A failed turn, an error with no completed turn, or a missing or invalid final message
// publishes nothing and returns an error wrapping ErrNoAnswer that names why. Codex's exit status does not decide
// completion: a failing exit after a published result is still returned as an error.
func Run(ctx context.Context, a Args, answers Answers, stdout, stderr io.Writer) error {
	if err := (Adapter{}).CheckSettings(a.Settings); err != nil {
		return err
	}
	// Values the Artisan inherited through its profile's inherit_env are redacted from its output, the Workbench
	// event file, the answer and the result (docs/secrets.md#redaction).
	red := secrets.FromEnv(os.Getenv)
	stdout, stderr = red.Writer(stdout), red.Writer(stderr)
	rules, err := grant.ProfileTools(a.Copy, a.Profile, a.Repository)
	if err != nil {
		return fmt.Errorf("codex adapter: profile: %w", err)
	}
	if len(rules) > 0 {
		return errors.New("codex adapter: it grants no allowed tools; Codex runs read-only")
	}
	step, _, ok := artisan.ParseAttemptID(a.AttemptID)
	ans, registered := answers[step]
	if !ok || !registered {
		return fmt.Errorf("%w: attempt %s", ErrNoAnswerContract, a.AttemptID)
	}
	if marked, err := adapter.Cancelled(a.ResultFile, a.Identity); err != nil || marked {
		return errors.Join(err, cancelledErr(marked))
	}
	if _, err := os.Lstat(a.ResultFile); err == nil {
		return fmt.Errorf("%w: %s", adapter.ErrResultExists, a.ResultFile)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	program := a.Codex
	if program == "" {
		program = Program
	}
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("codex adapter: %w", err)
	}
	version, err := CheckVersion(ctx, path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "guildhall: %s: codex-cli %s at %s\n", Subcommand, version, path)
	if err := CheckLogin(ctx, path); err != nil {
		return err
	}
	if err := CheckUserConfig(); err != nil {
		return err
	}
	schemaFlag, err := CheckFlags(ctx, path)
	if err != nil {
		return err
	}
	schemaFile := ""
	if schemaFlag {
		schemaFile = filepath.Join(filepath.Dir(a.ResultFile), OutputSchemaFile)
		if err := os.WriteFile(schemaFile, OutputSchema(ans), 0o600); err != nil {
			return fmt.Errorf("codex adapter: output schema: %w", err)
		}
	}

	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt)
	defer stopSignals()
	ctx, cancel := watchMarker(ctx, a)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, ExecArgs(a, ans, workDir, schemaFile)...)
	events, closeEvents := openEvents(a.ResultFile, red)
	defer closeEvents()
	// Codex's error output and the decoder's own lines share stderr, from two goroutines.
	errOut := &lockedWriter{w: stderr}
	s := &stream{out: stdout, errOut: errOut, events: events}
	cmd.Stdout, cmd.Stderr = s, errOut
	cmd.Cancel = func() error { return stop(cmd.Process) }
	cmd.WaitDelay = StopGrace
	runErr := cmd.Run()
	s.finish()
	events.Write(workbench.End(exitStatus(runErr)))
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errMarked) {
			_, _ = fmt.Fprintf(stderr, "guildhall: %s: cancel marker present; Codex stopped without a result\n", Subcommand)
			return nil
		}
		return fmt.Errorf("codex adapter: stopped without a result: %w", context.Cause(ctx))
	}
	var usageErr error
	if err := adapter.WriteUsage(a.ResultFile, s.usage()); err != nil {
		usageErr = fmt.Errorf("codex adapter: usage: %w", err)
	}
	if s.final != nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(a.ResultFile), FinalMessageFile), red.Bytes([]byte(*s.final)), 0o600)
	}
	if err := s.failure(); err != nil {
		return errors.Join(err, usageErr)
	}
	r, doc, err := decide(a, ans, s.final)
	if err != nil {
		return errors.Join(err, usageErr)
	}
	r.Summary, doc = red.String(r.Summary), red.Bytes(doc)
	if doc != nil {
		if err := atomicfile.Write(filepath.Join(workDir, filepath.FromSlash(ans.File)), doc); err != nil {
			return errors.Join(fmt.Errorf("codex adapter: answer: %w", err), usageErr)
		}
	}
	if err := adapter.WriteResult(a.ResultFile, r); err != nil {
		return errors.Join(err, usageErr)
	}
	_, _ = fmt.Fprintln(stdout, r.Summary)
	if runErr != nil {
		return errors.Join(fmt.Errorf("codex adapter: Codex exited after its answer: %w", runErr), usageErr)
	}
	return usageErr
}

// finalMessage is the final message's shape (OutputSchema).
type finalMessage struct {
	Status  *adapter.Status `json:"status"`
	Summary *string         `json:"summary"`
	Answer  json.RawMessage `json:"answer"`
	// Outcome is the final message's structured outcome, when its answer contract lets it carry one; null is nil.
	Outcome *finalOutcome `json:"outcome"`
}

// finalOutcome is an outcome as the strict output schema has Codex return it: every field present, null where
// unused.
type finalOutcome struct {
	Kind    adapter.OutcomeKind `json:"kind"`
	ByPR    *string             `json:"by_pr"`
	BySlice *int                `json:"by_slice"`
	Reason  *string             `json:"reason"`
}

// outcome is o as the result carries it.
func (o *finalOutcome) outcome() *adapter.Outcome {
	out := &adapter.Outcome{Kind: o.Kind}
	if o.ByPR != nil {
		out.ByPR = *o.ByPR
	}
	if o.BySlice != nil {
		out.BySlice = *o.BySlice
	}
	if o.Reason != nil {
		out.Reason = *o.Reason
	}
	return out
}

// decide reads final, Codex's last agent message, under answer contract ans for attempt a: the result to publish
// and, for a succeeded one, the answer document to write, indented, which ans.Check accepts; a succeeded result
// names ans.File as its one output, and a failed one has no outputs and writes no document. No final message, one
// that is not exactly a finalMessage, or a succeeded one without an answer the contract accepts is an error
// wrapping ErrNoAnswer.
func decide(a Args, ans Answer, final *string) (adapter.Result, []byte, error) {
	refuse := func(format string, args ...any) (adapter.Result, []byte, error) {
		return adapter.Result{}, nil, fmt.Errorf("%w: %s", ErrNoAnswer, fmt.Sprintf(format, args...))
	}
	if final == nil {
		return refuse("Codex ended with no final message")
	}
	text := strings.TrimSpace(*final)
	// A model sometimes fences its JSON despite the instruction; the fence alone is not the answer's fault.
	if body, ok := strings.CutPrefix(text, "```json"); ok {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
	} else if body, ok := strings.CutPrefix(text, "```"); ok {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var m finalMessage
	if err := dec.Decode(&m); err != nil {
		return refuse("final message is not the answer object: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return refuse("final message has more than one JSON value")
	}
	if m.Status == nil || m.Summary == nil {
		return refuse("final message lacks status or summary")
	}
	r := adapter.Result{Identity: a.Identity, Status: *m.Status, Summary: *m.Summary, Outputs: []adapter.Output{}}
	if m.Outcome != nil {
		switch {
		case !ans.Outcomes:
			return refuse("the step reports no outcome, but the final message has one")
		case *m.Status != adapter.StatusFailed:
			return refuse("an outcome pairs with status %q, not %q", adapter.StatusFailed, *m.Status)
		}
		r.Outcome = m.Outcome.outcome()
		if err := r.Validate(); err != nil {
			return refuse("outcome: %v", err)
		}
	}
	switch *m.Status {
	case adapter.StatusFailed:
		return r, nil, nil
	case adapter.StatusSucceeded:
	default:
		return refuse("status %q must be %q or %q", *m.Status, adapter.StatusSucceeded, adapter.StatusFailed)
	}
	if len(m.Answer) == 0 || string(m.Answer) == "null" {
		return refuse("a succeeded final message has no answer")
	}
	var doc bytes.Buffer
	if err := json.Indent(&doc, m.Answer, "", "  "); err != nil {
		return refuse("answer: %v", err)
	}
	doc.WriteByte('\n')
	if ans.Check != nil {
		if err := ans.Check(filepath.Dir(a.PromptFile), doc.Bytes()); err != nil {
			return refuse("answer: %v", err)
		}
	}
	r.Outputs = []adapter.Output{{Path: ans.File}}
	return r, doc.Bytes(), nil
}

// versionLine is `codex --version`'s output: codex-cli and a semantic version, perhaps with a suffix.
var versionLine = regexp.MustCompile(`^codex-cli (\d+)\.(\d+)\.(\d+)(?:[-+][0-9A-Za-z.-]+)?$`)

// CheckVersion runs `codex --version` and returns the version it reports, refusing with ErrVersion one older than
// MinVersion or output that is not one `codex-cli X.Y.Z` line.
func CheckVersion(ctx context.Context, program string) (string, error) {
	out, err := probe(ctx, program, "--version")
	if err != nil {
		return "", fmt.Errorf("%w: codex --version: %v", ErrVersion, err)
	}
	line := strings.TrimSpace(string(out))
	m := versionLine.FindStringSubmatch(line)
	if m == nil {
		return "", fmt.Errorf("%w: codex --version printed %q, not codex-cli X.Y.Z", ErrVersion, line)
	}
	var got [3]int
	for i := range got {
		if got[i], err = strconv.Atoi(m[i+1]); err != nil {
			return "", fmt.Errorf("%w: codex --version printed %q", ErrVersion, line)
		}
	}
	for i := range got {
		if got[i] != MinVersion[i] {
			if got[i] < MinVersion[i] {
				return "", fmt.Errorf("%w: codex-cli %s is older than %d.%d.%d", ErrVersion, strings.TrimPrefix(line, "codex-cli "), MinVersion[0], MinVersion[1], MinVersion[2])
			}
			break
		}
	}
	return strings.TrimPrefix(line, "codex-cli "), nil
}

// CheckLogin runs `codex login status`, refusing with ErrNotLoggedIn unless it exits 0 reporting "Logged in".
func CheckLogin(ctx context.Context, program string) error {
	out, err := probe(ctx, program, "login", "status")
	text := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(text, "Logged in") {
		return fmt.Errorf("%w: codex login status: %q %v; log in with `codex login` on this machine", ErrNotLoggedIn, text, err)
	}
	return nil
}

// UserConfig is the user's Codex configuration file: config.toml in $CODEX_HOME, or in ~/.codex without one.
func UserConfig() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// CheckUserConfig refuses with ErrUserMCPServers a user Codex configuration (UserConfig) that declares any MCP
// server: such servers run outside the sandbox, and until evidence shows `-c mcp_servers={}` switches them off,
// the wrapper does not rely on it alone. A missing file declares none; one that cannot be read or parsed is refused.
func CheckUserConfig() error {
	path, err := UserConfig()
	if err != nil {
		return fmt.Errorf("codex adapter: user configuration: %w", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("codex adapter: user configuration: %w", err)
	}
	var conf struct {
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &conf); err != nil {
		return fmt.Errorf("codex adapter: user configuration %s: %w", path, err)
	}
	if len(conf.MCPServers) > 0 {
		names := make([]string, 0, len(conf.MCPServers))
		for name := range conf.MCPServers {
			names = append(names, name)
		}
		slices.Sort(names)
		return fmt.Errorf("%w: %s declares %s, which would run outside the read-only sandbox; remove them to run Codex attempts", ErrUserMCPServers, path, strings.Join(names, ", "))
	}
	return nil
}

// CheckFlags runs `codex exec --help` and refuses with ErrMissingFlag a Codex that does not list each of ExecFlags;
// it reports whether OutputSchemaFlag is listed.
func CheckFlags(ctx context.Context, program string) (outputSchema bool, err error) {
	out, err := probe(ctx, program, "exec", "--help")
	if err != nil {
		return false, fmt.Errorf("%w: codex exec --help: %v", ErrMissingFlag, err)
	}
	listed := func(flag string) bool {
		return regexp.MustCompile(`(^|[\s,\[])` + regexp.QuoteMeta(flag) + `($|[\s,=\]<])`).Match(out)
	}
	var missing []string
	for _, f := range ExecFlags {
		if !listed(f) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Errorf("%w: codex exec --help does not list %s", ErrMissingFlag, strings.Join(missing, ", "))
	}
	return listed(OutputSchemaFlag), nil
}

// probe runs program with args, no standard input, bounded by probeTimeout, and returns its combined output.
func probe(ctx context.Context, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// WorkbenchPath is the Workbench event file the wrapper appends for the attempt whose result file is result:
// workbench.File beside it.
func WorkbenchPath(result string) string {
	return filepath.Join(filepath.Dir(result), workbench.File)
}

// openEvents opens the attempt's Workbench event file for append, each line with red's values redacted; one that
// cannot be opened writes no events.
func openEvents(result string, red *secrets.Redactor) (*workbench.Writer, func()) {
	f, err := os.OpenFile(WorkbenchPath(result), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, func() {}
	}
	return workbench.NewWriter(red.Writer(f)), func() { _ = f.Close() }
}

// exitStatus is the exit status err reports for Codex's process: 0 for none, the process's status, or -1.
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

func cancelledErr(marked bool) error {
	if marked {
		return adapter.ErrAttemptCancelled
	}
	return nil
}

// watchMarker returns ctx ended with cause errMarked once a's cancel marker is present.
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

// lockedWriter serializes writes to w. It has only Write, so os/exec copies into it with Write alone.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// stop asks Codex to stop as Ctrl+C would, or kills it where no interrupt can be sent, as on Windows.
func stop(p *os.Process) error {
	if err := p.Signal(os.Interrupt); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}
