// Package codextest is a stand-in Codex CLI for tests: the test binary itself, installed on PATH under the name
// `codex`, answers `codex --version`, `codex login status` and `codex exec --help`, and plays a `codex exec --json`
// run that makes no model calls. Its streams are shaped as codex-cli 0.163.0's
// (docs/evidence/codex-exec-policy-2026-11-03.txt, section 5). It checks the read-only arguments the Codex wrapper
// passes, records them, reads the prompt file the instruction names, and acts on one prompt line
// `codex-stub: <behaviour>`, an adaptertest.Behaviour or one of this package's; a prompt without one succeeds.
// A succeeded answer judges every pending criterion of the self-review beside the prompt (inputs/self-review.json).
package codextest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
)

// Directive starts the prompt line that chooses the stub's behaviour.
const Directive = "codex-stub:"

// Summary is the summary of every final message the stub returns.
const Summary = "The stub judged the change."

// Behaviours beyond adaptertest's.
const (
	// TurnFailed runs out of quota: an error event, then a failed turn, then a failing exit.
	TurnFailed adaptertest.Behaviour = "turn-failed"
	// NoUsage succeeds with a completed turn that reports no usage block.
	NoUsage adaptertest.Behaviour = "no-usage"
	// NoAnswer returns a succeeded final message whose answer is null.
	NoAnswer adaptertest.Behaviour = "no-answer"
	// Fenced succeeds with its final message inside a ```json fence.
	Fenced adaptertest.Behaviour = "fenced"
)

// QuotaMessage is the error TurnFailed reports.
const QuotaMessage = "You've hit your usage limit. Try again later."

// StreamUsage is the usage block of every completed stub turn, and Usage what the wrapper records from it.
var (
	StreamUsage = map[string]int64{"input_tokens": 128317, "cached_input_tokens": 114944, "cache_write_input_tokens": 512, "output_tokens": 699, "reasoning_output_tokens": 54}
	Usage       = adapter.Usage{Tokens: adapter.Measured, InputTokens: 128317 - 114944, CacheReadInputTokens: 114944, CacheCreationInputTokens: 512, OutputTokens: 699, Cost: adapter.Unknown}
)

// Environment variables that shape the stub's answers to the wrapper's checks.
const (
	// VersionEnv replaces the `codex --version` line, "codex-cli 0.163.0".
	VersionEnv = "GUILDHALL_TEST_CODEX_VERSION"
	// LoggedOutEnv, when set, makes `codex login status` report no login and exit 1.
	LoggedOutEnv = "GUILDHALL_TEST_CODEX_LOGGED_OUT"
	// HelpDropEnv names flags, separated by spaces, that `codex exec --help` does not list.
	HelpDropEnv = "GUILDHALL_TEST_CODEX_HELP_DROP"
)

// Version is the version the stub reports by default.
const Version = "codex-cli 0.163.0"

// HangLimit bounds how long a hanging stub works when nothing stops it.
const HangLimit = 2 * time.Minute

// ArgsFile is the record, beside the prompt file, of the arguments the stub's `codex exec` was given.
const ArgsFile = "codex-stub-args.json"

// PromptLine is the instruction line naming the prompt file.
const PromptLine = "Prompt file: "

// Prompt is the prompt that makes the stub produce b.
func Prompt(b adaptertest.Behaviour) string {
	return "Judge the change.\n" + Directive + " " + string(b) + "\n"
}

// SelfReview is the self-review the attempts Attempt lays out give the judge: one score, B2, and one gate, G2,
// pending.
const SelfReview = `{"schema": "guildhall.self-review/1", "repository": "app", "task": "task-1", "number": 1,
 "base": "fedcba9876543210fedcba9876543210fedcba98", "head": "0123456789abcdef0123456789abcdef01234567",
 "criteria": [{"id": "B2", "kind": "score", "judge": "pending"}, {"id": "G2", "kind": "gate", "judge": "pending"},
  {"id": "A1", "kind": "score", "judge": "done"}]}
`

// Attempt lays out a quality judge attempt as quality.Judge does: the prompt, carrying prompt, in prompt/ with the
// self-review in prompt/inputs, an empty working directory work/, and the result file beside them, for task-1's
// attempt quality-judge-1.
func Attempt(t *testing.T, prompt string) adapter.Spec {
	t.Helper()
	dir := t.TempDir()
	spec := Lay(t, adapter.Identity{TaskID: "task-1", AttemptID: "quality-judge-1"}, dir, prompt)
	spec.ResultFile = filepath.Join(dir, "result.json")
	return spec
}

// Lay lays out a quality judge attempt for id in dir as Attempt does, the prompt, carrying prompt, in dir/prompt
// with the self-review in its inputs and an empty working directory dir/work, and returns its spec with no result
// file, for a caller whose result file lives elsewhere, such as a launched attempt's.
func Lay(t *testing.T, id adapter.Identity, dir, prompt string) adapter.Spec {
	t.Helper()
	spec := adapter.Spec{
		Identity:   id,
		WorkDir:    filepath.Join(dir, "work"),
		PromptFile: filepath.Join(dir, "prompt", render.PromptFile),
	}
	for _, f := range []struct{ path, data string }{
		{spec.PromptFile, prompt},
		{filepath.Join(dir, "prompt", render.InputsDir, "self-review.json"), SelfReview},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(f.data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(spec.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return spec
}

// Install puts the stub on PATH as `codex` for t and its child processes, ahead of any real Codex CLI, names it in
// interp.CodexStubEnv so the interpreter's test-binary guard admits the codex adapter, and points CODEX_HOME at an
// empty directory so no user configuration is read.
func Install(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := codex.Program
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.Link(exe, dst); err != nil {
		if err := copyFile(exe, dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(interp.CodexStubEnv, dst)
	t.Setenv("CODEX_HOME", t.TempDir())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// RunIfStub makes the test binary act as the stub, and exit, when it was started as `codex`. Call it first in
// TestMain.
func RunIfStub() {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != codex.Program {
		return
	}
	code, err := stub(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex stub: %v\n", err)
		os.Exit(2)
	}
	os.Exit(code)
}

// RecordedArgs returns the arguments the stub's `codex exec` for the prompt file prompt was given.
func RecordedArgs(prompt string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(prompt), ArgsFile))
	if err != nil {
		return nil, err
	}
	var args []string
	return args, json.Unmarshal(data, &args)
}

func stub(args []string) (int, error) {
	switch {
	case slices.Equal(args, []string{"--version"}):
		fmt.Println(cmpOr(os.Getenv(VersionEnv), Version))
		return 0, nil
	case slices.Equal(args, []string{"login", "status"}):
		if os.Getenv(LoggedOutEnv) != "" {
			fmt.Fprintln(os.Stderr, "Not logged in")
			return 1, nil
		}
		fmt.Fprintln(os.Stderr, "Logged in using ChatGPT")
		return 0, nil
	case slices.Equal(args, []string{"exec", "--help"}):
		drop := strings.Fields(os.Getenv(HelpDropEnv))
		fmt.Println("Run Codex non-interactively\n\nUsage: codex exec [OPTIONS] [PROMPT]\n\nOptions:")
		for _, f := range []string{"-c, --config <key=value>", "--enable <FEATURE>", "--disable <FEATURE>", "-m, --model <MODEL>",
			"-s, --sandbox <SANDBOX_MODE>", "-C, --cd <DIR>", "--skip-git-repo-check", "--ephemeral", "--output-schema <FILE>",
			"--json", "-o, --output-last-message <FILE>"} {
			if !slices.ContainsFunc(drop, func(d string) bool {
				return slices.Contains(strings.FieldsFunc(f, func(r rune) bool { return r == ' ' || r == ',' }), d)
			}) {
				fmt.Println("      " + f)
			}
		}
		return 0, nil
	case len(args) > 0 && args[0] == "exec":
		return run(args)
	}
	return 0, fmt.Errorf("unexpected arguments %q", args)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// run checks a `codex exec` command line as the wrapper passes it, records it and plays the prompt's behaviour.
func run(args []string) (int, error) {
	if len(args) < 2 {
		return 0, fmt.Errorf("exec without a prompt: %q", args)
	}
	flags, instruction := args[1:len(args)-1], args[len(args)-1]
	for _, want := range [][]string{{"--json"}, {"--ephemeral"}, {"--skip-git-repo-check"}, {"--sandbox", codex.Sandbox}, codex.Switches} {
		if !containsRun(flags, want) {
			return 0, fmt.Errorf("want %q among %q", want, flags)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	if i := slices.Index(flags, "-C"); i < 0 || i+1 >= len(flags) || flags[i+1] != wd {
		return 0, fmt.Errorf("want -C %s, got %q", wd, flags)
	}
	if i := slices.Index(flags, codex.OutputSchemaFlag); i >= 0 {
		data, err := os.ReadFile(flags[i+1])
		if err != nil || !json.Valid(data) {
			return 0, fmt.Errorf("--output-schema %s is not a JSON file: %v", flags[i+1], err)
		}
	}
	var promptFile string
	sc := bufio.NewScanner(strings.NewReader(instruction))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), PromptLine); ok {
			promptFile = v
		}
	}
	if promptFile == "" {
		return 0, fmt.Errorf("instruction names no prompt file: %q", instruction)
	}
	data, err := json.Marshal(args)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(promptFile), ArgsFile), data, 0o600); err != nil {
		return 0, err
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return 0, err
	}
	b := adaptertest.Succeed
	sc = bufio.NewScanner(strings.NewReader(string(prompt)))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), Directive); ok {
			b = adaptertest.Behaviour(strings.TrimSpace(v))
		}
	}
	return play(b, filepath.Dir(promptFile))
}

// containsRun reports whether want appears in flags as consecutive elements.
func containsRun(flags, want []string) bool {
	for i := 0; i+len(want) <= len(flags); i++ {
		if slices.Equal(flags[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// play writes b's --json stream to stdout and returns its exit status.
func play(b adaptertest.Behaviour, promptDir string) (int, error) {
	emit := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Println(string(data))
	}
	item := func(typ string, it map[string]any) { emit(map[string]any{"type": typ, "item": it}) }
	emit(map[string]any{"type": "thread.started", "thread_id": "01a1a09c-a5b0-7c40-811d-7afc2d4c145d"})
	emit(map[string]any{"type": "turn.started"})
	if b != adaptertest.NoResult {
		item("item.completed", map[string]any{"id": "item_0", "type": "agent_message", "text": "I am reading the self-review."})
	}
	cmd := map[string]any{"id": "item_1", "type": "command_execution", "command": "/usr/bin/zsh -lc 'cat inputs/self-review.json'", "aggregated_output": "", "exit_code": nil, "status": "in_progress"}
	item("item.started", cmd)
	cmd["aggregated_output"], cmd["exit_code"], cmd["status"] = SelfReview, 0, "completed"
	item("item.completed", cmd)
	touch := map[string]any{"id": "item_2", "type": "command_execution", "command": "/usr/bin/zsh -lc 'touch notes.txt'", "aggregated_output": "", "exit_code": nil, "status": "in_progress"}
	item("item.started", touch)
	touch["aggregated_output"], touch["exit_code"], touch["status"] = "touch: cannot touch 'notes.txt': Read-only file system\n", 1, "failed"
	item("item.completed", touch)
	if b == adaptertest.Hang {
		hang()
		return 0, nil
	}
	if b == TurnFailed {
		emit(map[string]any{"type": "error", "message": QuotaMessage})
		emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": QuotaMessage}})
		return 1, nil
	}
	answer, err := judge(promptDir)
	if err != nil {
		return 0, err
	}
	final := map[string]any{"status": "succeeded", "summary": Summary, "answer": answer}
	switch b {
	case adaptertest.Succeed, adaptertest.ExitErrorAfterResult, NoUsage, Fenced:
	case adaptertest.Fail:
		final["status"], final["answer"] = "failed", nil
	case NoAnswer:
		final["answer"] = nil
	case adaptertest.WrongIdentity:
		// An answer for another self-review: it judges a criterion this one does not have pending.
		answer["criteria"] = append(answer["criteria"].([]any), map[string]any{"id": "H9", "score": 1, "pass": nil, "evidence": "elsewhere"})
	case adaptertest.Malformed, adaptertest.NoResult:
	default:
		return 0, fmt.Errorf("unknown behaviour %q", b)
	}
	data, _ := json.Marshal(final)
	text := string(data)
	switch b {
	case adaptertest.Malformed:
		text = `Here is my judgement: {"status": "succeeded", "summary": `
	case Fenced:
		text = "```json\n" + text + "\n```"
	}
	if b != adaptertest.NoResult {
		item("item.completed", map[string]any{"id": "item_3", "type": "agent_message", "text": text})
	}
	turn := map[string]any{"type": "turn.completed", "usage": StreamUsage}
	if b == NoUsage {
		delete(turn, "usage")
	}
	emit(turn)
	if b == adaptertest.ExitErrorAfterResult {
		return 1, nil
	}
	return 0, nil
}

// judge is an answer to the self-review beside the prompt in promptDir: each pending score 2 and each pending gate
// a pass.
func judge(promptDir string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(promptDir, render.InputsDir, "self-review.json"))
	if err != nil {
		return nil, err
	}
	sr, err := quality.ReadSelfReview(data)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sr.Pending))
	for id := range sr.Pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	criteria := []any{}
	for _, id := range ids {
		c := map[string]any{"id": id, "score": nil, "pass": nil, "evidence": "internal/x/x_test.go:12 names its cases"}
		if sr.Pending[id] == quality.KindScore {
			c["score"] = 2
		} else {
			c["pass"] = true
		}
		criteria = append(criteria, c)
	}
	return map[string]any{"merge_as_is": "yes", "criteria": criteria}, nil
}

// hang works without an answer until stopped, its parent (the wrapper) ends, or HangLimit.
func hang() {
	deadline := time.Now().Add(HangLimit)
	gone := parentGone()
	for time.Now().Before(deadline) {
		select {
		case <-gone:
			return
		case <-time.After(50 * time.Millisecond): // timing:allow parent-liveness polling; HangLimit bounds the fixture lifetime.
		}
	}
}
